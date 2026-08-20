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
	"github.com/icediceice/light-tools/internal/security"
)

func newTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	handler, err := New(Options{Roots: []string{root}, SnapshotRoot: filepath.Join(root, ".snapshots")})
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

func invokeRequest(t *testing.T, handler *Handler, request any) string {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := handler.Portable()(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result, ok := value.(mcp.Result); ok {
		if len(result.Content) != 1 {
			t.Fatalf("unexpected result %#v", value)
		}
		return result.Content[0].Text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestJSONMutationFieldsReachSharedIR(t *testing.T) {
	handler, root := newTestHandler(t)
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nalpha\nomega\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	preview := invokeRequest(t, handler, map[string]any{
		"verb": "sed", "path": path, "find": "alpha", "replace": "beta",
		"count": 2, "dry_run": true,
	})
	if !strings.Contains(preview, `"replacements":2`) || !strings.Contains(preview, `"dry_run":true`) {
		t.Fatalf("sed controls were not preserved: %s", preview)
	}
	if data, _ := os.ReadFile(path); string(data) != "alpha\nalpha\nomega\n" {
		t.Fatalf("dry run wrote the file: %q", data)
	}

	invokeRequest(t, handler, map[string]any{
		"verb": "edit", "path": path, "start_line": 2, "end_line": 2,
		"start_guard": "alpha", "end_guard": "alpha", "new_string": "BETA",
	})
	if data, _ := os.ReadFile(path); string(data) != "alpha\nBETA\nomega\n" {
		t.Fatalf("edit selectors were not preserved: %q", data)
	}
}

func TestAliasesAndSamePathPayloadBatch(t *testing.T) {
	handler, root := newTestHandler(t)
	source := filepath.Join(root, "source.txt")
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(source, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invokeRequest(t, handler, map[string]any{
		"verb": "rename", "from": source, "to": target, "overwrite": true,
	})
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("rename alias left source: %v", err)
	}

	payload := strings.Join([]string{
		"@file " + target,
		"@verb edit",
		"@start_line 1",
		"@new_string",
		"ONE",
		"<<LF-END>>",
		"@file " + target,
		"@verb edit",
		"@start_line 3",
		"@new_string",
		"THREE",
		"<<LF-END>>",
	}, "\n")
	invokeRequest(t, handler, map[string]any{"payload": payload})
	if data, _ := os.ReadFile(target); string(data) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("same-path edits did not coalesce: %q", data)
	}

	read := invokeRequest(t, handler, map[string]any{
		"verb": "read", "reads": []map[string]any{{"path": target, "offset": 0, "limit": 2}},
	})
	if !strings.Contains(read, "=== "+target+" ===") || !strings.Contains(read, "ONE") {
		t.Fatalf("reads alias did not materialize: %s", read)
	}
}

func TestUnifiedDiffAndPatchPreview(t *testing.T) {
	handler, root := newTestHandler(t)
	left := filepath.Join(root, "left.txt")
	right := filepath.Join(root, "right.txt")
	os.WriteFile(left, []byte("one\ntwo\nthree\n"), 0o600)
	os.WriteFile(right, []byte("one\nTWO\nthree\n"), 0o600)

	diff := invokeRequest(t, handler, map[string]any{
		"verb": "diff", "path": left, "target": right, "diff_context": 1,
	})
	for _, want := range []string{"@@ -1,3 +1,3 @@", "-two", "+TWO"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff missing %q: %s", want, diff)
		}
	}

	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	preview := invokeRequest(t, handler, map[string]any{
		"verb": "diff", "path": left, "patch": patch,
	})
	if !strings.Contains(preview, `"applied_hunks":1`) || !strings.Contains(preview, "+TWO") {
		t.Fatalf("patch preview failed: %s", preview)
	}
	if data, _ := os.ReadFile(left); string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("patch preview mutated input: %q", data)
	}
}

func TestReadContinuationCursor(t *testing.T) {
	handler, root := newTestHandler(t)
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("0123456789abcdef\n", 12000)), 0o600); err != nil {
		t.Fatal(err)
	}
	first := invokeRequest(t, handler, map[string]any{
		"verb": "read", "items": []map[string]any{{"path": path, "offset": 0, "limit": 12000}},
	})
	cursorMatch := regexp.MustCompile(`\[CONTINUE ([A-Za-z0-9_-]+)\]`).FindStringSubmatch(first)
	if len(cursorMatch) != 2 {
		t.Fatalf("missing continuation cursor")
	}
	second := invokeRequest(t, handler, map[string]any{
		"verb": "read", "items": []map[string]any{{"path": path, "offset": 0, "limit": 12000}},
		"cursor": cursorMatch[1],
	})
	if second == "" || second == first {
		t.Fatalf("cursor did not resume distinct content")
	}
}
