package file

import (
	"strings"
	"testing"
)

func pointer(value string) *string { return &value }

func TestSedAmbiguityRefusal(t *testing.T) {
	_, err := Transform(Mutation{Verb: VerbSed, Path: "x", Find: pointer("a"), Replace: pointer("b")}, []byte("a a"))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity refusal, got %v", err)
	}
}

func TestSedCountAndCRLFPreservation(t *testing.T) {
	result, err := Transform(Mutation{Verb: VerbSed, Path: "x", Find: pointer("a"), Replace: pointer("b"), Count: 2}, []byte("a\r\na\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "b\r\nb\r\n" {
		t.Fatalf("CRLF changed: %q", result.Data)
	}
}

func TestMultiSpanBottomUpAndOverlapRefusal(t *testing.T) {
	first := "ONE"
	second := "THREE"
	result, err := TransformEdits([]Mutation{
		{Verb: VerbEdit, Path: "x", StartLine: 1, EndLine: 1, NewString: &first},
		{Verb: VerbEdit, Path: "x", StartLine: 3, EndLine: 3, NewString: &second},
	}, []byte("one\ntwo\nthree"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Data) != "ONE\ntwo\nTHREE" {
		t.Fatalf("bottom-up edit mismatch: %q", result.Data)
	}
	_, err = TransformEdits([]Mutation{
		{Verb: VerbEdit, Path: "x", Spans: []EditSpan{{StartLine: 1, EndLine: 2, NewString: "x"}, {StartLine: 2, EndLine: 3, NewString: "y"}}},
	}, []byte("one\ntwo\nthree"))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap refusal, got %v", err)
	}
}

func TestEndGuardRelocationAndAutoSnap(t *testing.T) {
	replacement := "  if false {\n    b()\n  }"
	result, err := Transform(Mutation{
		Verb: VerbEdit, Path: "x", StartLine: 2, EndLine: 3,
		StartGuard: "  if true {", EndGuard: "    a()", NewString: &replacement,
	}, []byte("func x() {\n  if true {\n    a()\n  }\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Spans) != 1 || !result.Spans[0].Adjusted || result.Spans[0].AppliedEnd != 4 {
		t.Fatalf("expected closer-only auto-snap: %#v", result.Spans)
	}

	relocated := "changed"
	result, err = Transform(Mutation{
		Verb: VerbEdit, Path: "x", StartLine: 1, StartGuard: "target", EndGuard: "end", NewString: &relocated,
	}, []byte("prefix\ntarget\nmiddle\nend\nsuffix"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Spans[0].Relocated || string(result.Data) != "prefix\nchanged\nsuffix" {
		t.Fatalf("guard relocation failed: %#v %q", result.Spans, result.Data)
	}
}
