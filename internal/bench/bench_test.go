package bench

// The suite is the honesty control, not just a runner.
//
// Every assertion here exists to stop a specific way this benchmark could
// produce a flattering number that is not true:
//
//   - a light arm that "saves" by dropping the answer          → TestLightArmAlwaysAnswers
//   - a degraded build silently measuring the no-symbol path   → TestSymbolExtractionIsAvailable
//   - an adversarial row quietly disappearing from the suite   → TestSuiteContainsRowsWeLose
//   - a report that drifts from the code that produced it      → TestBenchmarkReport

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/symbol"
)

var update = flag.Bool("update", false, "regenerate docs/BENCHMARK.md")

// allScenarios builds both tracks against a temp root.
func allScenarios(t *testing.T) []Scenario {
	t.Helper()
	root := t.TempDir()

	scenarios := LogScenarios(root)
	code, err := CodeScenarios(root)
	if err != nil {
		t.Fatalf("build code scenarios: %v", err)
	}
	return append(scenarios, code...)
}

// measure runs every scenario and fills in the corpus size.
func measure(t *testing.T, scenarios []Scenario) []Row {
	t.Helper()
	rows := make([]Row, 0, len(scenarios))
	for _, scenario := range scenarios {
		observations, err := scenario.Run()
		if err != nil {
			t.Fatalf("%s: %v", scenario.Name, err)
		}
		if naive, ok := Find(observations, ArmNaive); ok {
			scenario.CorpusBytes = naive.Delivered
		}
		rows = append(rows, Row{Scenario: scenario, Observations: observations})
	}
	return rows
}

// treesitterAvailable reports whether this build can extract symbols from Go
// source. Without -tags treesitter it cannot (internal/symbol/extract_stub.go),
// and the code track would measure the no-symbol fallback.
func treesitterAvailable() bool {
	symbols, err := symbol.Extract("probe.go", []byte("package p\n\nfunc F() int { return 1 }\n"))
	return err == nil && len(symbols) > 0
}

// The code track needs tree-sitter, but `go test ./...` without tags is an
// ordinary thing to run and must not break — so an untagged run SKIPS the code
// track rather than failing it.
//
// The one case that still fails hard is -update: publishing a report generated
// from the degraded path would put wrong numbers in docs/BENCHMARK.md, and a
// skip there would do it silently.
func TestSymbolExtractionAvailability(t *testing.T) {
	if treesitterAvailable() {
		return
	}
	if *update {
		t.Fatal("refusing to regenerate the report without symbol extraction: " +
			"the code track would measure the no-symbol fallback and publish wrong numbers.\n" +
			"Run: " + ReproduceCommand)
	}
	t.Skip("tree-sitter absent — code track skipped. Full suite: go test -tags treesitter ./internal/bench/")
}

// The claim we are making is ours, so the standard on our own arm is absolute:
// if light-tools does not deliver the answer, the number does not ship.
func TestLightArmAlwaysAnswers(t *testing.T) {
	available := treesitterAvailable()
	for _, row := range measure(t, allScenarios(t)) {
		if row.Scenario.Track == TrackCode && !available {
			continue // covered by TestSymbolExtractionAvailability
		}
		light, ok := Find(row.Observations, ArmLight)
		if !ok {
			t.Fatalf("%s: no light observation", row.Scenario.Name)
		}

		if row.Scenario.ContextCarried {
			// A dedup stub legitimately omits the content, because the reader
			// already holds it. What it may NEVER do is elide without saying
			// what it elided, so assert the stub identifies its own bytes.
			if !strings.Contains(light.Text, "[dedup]") {
				t.Errorf("%s: expected a dedup stub, got %q", row.Scenario.Name, truncate(light.Text))
			}
			if !regexp.MustCompile(`sha256:[0-9a-f]+`).MatchString(light.Text) {
				t.Errorf("%s: dedup stub does not identify the bytes it stands for: %q",
					row.Scenario.Name, truncate(light.Text))
			}
			continue
		}

		if row.Scenario.LightLosesAnswer {
			// The assertion inverts so a documented limitation cannot rot into
			// a stale claim: if this starts answering, the report is wrong.
			if light.AnswerSurvives(row.Scenario.Answers) {
				t.Errorf("%s: marked as a known light-arm loss, but the answer now SURVIVES. "+
					"The documented limitation is stale — update the scenario note and the report.",
					row.Scenario.Name)
			}
			// Elision must always imply recovery. A row we lose is only
			// acceptable because the exact bytes remain one call away.
			if !strings.Contains(light.Text, "read_block") {
				t.Errorf("%s: light arm lost the answer AND carries no recovery pointer. "+
					"An elision the reader cannot undo is not a trade-off, it is data loss.",
					row.Scenario.Name)
			}
			continue
		}

		if !light.AnswerSurvives(row.Scenario.Answers) {
			missing := make([]string, 0, len(row.Scenario.Answers))
			for _, answer := range row.Scenario.Answers {
				if !answer.MatchString(light.Text) {
					missing = append(missing, answer.String())
				}
			}
			t.Errorf("%s: light arm delivered %d bytes but LOST %d of %d answer clauses: %s.\n"+
				"A saving that deletes the line the question asked for is not a saving.\n"+
				"If this is a genuine limitation, mark the scenario LightLosesAnswer and document it "+
				"in the report — do not delete the row.",
				row.Scenario.Name, light.Delivered, len(missing), len(row.Scenario.Answers),
				strings.Join(missing, ", "))
		}
	}
}

// For a ContextCarried row the first read must genuinely have carried the
// answer, or the dedup row is asserting nothing at all.
func TestRepeatReadRowsCarryTheAnswerFirst(t *testing.T) {
	root := t.TempDir()
	paths, err := writeFixtures(root)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newFileHandler(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := callFileTool(handler, map[string]any{
		"verb": "read", "path": paths["service.go"], "offset": 0, "limit": 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "RECONCILE_SENTINEL") {
		t.Fatal("the first read did not contain the answer, so the dedup row proves nothing")
	}
}

// A suite containing only cases the tool wins measures its author. These rows
// are load-bearing, so removing one has to break a test rather than quietly
// improve the averages.
func TestSuiteContainsRowsWeLose(t *testing.T) {
	scenarios := allScenarios(t)

	byTrack := map[string]int{}
	for _, scenario := range scenarios {
		if scenario.Adversarial {
			byTrack[scenario.Track]++
		}
	}
	for _, track := range []string{TrackLogs, TrackCode} {
		if byTrack[track] == 0 {
			t.Errorf("track %q has no adversarial scenario; a suite of only favourable cases "+
				"measures the suite author, not the tool", track)
		}
	}
}

// The adversarial log row sits under the pass-through floor, so light-tools
// must return it byte-for-byte. A "saving" there would mean compaction had
// started touching output that was already legible.
func TestPassThroughFloorIsRespected(t *testing.T) {
	for _, row := range measure(t, allScenarios(t)) {
		if row.Scenario.Name != "short-status" {
			continue
		}
		naive, _ := Find(row.Observations, ArmNaive)
		light, _ := Find(row.Observations, ArmLight)
		if light.Delivered != naive.Delivered {
			t.Errorf("short-status: expected byte-identical pass-through, got %d vs %d",
				light.Delivered, naive.Delivered)
		}
		return
	}
	t.Fatal("short-status scenario is missing — the pass-through canary was removed")
}

// TestBenchmarkReport regenerates docs/BENCHMARK.md with -update and otherwise
// checks it is not stale. Same convention as the README samples in
// internal/logs/example_test.go: never hand-edit the output.
func TestBenchmarkReport(t *testing.T) {
	if !treesitterAvailable() && !*update {
		// The committed report contains code-track rows this build cannot
		// reproduce, so a comparison here would report a false staleness.
		t.Skip("tree-sitter absent — cannot reproduce the code track. Full suite: " + ReproduceCommand)
	}
	rows := measure(t, allScenarios(t))
	rendered := Render(rows)

	path := filepath.Join("..", "..", "docs", "BENCHMARK.md")
	if *update {
		if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(rendered))
		return
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no generated report yet (%v); run: %s", err, ReproduceCommand)
	}
	if string(existing) != rendered {
		line, want, got := firstDivergence(string(existing), rendered)
		t.Errorf("docs/BENCHMARK.md is stale. Regenerate with:\n  %s\n"+
			"first divergence at line %d:\n  committed: %q\n  measured:  %q",
			ReproduceCommand, line, truncate(want), truncate(got))
	}
}

// firstDivergence returns the 1-based line where the committed report and the
// freshly measured one first disagree, plus both versions of that line.
//
// A bare "is stale" names no cause, which is worthless precisely when it is
// needed most: when the staleness reproduces ONLY on another operating
// system's CI runner and cannot be reproduced on the machine that regenerates
// the report. Printing the differing row turns one CI round into a diagnosis
// instead of a guess.
func firstDivergence(committed, measured string) (int, string, string) {
	want := strings.Split(committed, "\n")
	got := strings.Split(measured, "\n")
	limit := len(want)
	if len(got) > limit {
		limit = len(got)
	}
	for index := 0; index < limit; index++ {
		wantLine, gotLine := "<past end>", "<past end>"
		if index < len(want) {
			wantLine = want[index]
		}
		if index < len(got) {
			gotLine = got[index]
		}
		if wantLine != gotLine {
			return index + 1, wantLine, gotLine
		}
	}
	// Equal line-by-line but unequal as strings: a trailing-byte difference.
	return 0, committed[maxInt(0, len(committed)-40):], measured[maxInt(0, len(measured)-40):]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncate(value string) string {
	if len(value) <= 200 {
		return value
	}
	return value[:200] + "…"
}
