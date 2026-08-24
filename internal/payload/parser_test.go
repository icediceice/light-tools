package payload

import (
	"strings"
	"testing"
)

func TestParseExactSealAndBatch(t *testing.T) {
	input := strings.Join([]string{
		"@file /tmp/a",
		"@verb write",
		"@content",
		"alpha",
		"<<LF-END >>",
		"omega",
		"<<LF-END>>",
		"@file /tmp/b",
		"@verb sed",
		"@find",
		"x",
		"<<LF-END>>",
		"@replace",
		"y",
		"<<LF-END>>",
	}, "\n")
	mutations, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutations) != 2 {
		t.Fatalf("got %d mutations", len(mutations))
	}
	if got := *mutations[0].Content; got != "alpha\n<<LF-END >>\nomega" {
		t.Fatalf("near-miss seal was not preserved: %q", got)
	}
	if *mutations[1].Find != "x" || *mutations[1].Replace != "y" {
		t.Fatalf("sed bodies parsed incorrectly: %#v", mutations[1])
	}
}

func TestParseUnterminatedWritesNothing(t *testing.T) {
	_, err := Parse("@verb write\n@path /tmp/a\n@content\nvalue")
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("expected unterminated error, got %v", err)
	}
}

func TestParseRendersCaretEnvelope(t *testing.T) {
	_, err := Parse("@verb write\nnot-a-header")
	if err == nil {
		t.Fatal("expected parse error")
	}
	rendered := err.Error()
	// "at: payload, line 2, column 1" — the source name prefixes the position
	// rather than replacing it, so both substrings must survive.
	for _, expected := range []string{
		"error[E_PAYLOAD]",
		"at: payload, ",
		"line 2, column 1",
		"fix: correct the call arguments and retry",
		"2 | not-a-header",
		"| ^",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("caret envelope missing %q:\n%s", expected, rendered)
		}
	}
}
