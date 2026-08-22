package terse

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeRejectsMalformedTerse(t *testing.T) {
	cases := []string{
		"",
		"{}",
		"~",
		"~{}",
		"~[]",
		"~{missing}",
		"~{a:1",
		"~[a,b|1]",
		"~[a,a|1,2]",
		"~[a,b|1,2|3]",
		"~{a:\"unterminated}",
		"~{a:1} trailing",
	}
	for _, input := range cases {
		if value, err := Decode([]byte(input)); err == nil {
			t.Fatalf("Decode(%q) = %#v, want error", input, value)
		}
	}
}

func TestDecodeKnownGrammar(t *testing.T) {
	tests := []struct {
		input string
		want  any
	}{
		{input: `~{a:1;b:true;c:null;d:plain;e:"8080";f:"line\nnext"}`, want: map[string]any{
			"a": json.Number("1"), "b": true, "c": nil, "d": "plain", "e": "8080", "f": "line\nnext",
		}},
		{input: `~[alpha,"true",false,null,1.20e+3]`, want: []any{"alpha", "true", false, nil, json.Number("1.20e+3")}},
		{input: `~[name,pid|api,101|worker,202]`, want: []any{
			map[string]any{"name": "api", "pid": json.Number("101")},
			map[string]any{"name": "worker", "pid": json.Number("202")},
		}},
	}
	for _, test := range tests {
		got, err := Decode([]byte(test.input))
		if err != nil {
			t.Fatalf("Decode(%q): %v", test.input, err)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("Decode(%q)\nwant %#v\n got %#v", test.input, test.want, got)
		}
	}
}

func TestDuplicateJSONKeysUseDecoderLastWinsSemantics(t *testing.T) {
	raw := []byte(`{"duplicate":"first","duplicate":"second","padding":"` + strings.Repeat("word ", 140) + `"}`)
	formatted, changed := Format(raw)
	if !changed {
		t.Fatal("expected duplicate-key fixture to format")
	}
	got, err := Decode(formatted)
	if err != nil {
		t.Fatal(err)
	}
	value := got.(map[string]any)
	if value["duplicate"] != "second" {
		t.Fatalf("duplicate key did not preserve encoding/json last-wins behavior: %#v", value)
	}
}
