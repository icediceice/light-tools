//go:build treesitter

package filetool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutlineAndNamedSymbolUseDedicatedTSXGrammar(t *testing.T) {
	handler, root := newTestHandler(t)
	path := filepath.Join(root, "badge.tsx")
	source := "interface Props { label: string }\nexport const Badge = (p: Props) => <span>{p.label}</span>;\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	outlineText := invokeRequest(t, handler, map[string]any{"verb": "read", "path": path})
	var outline struct {
		Symbols []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(outlineText), &outline); err != nil {
		t.Fatalf("outline JSON: %v: %s", err, outlineText)
	}
	names := make(map[string]string)
	for _, symbol := range outline.Symbols {
		names[symbol.Name] = symbol.Kind
	}
	if names["Props"] != "interface" || names["Badge"] != "function" {
		t.Fatalf("TSX outline = %#v", outline.Symbols)
	}

	// The symbol lane delivers whichever of the JSON envelope and the plain
	// render is smaller, so the decode discriminates on the first byte.
	symbolText := invokeRequest(t, handler, map[string]any{"verb": "symbol", "path": path, "name": "Badge"})
	symbolResult := decodeSymbolResult(t, symbolText)
	matches, ok := symbolResult["matches"].([]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("Badge matches = %#v", symbolResult["matches"])
	}
	match, ok := matches[0].(map[string]any)
	if !ok || match["content"] == "" {
		t.Fatalf("Badge match lost its content: %#v", matches[0])
	}
}

func TestOutlineReportsTypedExtractionFailure(t *testing.T) {
	handler, root := newTestHandler(t)
	path := filepath.Join(root, "hostile.ts")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 8001)), 0o600); err != nil {
		t.Fatal(err)
	}

	outlineText := invokeRequest(t, handler, map[string]any{"verb": "read", "path": path})
	var outline struct {
		Note   string `json:"note"`
		Chunks []any  `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(outlineText), &outline); err != nil {
		t.Fatalf("outline JSON: %v: %s", err, outlineText)
	}
	if !strings.Contains(outline.Note, "source rejected as parse-hostile") || len(outline.Chunks) == 0 {
		t.Fatalf("typed outline fallback = %#v", outline)
	}
}
