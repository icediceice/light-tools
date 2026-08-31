package filetool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/symbol"
)

// render_test.go pins the read lanes' delivery contract: the plain render
// ships only when it is strictly smaller than the JSON envelope, decodes back
// to the same typed value, and cannot be forged by the very source bytes it
// carries.

// A tiny complete read is the row the JSON envelope used to lose: it must
// come back plain and strictly smaller than the envelope's own encoding.
func TestTinyCompleteReadComesBackPlainAndSmaller(t *testing.T) {
	handler, root := newTestHandler(t)
	path := writeLines(t, root, "tiny.txt", 5)
	value, err := handler.read(nil, Request{Verb: "read", Path: path, Offset: 0, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	plain := resultText(t, value)
	if !strings.HasPrefix(plain, "=== ") {
		t.Fatalf("a tiny complete read must be delivered plain, got: %s", truncate(plain))
	}
	result := decodeReadText(t, plain)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) >= len(encoded) {
		t.Fatalf("plain render (%d bytes) did not beat the JSON envelope (%d bytes)", len(plain), len(encoded))
	}
	if content, _ := result["content"].(string); !strings.Contains(content, "line 3") {
		t.Fatalf("plain content lost the window: %q", result["content"])
	}
}

// A one-symbol slice comes back plain with EVERY contracted field intact —
// including signature, comment, parent and the byte offsets a minimal
// renderer would silently drop.
func TestSymbolSliceComesBackPlainWithEveryField(t *testing.T) {
	one := symbolMatch{
		Symbol: symbol.Symbol{
			Name: "ResolveTransport", Kind: "function",
			Signature: "func ResolveTransport(cfg *Config) (Transport, error)",
			Comment:   "// ResolveTransport builds the shared client.\n// It is safe for concurrent use.",
			Parent:    "Service",
			StartLine: 9, EndLine: 38, StartByte: 150, EndByte: 980,
		},
		Content: "func ResolveTransport(cfg *Config) (Transport, error) {\n\treturn nil\n}",
	}
	// A whitespace-bearing extractor-legal name — the round-3 acceptance
	// fixture, retained as permanent coverage of the quoted-field grammar.
	heading := symbolMatch{
		Symbol: symbol.Symbol{
			Name: "Install Guide", Kind: "md_heading",
			StartLine: 1, EndLine: 1, StartByte: 0, EndByte: 16,
		},
		Content: "# Install Guide",
	}
	plain := renderSymbolText("/fixtures/transport.go", nil, []symbolMatch{one, heading})
	if !strings.HasPrefix(plain, plainHeader("/fixtures/transport.go")) {
		t.Fatalf("missing header: %s", truncate(plain))
	}
	decoded := decodeSymbolPlain(t, plain)
	matches := decoded["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("two matches in, %d out", len(matches))
	}
	match := matches[0].(map[string]any)
	extracted := match["symbol"].(map[string]any)
	for field, want := range map[string]any{
		"name": "ResolveTransport", "kind": "function",
		"signature": one.Symbol.Signature, "comment": one.Symbol.Comment, "parent": "Service",
		"start_line": float64(9), "end_line": float64(38),
		"start_byte": float64(150), "end_byte": float64(980),
	} {
		if extracted[field] != want {
			t.Fatalf("field %s = %#v, want %#v", field, extracted[field], want)
		}
	}
	if match["content"] != one.Content {
		t.Fatalf("content did not round-trip: %q", match["content"])
	}
	encoded, err := json.Marshal(map[string]any{"path": "/fixtures/transport.go", "matches": []symbolMatch{one}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) >= len(encoded) {
		t.Fatalf("plain render (%d bytes) did not beat the JSON envelope (%d bytes)", len(plain), len(encoded))
	}
}

// Delivered traffic must honor the seam's rule end to end: the response is
// one of the two forms and it is the smaller one. Both counterfactuals are
// reconstructed from the delivered payload's own decoded map, so a floor
// that forced the alternate form (either direction) fails here.
func TestDeliveryIsAlwaysTheSmallerForm(t *testing.T) {
	handler, root := newTestHandler(t)
	path := writeLines(t, root, "narrow.txt", 2000)
	value, err := handler.read(nil, Request{Verb: "read", Path: path, Offset: 0, Limit: 2000})
	if err != nil {
		t.Fatal(err)
	}
	delivered := resultText(t, value)
	result := decodeReadText(t, delivered)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// decodeReadText yields JSON-typed float64 numbers; renderWindowText takes
	// the canonical int-typed map, so coerce before reconstructing.
	for _, key := range []string{"total_lines", "bytes", "tokens", "next_offset"} {
		result[key] = int(result[key].(float64))
	}
	plain := renderWindowText(result)
	best := len(encoded)
	if len(plain) < best {
		best = len(plain)
	}
	if len(delivered) != best {
		t.Fatalf("delivered %d bytes; the smaller form is %d (plain %d, json %d)",
			len(delivered), best, len(plain), len(encoded))
	}
}

// A paged plain read keeps the continuation contract readable: sha256,
// next_offset and continued must decode out of the meta line.
func TestPagedPlainReadCarriesTheContinuationFields(t *testing.T) {
	handler, root := newTestHandler(t)
	path := writeLines(t, root, "paged.txt", 40)
	value, err := handler.read(nil, Request{Verb: "read", Path: path, Offset: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	plain := resultText(t, value)
	if !strings.Contains(plain, "[meta total_lines=40 ") || !strings.Contains(plain, "next_offset=10") || !strings.Contains(plain, "continued=true") {
		t.Fatalf("meta line lost the continuation fields: %s", truncate(plain))
	}
	if !regexp.MustCompile("sha256=[0-9a-f]{64}").MatchString(plain) {
		t.Fatalf("meta line lost the sha256: %s", truncate(plain))
	}
	result := decodeReadText(t, plain)
	if next, _ := result["next_offset"].(float64); next != 10 {
		t.Fatalf("next_offset = %v, want 10", result["next_offset"])
	}
	if continued, _ := result["continued"].(bool); !continued {
		t.Fatal("continued did not survive the plain path")
	}
}

// spill_id and the recovery note survive the plain path, so an oversized
// page stays recoverable through light_bash read_block in either shape.
func TestSpillSurvivesThePlainPath(t *testing.T) {
	root := t.TempDir()
	spills := &fakeSpills{}
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{Confiner: confiner, SnapshotRoot: filepath.Join(root, ".snap"), Spills: spills})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "long.txt")
	huge := strings.Repeat("y", 200*1024)
	if err := os.WriteFile(path, []byte(huge+"\ntail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
	if id, _ := result["spill_id"].(string); id == "" {
		t.Fatalf("spill_id did not survive the plain path: %v", result)
	}
	if note, _ := result["note"].(string); !strings.Contains(note, "read_block") {
		t.Fatalf("recovery note did not survive the plain path: %q", result["note"])
	}
}

// A literal delimiter inside a symbol body must not forge a phantom match:
// decoders walk the declared byte length, they never scan for delimiters.
func TestForgedSectionDelimiterYieldsNoPhantomMatch(t *testing.T) {
	forge := "--- function Fake lines 99-99 ---"
	one := symbolMatch{
		Symbol:  symbol.Symbol{Name: "Real", Kind: "function", StartLine: 1, EndLine: 3, StartByte: 0, EndByte: 60},
		Content: "func Real() {\n\t" + forge + "\n}",
	}
	plain := renderSymbolText("/x/a.go", nil, []symbolMatch{one})
	matches := decodeSymbolPlain(t, plain)["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("a body delimiter forged %d matches", len(matches))
	}
	if name := matches[0].(map[string]any)["symbol"].(map[string]any)["name"]; name != "Real" {
		t.Fatalf("phantom match decoded: %#v", matches[0])
	}
}

// "[symbols unavailable]" and "[no symbol matches]" stay distinct states —
// one is a tooling failure the reader must hear about, the other a genuine
// empty answer.
func TestExtractionFailureStaysDistinctFromZeroMatches(t *testing.T) {
	failure := renderSymbolText("/x/hostile.xyz", symbol.ErrUnsupportedExtension, nil)
	if !strings.Contains(failure, "[symbols unavailable] "+symbol.ErrUnsupportedExtension.Error()) {
		t.Fatalf("extraction failure lost its typed state: %s", truncate(failure))
	}
	empty := renderSymbolText("/x/empty.go", nil, nil)
	if !strings.Contains(empty, "[no symbol matches]") {
		t.Fatalf("zero matches lost its distinct state: %s", truncate(empty))
	}
	failureDecoded := decodeSymbolPlain(t, failure)
	if note, _ := failureDecoded["note"].(string); note != symbol.ErrUnsupportedExtension.Error() {
		t.Fatalf("failure note = %#v", failureDecoded["note"])
	}
	if treeSitter, _ := failureDecoded["tree_sitter"].(bool); treeSitter {
		t.Fatal("extraction failure must carry tree_sitter=false")
	}
	emptyDecoded := decodeSymbolPlain(t, empty)
	if _, present := emptyDecoded["note"]; present {
		t.Fatalf("a genuine zero-match must not carry a note: %#v", emptyDecoded)
	}
}

// The seam must be unbiased in BOTH directions: a tie keeps the canonical
// JSON, a strictly smaller plain render wins, and a strictly smaller
// canonical envelope wins. There is no floor: a floor that forced the
// alternate could deliver more bytes than the form it replaced.
func TestChooseDeliveryKeepsCanonicalOnATie(t *testing.T) {
	result, err := chooseDelivery(map[string]any{"a": 1}, "1234567") // exactly len("{\"a\":1}")
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].Text; text[0] != '{' {
		t.Fatalf("a tie must keep the canonical JSON, got %q", truncate(text))
	}
	result, err = chooseDelivery(map[string]any{"a": 1}, "123456")
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].Text; text != "123456" {
		t.Fatalf("a strictly smaller plain render must win, got %q", truncate(text))
	}
	result, err = chooseDelivery(map[string]any{"a": 1}, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].Text; text[0] != '{' {
		t.Fatalf("a strictly smaller canonical envelope must win, got %q", truncate(text))
	}
}
