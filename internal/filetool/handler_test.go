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
	"github.com/icediceice/light-tools/internal/snapshot"
)

func newTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	snapshotRoot := filepath.Join(root, ".snapshots")
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	confiner, err := security.NewConfiner([]string{root}, []string{snapshotRoot})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{Confiner: confiner, SnapshotRoot: snapshotRoot})
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

// The revert handle light_bash prints is only worth printing if light_file
// actually accepts it. This walks the whole hop: raw JSON in through Portable,
// where an unrecognised key would be normalized away before any handler saw
// it, then capture_id dispatch, then the bytes back on disk.
func TestCaptureIDRestoresThroughThePortableHandler(t *testing.T) {
	handler, root := newTestHandler(t)
	target := filepath.Join(root, "doomed.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := handler.vault.CaptureSurface(snapshot.NewCaptureID(), "rm *.txt", []string{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := handler.vault.SealCapture(capture.ID); err != nil {
		t.Fatal(err)
	}

	body := invokeRequest(t, handler, map[string]any{"verb": "vault_restore", "capture_id": capture.ID})
	if !strings.Contains(body, capture.ID) {
		t.Fatalf("restore did not echo the capture: %s", body)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("capture_id restore did not bring the file back: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("restored wrong bytes: %q", data)
	}

	listed := invokeRequest(t, handler, map[string]any{"verb": "vault_list", "capture_id": capture.ID})
	if !strings.Contains(listed, "doomed.txt") {
		t.Fatalf("vault_list by capture_id did not name the surface: %s", listed)
	}
}

// A path a later writer touched must survive a non-force revert: the guard
// exists so a revert cannot silently discard work the caller never saw.
func TestNonForceRestoreSkipsAPathChangedSinceTheCommand(t *testing.T) {
	handler, root := newTestHandler(t)
	target := filepath.Join(root, "edited.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := handler.vault.CaptureSurface(snapshot.NewCaptureID(), "sed -i s/a/b/ *.txt", []string{target})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("by the command"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handler.vault.SealCapture(capture.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("by somebody else, afterwards"), 0o600); err != nil {
		t.Fatal(err)
	}

	body := invokeRequest(t, handler, map[string]any{"verb": "vault_restore", "capture_id": capture.ID})
	if !strings.Contains(body, "skipped") {
		t.Fatalf("a later write was clobbered instead of skipped: %s", body)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "by somebody else, afterwards" {
		t.Fatalf("non-force revert overwrote a later writer: %q", data)
	}

	forced := invokeRequest(t, handler, map[string]any{"verb": "vault_restore", "capture_id": capture.ID, "force": true})
	if strings.Contains(forced, "\"skipped\"") {
		t.Fatalf("force still skipped: %s", forced)
	}
	data, _ = os.ReadFile(target)
	if string(data) != "original" {
		t.Fatalf("forced revert did not restore the preimage: %q", data)
	}
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
	resolvedTarget, err := handler.confiner.Resolve(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "=== "+resolvedTarget+" ===") || !strings.Contains(read, "ONE") {
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
