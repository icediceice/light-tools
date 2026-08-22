package terse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		t.Fatal("accepted a trailing JSON document")
	}
	return value
}

func largeObject(extra string) []byte {
	var fields []string
	for index := 0; index < 32; index++ {
		fields = append(fields, fmt.Sprintf("%q:%q", fmt.Sprintf("field_%02d", index), strings.Repeat("word", 3)))
	}
	if extra != "" {
		fields = append(fields, extra)
	}
	return []byte("{" + strings.Join(fields, ",") + "}")
}

func TestFormatPreservesExactJSONSemantics(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "object scalars", raw: largeObject(`"enabled":true,"missing":null,"empty":"","zero":0`)},
		{name: "number-looking string", raw: largeObject(`"port":"8080","bool_text":"true","null_text":"null"`)},
		{name: "delimiter and multiline strings", raw: largeObject(`"content":"first: value\nsecond; [value] | tail","quoted":"a,b;c:d"`)},
		{name: "large and spelled numbers", raw: largeObject(`"large":9007199254740993,"exponent":1.2300e+09`)},
		{name: "nested collect result", raw: []byte(`{"job_id":"job-1","status":"complete","result":{"exit_code":0,"stdout":"` + strings.Repeat("ok ", 120) + `","stderr":""}}`)},
		{name: "scalar array", raw: []byte(`{"values":["alpha","8080",true,false,null,9007199254740993],"padding":"` + strings.Repeat("word ", 120) + `"}`)},
		{name: "homogeneous rows", raw: []byte(`{"services":[{"name":"api","state":"running","pid":101},{"name":"worker","state":"stopped","pid":202},{"name":"cron","state":"running","pid":303}],"padding":"` + strings.Repeat("word ", 120) + `"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formatted, changed := Format(test.raw)
			if !changed {
				t.Fatalf("expected a useful terse rendering for %s", test.raw)
			}
			if len(formatted) >= len(test.raw) {
				t.Fatalf("formatted output did not shrink: %d >= %d", len(formatted), len(test.raw))
			}
			want := decodeJSON(t, test.raw)
			got, err := Decode(formatted)
			if err != nil {
				t.Fatalf("Decode(%q): %v", formatted, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("semantic drift\nwant: %#v\n got: %#v\nout: %s", want, got, formatted)
			}
		})
	}
}

func TestFormatFallsBackToExactRawBytes(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "below floor", raw: []byte(`{"ok":true}`)},
		{name: "malformed", raw: []byte(`{"broken":`)},
		{name: "trailing document", raw: append(largeObject(""), []byte(` {"second":true}`)...)},
		{name: "unsafe key", raw: largeObject(`"unsafe key":"value"`)},
		{name: "nested empty object", raw: largeObject(`"nested":{}`)},
		{name: "nested empty array", raw: largeObject(`"nested":[]`)},
		{name: "heterogeneous array", raw: largeObject(`"values":[1,{"a":2}]`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formatted, changed := Format(test.raw)
			if changed {
				t.Fatalf("unsupported input changed to %q", formatted)
			}
			if formatted != nil {
				t.Fatalf("fallback returned bytes instead of nil: %q", formatted)
			}
		})
	}
}

func TestFormatIsDeterministicAndStrictlyWins(t *testing.T) {
	raw := largeObject(`"rows":[{"z":"last","a":"first"},{"a":"second","z":"tail"}]`)
	first, changed := Format(raw)
	if !changed {
		t.Fatal("expected formatter to win")
	}
	for iteration := 0; iteration < 100; iteration++ {
		got, ok := Format(raw)
		if !ok || !bytes.Equal(got, first) {
			t.Fatalf("formatter drifted at iteration %d: %q != %q", iteration, got, first)
		}
	}
	if EstimateTokens(first) >= EstimateTokens(raw) {
		t.Fatalf("token estimate did not improve: %d >= %d", EstimateTokens(first), EstimateTokens(raw))
	}
	if len(first) >= len(raw) {
		t.Fatalf("byte size did not improve: %d >= %d", len(first), len(raw))
	}
}

func TestFormatUsesHundredTokenFloor(t *testing.T) {
	raw := []byte(`{"one":"alpha","two":"beta","three":"gamma"}`)
	if EstimateTokens(raw) >= minimumInputTokens {
		t.Fatalf("fixture unexpectedly crosses floor: %d", EstimateTokens(raw))
	}
	if got, changed := Format(raw); changed || got != nil {
		t.Fatalf("small payload changed: %q, %v", got, changed)
	}
}
