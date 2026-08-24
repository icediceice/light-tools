package filetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/mcp"
)

func TestDerivedEpochPreservesBatchContinuation(t *testing.T) {
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789abcdef\n", 12000)), 0o600); err != nil {
		t.Fatal(err)
	}

	request := map[string]any{
		"verb": "read",
		"items": []map[string]any{{"path": path, "offset": 0, "limit": 12000}},
	}
	first := invokeRequest(t, handler, request)
	cursorMatch := regexp.MustCompile(`\[CONTINUE ([A-Za-z0-9_-]+)\]`).FindStringSubmatch(first)
	if len(cursorMatch) != 2 {
		t.Fatalf("missing continuation cursor")
	}

	request["cursor"] = cursorMatch[1]
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := handler.Portable()(context.Background(), raw)
	if err != nil {
		t.Fatalf("continuation failed after the first page marked its item as seen: %v", err)
	}
	result, ok := value.(mcp.Result)
	if !ok || len(result.Content) != 1 {
		t.Fatalf("unexpected continuation result %#v", value)
	}
	second := result.Content[0].Text
	if second == "" || second == first || strings.Contains(second, "[dedup]") {
		t.Fatalf("continuation did not return the remaining distinct bytes: %q", second)
	}
}

func TestDerivedEpochKeepsDistinctBatchWindowsDistinct(t *testing.T) {
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := filepath.Join(root, "windows.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := invokeRequest(t, handler, map[string]any{
		"verb": "read",
		"items": []map[string]any{
			{"path": path, "offset": 0, "limit": 1},
			{"path": path, "offset": 1, "limit": 1},
		},
	})
	if !strings.Contains(result, "alpha") || !strings.Contains(result, "beta") {
		t.Fatalf("distinct windows of one file collided in the dedup ledger: %s", result)
	}
	if strings.Contains(result, "[dedup]") {
		t.Fatalf("the first observation of a distinct window was elided: %s", result)
	}
}
