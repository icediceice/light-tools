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
