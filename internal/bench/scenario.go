// Package bench measures what a coding agent actually receives.
//
// It answers ONE question: for a fixed information need, how many bytes must be
// delivered into a model's context to satisfy it — with light-tools, and
// without? Everything here is offline and deterministic. No model runs, no
// network, no clock, no randomness: the same checkout always produces the same
// report, so a number in docs/BENCHMARK.md can be re-derived by anyone.
//
// # WHAT THIS MEASURES, AND WHAT IT DOES NOT
//
// It measures DELIVERED CONTEXT COST. It does NOT measure task success, wall
// time, or whether an agent reasoned better. A benchmark that cannot separate
// those two claims is a benchmark that will eventually be used to make the
// wrong one, so the distinction is stated here, in the report, and in the
// README.
//
// # THE HONESTY CONTROL, IN TWO DIRECTIONS
//
// Delivering fewer bytes is trivial if you are allowed to delete the answer.
// So every scenario carries the QUESTION a reader came to the corpus with and
// a regexp matching the fact that answers it. The rule is deliberately
// asymmetric, because the two directions of failure mean different things:
//
//   - If the LIGHT arm loses the answer, the suite FAILS. We are the ones
//     making a claim here, and a claim that rests on having deleted the line
//     the reader came for is not a claim worth shipping.
//
//   - If a BASELINE arm loses the answer, that is RECORDED, not fatal. It is
//     a genuine and common outcome: a grep is only as good as the pattern you
//     could think of before you had the answer, and a plausible pattern that
//     misses is the everyday failure of searching a log you do not yet
//     understand. Suppressing those rows would hide the most informative
//     result the suite produces.
//
// What must never happen is CREDITING a baseline for bytes it saved by
// missing. So a row whose baseline lost the answer is marked in the report and
// quotes no ratio: comparing against an arm that did not answer the question
// is not a comparison.
package bench

import "regexp"

// The three arms. Two of them model a native toolchain; naming them as
// constants keeps the report and the assertions from drifting apart.
const (
	// ArmNaive is the default behaviour of an agent that does not yet know the
	// shape of what it is looking at: read the whole file, take the whole
	// stream. It is the honest description of a first contact with an unknown
	// corpus, not a straw man — but it is also not the only thing a competent
	// agent does, which is why ArmSkilled exists.
	ArmNaive = "native-naive"

	// ArmSkilled is a native toolchain used WELL: grep for the pattern first,
	// then read a bounded window around the hit. It is the strong baseline and
	// the one the headline ratio should be quoted against. Comparing only
	// against ArmNaive would be marketing.
	//
	// It costs an extra round trip, which Observation.Calls records: the agent
	// must see the grep output before it knows which window to ask for.
	ArmSkilled = "native-skilled"

	// ArmLight is light-tools as it actually ships — the real handlers, the
	// real options, driven through the same surface a model reaches.
	ArmLight = "light-tools"
)

// Track separates the two questions the suite answers. They are reported apart
// because they behave differently: log reading is dominated by repetition
// collapse, code reading by targeted extraction, and averaging the two would
// describe neither.
const (
	TrackLogs = "logs"
	TrackCode = "code"
)

// Observation is one arm's outcome for one scenario.
type Observation struct {
	// Arm is one of the three constants above.
	Arm string

	// Delivered is the byte count handed to the model. For the light arm this
	// is the real result payload, not an internal intermediate: a saving that
	// only exists before serialisation is not a saving.
	Delivered int

	// Calls is how many model-visible round trips the arm needs. A grep-then-
	// read baseline that delivers few bytes still costs a turn to discover
	// which bytes to ask for, and a byte-only table would hide that.
	Calls int

	// Text is what was delivered, retained so the answer check runs against
	// the ACTUAL payload rather than against a promise about it.
	Text string
}

// AnswerSurvives reports whether this observation still contains every fact the
// scenario's question asked for.
//
// All-of, deliberately. These questions are compound — "which file broke, and
// what was the error" — so a single regexp over one clause would let an arm
// drop the other half and still pass the control that exists to catch exactly
// that. An empty set never passes: a scenario that asserts nothing is a
// scenario that proves nothing.
func (o Observation) AnswerSurvives(answers []*regexp.Regexp) bool {
	if len(answers) == 0 {
		return false
	}
	for _, answer := range answers {
		if !answer.MatchString(o.Text) {
			return false
		}
	}
	return true
}

// Scenario is one information need, measured across every arm.
type Scenario struct {
	// Name is the stable identifier used as the report's row key.
	Name string

	// Track is TrackLogs or TrackCode.
	Track string

	// Question is what a reader came to this corpus to find out, in plain
	// words. It is printed in the report so the answer regexp below can be
	// judged as a fair test of it rather than taken on trust.
	Question string

	// Corpus describes the input and — critically — whether it is REAL
	// captured material or SYNTHETIC material generated by committed code.
	// The report prints this verbatim per scenario. A synthetic corpus shaped
	// to flatter the tool is the most likely way this suite could mislead, so
	// the provenance travels with every row.
	Corpus string

	// CorpusBytes is the size of the underlying material, before any arm
	// decides what to deliver.
	CorpusBytes int

	// Answers matches the facts that answer Question — ALL of them, one entry
	// per clause. See the package comment: this is the honesty control, and a
	// compound question given a single regexp only tests half of itself.
	//
	// The rule this set enforces is NOT universal; the two marks below name its
	// exceptions, and the report states them rather than claiming an absolute
	// it then contradicts.
	Answers []*regexp.Regexp

	// Adversarial marks a scenario chosen because light-tools is NOT expected
	// to win it — a corpus under the pass-through floor, or a file small
	// enough that reading all of it was already the right call.
	//
	// These are not filler. A suite containing only cases the tool wins
	// measures the suite author, not the tool. The report labels these rows
	// and states the expectation, so a ~1.0x ratio there reads as the design
	// working rather than as a defect.
	Adversarial bool

	// LightLosesAnswer marks a scenario where light-tools is KNOWN not to
	// deliver the answer, and where that is reported rather than hidden.
	//
	// It exists because the alternative was worse. The suite's default rule is
	// that a light arm losing the answer fails the build — the right default,
	// since it stops a flattering claim shipping. But a loss that is measured,
	// displayed and explained is not a flattering claim; it is the most
	// informative row the suite produces. Deleting the scenario to keep the
	// build green would be the actual dishonesty.
	//
	// The assertion INVERTS for these rows, so the marking cannot rot: the
	// light arm must still fail to answer. If it starts answering, the
	// documented limitation is stale and the test fails until the report is
	// corrected. And because elision must always imply recovery, a marked row
	// must still carry a resolvable pointer to the exact bytes.
	LightLosesAnswer bool

	// ContextCarried marks a scenario where the light arm deliberately does
	// NOT re-deliver the answer, because the reader already holds it.
	//
	// Read-dedup is the only such case: on a repeat read of unchanged bytes
	// the tool returns "[dedup] <path> sha256:<hash> lines N-M" instead of the
	// content (filetool/read.go:readWindow). That is legitimate precisely
	// BECAUSE the content is already in context from the first read — and it
	// means a naive answer-survival check on the second payload would be
	// asking the wrong question and would fail a correct behaviour.
	//
	// So for these rows the suite asserts something stricter and more
	// meaningful than a regexp over one payload: the FIRST read must have
	// carried the answer, and the second must be a dedup stub identifying the
	// exact bytes it stands for. A stub that did not identify its content
	// would be an unrecoverable elision, which is the one thing this codebase
	// refuses to do anywhere else.
	ContextCarried bool

	// Note explains why this scenario is in the suite. Required for
	// adversarial rows, optional elsewhere.
	Note string

	// Run produces one observation per arm. It must be pure with respect to
	// anything outside the scenario's own corpus and temp files.
	Run func() ([]Observation, error)
}

// Find returns the observation for one arm.
func Find(observations []Observation, arm string) (Observation, bool) {
	for _, observation := range observations {
		if observation.Arm == arm {
			return observation, true
		}
	}
	return Observation{}, false
}

// ratio expresses how many times more bytes a baseline arm delivered than the
// light arm. It is reported per scenario and never aggregated into a single
// headline number across tracks: the two tracks measure different mechanisms,
// and one blended figure would be an average of incomparable things.
func ratio(baseline, light int) float64 {
	if light <= 0 {
		return 0
	}
	return float64(baseline) / float64(light)
}
