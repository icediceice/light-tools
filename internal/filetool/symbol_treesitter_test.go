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

	symbolText := invokeRequest(t, handler, map[string]any{"verb": "symbol", "path": path, "name": "Badge"})
	var result struct {
		Matches []struct {
			Content string `json:"content"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(symbolText), &result); err != nil {
		t.Fatalf("symbol JSON: %v: %s", err, symbolText)
	}
	if len(result.Matches) != 1 || result.Matches[0].Content == "" {
		t.Fatalf("Badge matches = %#v", result.Matches)
	}
}
