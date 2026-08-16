package payload

import (
	"fmt"
	"strings"
	"testing"
)

func TestAssemblerResumesClippedBody(t *testing.T) {
	assembler := NewAssembler()
	_, partial, err := assembler.Assemble("@verb write\n@path /tmp/a\n@content\nalpha")
	if err != nil || partial == nil {
		t.Fatalf("expected partial stage, got %#v %v", partial, err)
	}
	resume := fmt.Sprintf("@stage %s\n@from_line %d\nomega\n<<LF-END>>", partial.Stage, partial.GotLines+1)
	mutations, next, err := assembler.Assemble(resume)
	if err != nil || next != nil {
		t.Fatalf("resume failed: %#v %v", next, err)
	}
	if got := *mutations[0].Content; got != "alpha\nomega" {
		t.Fatalf("resumed body mismatch: %q", got)
	}
	if _, _, err := assembler.Assemble(resume); err == nil || !strings.Contains(err.Error(), "unknown or expired") {
		t.Fatalf("completed stage should be consumed, got %v", err)
	}
}

func TestParseSpansBody(t *testing.T) {
	input := "@verb edit\n@path /tmp/a\n@spans\n" +
		`[{"start_line":4,"end_line":5,"start_guard":"a","new_string":"b"}]` +
		"\n<<LF-END>>"
	mutations, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutations[0].Spans) != 1 || mutations[0].Spans[0].StartLine != 4 {
		t.Fatalf("spans not parsed: %#v", mutations[0].Spans)
	}
}
