package bench

// The three arms.
//
// Each arm answers the same question over the same corpus and reports what it
// had to put in front of the model to do it. The baselines are MODELLED, not
// executed: no real `cat` or `grep` subprocess runs. That is a real limitation
// and the report states it — but the models are deliberately simple and
// generous to the baseline, which is the direction an honest error should run.
//
// THE RULE THAT KEEPS THE SKILLED BASELINE FAIR
//
// A grep is only as good as the pattern you can think of before you have the
// answer. So the skilled arm may use a pattern derived from the QUESTION, and
// never one derived from the ANSWER. Searching a restart log for "restart
// counter" is fair — the question says restart counter. Searching it for "614"
// is not: that is the answer, and assuming it makes the baseline a lookup of a
// fact it already had.
//
// Applied honestly this rule costs light-tools some rows. When the question
// names the string, a skilled grep is extremely efficient and will beat an
// outline on bytes. Those rows stay in the report. The asymmetry the report
// should leave a reader with is not "light always delivers less" — it is that
// grep requires you to already know what you are looking for, and the outline
// does not.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/bash"
	"github.com/icediceice/light-tools/internal/logs"
)

// shippedBudget is the byte ceiling light_bash, light_ssh and light_ops all
// pass to logs.Compact — internal/bash/runner.go:outputLimit,
// internal/remote/transport.go:remoteOutputLimit and
// internal/ops/handler.go:normalDrillCap are each 128 KiB.
//
// The bench passes this and NOTHING else, exactly as those three do: every
// other bound (the per-line clamp, the pass-through floor) stays at the
// package default. Tuning options here that the shipped tools do not pass
// would measure a configuration nobody runs.
const shippedBudget = 128 * 1024

// skilledWindow is how many matching lines the skilled baseline keeps — the
// `| tail -n 40` an agent adds once it sees a grep return more than a screen.
const skilledWindow = 40

// newSpills builds a real spill store rooted under a caller-owned temp dir.
//
// This matters for correctness of the light arm, not just realism:
// logs.Compact with a nil spiller FAILS OPEN and returns the exact raw output
// (deliver.go:Compact then spillFailed), because an outline whose recovery
// pointer does not resolve is worse than no compaction. Benchmarking with a
// nil store would therefore measure the fail-open path and report that
// compaction does nothing.
func newSpills(root string) (*bash.SpillStore, error) {
	return bash.NewSpillStore(root, time.Hour)
}

// LogArms measures one log corpus across all three arms.
//
// grepPattern must be derivable from the scenario's question. See the rule at
// the top of this file.
func LogArms(corpus, grepPattern, spillRoot string) ([]Observation, error) {
	pattern, err := regexp.Compile(grepPattern)
	if err != nil {
		return nil, fmt.Errorf("grep pattern: %w", err)
	}

	// Arm 1 — naive. The whole stream, uncapped.
	//
	// Modelling this as uncapped is generous to light-tools in one direction
	// and the report says so plainly: an agent whose shell tool truncates at N
	// bytes delivers less than this figure. But truncation also silently drops
	// whatever fell outside the cut, frequently the verdict line at the end.
	// Rather than invent a specific competitor's cap, the report prints the
	// corpus size alongside so a reader can apply their own.
	naive := Observation{Arm: ArmNaive, Delivered: len(corpus), Calls: 1, Text: corpus}

	// Arm 2 — skilled. Grep, then keep the last window of hits.
	//
	// Two calls, not one: the agent cannot know which window to ask for until
	// it has seen the grep result. A bytes-only table would hide that turn.
	var matched []string
	for _, line := range strings.Split(corpus, "\n") {
		if pattern.MatchString(line) {
			matched = append(matched, line)
		}
	}
	if len(matched) > skilledWindow {
		matched = matched[len(matched)-skilledWindow:]
	}
	skilledText := strings.Join(matched, "\n")
	if skilledText != "" {
		skilledText += "\n"
	}
	skilled := Observation{Arm: ArmSkilled, Delivered: len(skilledText), Calls: 2, Text: skilledText}

	// Arm 3 — light-tools, through the shipped path.
	spills, err := newSpills(spillRoot)
	if err != nil {
		return nil, fmt.Errorf("spill store: %w", err)
	}
	defer spills.Close()

	stream := logs.Compact(corpus, logs.Options{Budget: shippedBudget}, spills, nil)

	// The recovery pointer is part of what the model receives whenever bytes
	// were elided, so it is part of what this arm costs. Counting the outline
	// but not the pointer that makes the outline trustworthy would understate
	// the arm.
	lightText := stream.Text
	if stream.SpillID != "" {
		lightText += "\n" + logs.RecoveryHint(stream.SpillID)
	}
	light := Observation{Arm: ArmLight, Delivered: len(lightText), Calls: 1, Text: lightText}

	return []Observation{naive, skilled, light}, nil
}
