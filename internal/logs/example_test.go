package logs

import (
	"fmt"
	"strings"
)

// The examples in this file are the source of the before/after samples in
// README.md. They are golden: `go test ./internal/logs/` compares the rendered
// output against the Output block below, so a change to the template engine
// breaks this test rather than silently invalidating the README.
//
// Never hand-edit an Output block. Blank it, run the test, and paste back what
// the failure prints.

// ExampleAnalyze_climbingCounter is the case exact-duplicate deduplication
// cannot touch. No two of these lines are byte-identical, so a dedup pass
// collapses nothing and the climbing restart counter stays buried in the
// noise. Template collapse groups them by shape and reports the value that
// actually varied — including that it is a counter incrementing by one.
func ExampleAnalyze_climbingCounter() {
	var b strings.Builder
	for i := 599; i <= 614; i++ {
		fmt.Fprintf(&b, "Aug 21 03:26:59 ice-server systemd[2158]: light-edge.service: Scheduled restart job, restart counter is at %d.\n", i)
	}
	fmt.Println(Analyze(b.String(), noFloor()).Text)
	// Output:
	// [L1-16]       light-edge.service: Scheduled restart job, restart counter is at ▪1.  ×16
	//     ▪1: 599..614  (16 values, +1 each)
}

// ExampleAnalyze_loneVerdict is the property that makes a collapsed view
// trustworthy. A verdict line occurs exactly once by nature, so any design
// that suppresses singleton groups deletes precisely the line the reader came
// for. It survives verbatim.
func ExampleAnalyze_loneVerdict() {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "compiling module %d of 500\n", i)
	}
	b.WriteString("BUILD FAILED\n")
	fmt.Println(Analyze(b.String(), noFloor()).Text)
	// Output:
	// [L1-500]      compiling module ▪1 of 500  ×500
	//     ▪1: 0..499  (500 values, +1 each)
	// [L501]        BUILD FAILED
}
