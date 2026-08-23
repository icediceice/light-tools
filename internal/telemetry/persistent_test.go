package telemetry

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A transient read error means "a writer is replacing this file", and the fix
// for that is one rescan. An error that survives the rescan means something
// else: a Windows lock nobody released, a snapshot that is genuinely
// unreadable. Classifying THAT as supersession too drops the session's counts
// and reports the store healthy — the exact silent-wrong-answer the plan said
// must keep warning.
func TestSnapshotUnreadableOnBothPassesStillWarns(t *testing.T) {
	dir := t.TempDir()
	name := "session-v1-" + strings.Repeat("a", 32) + "-1.json"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Stands in for a lock that outlives the retry: every read fails the same
	// way, on both passes. fs.ErrNotExist is transient on every platform.
	original := readSnapshotFile
	t.Cleanup(func() { readSnapshotFile = original })
	reads := 0
	readSnapshotFile = func(string) ([]byte, error) {
		reads++
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	totals, err := Load(dir)
	if err != nil {
		t.Fatalf("a locked snapshot must not fail the load: %v", err)
	}
	if reads != 2 {
		t.Fatalf("read %d times, want one pass plus exactly one retry", reads)
	}
	var warned bool
	for _, warning := range totals.Warnings {
		if strings.Contains(warning, "unreadable") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("a snapshot unreadable on the final pass was reported as a healthy store: %#v", totals)
	}
}

// The single-race case must not regress into a warning: one transient failure
// followed by a clean read is an ordinary supersession and stays silent.
func TestSnapshotReadableOnRetryStaysSilent(t *testing.T) {
	dir := t.TempDir()
	session := strings.Repeat("b", 32)
	name := "session-v1-" + session + "-1.json"
	body := []byte(`{"session":"` + session + `","generation":1,"calls":{"light_file":3}}`)
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}

	original := readSnapshotFile
	t.Cleanup(func() { readSnapshotFile = original })
	first := true
	readSnapshotFile = func(path string) ([]byte, error) {
		if first {
			first = false
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return original(path)
	}

	totals, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range totals.Warnings {
		if strings.Contains(warning, "unreadable") {
			t.Fatalf("a supersession the retry resolved surfaced as a warning: %s", warning)
		}
	}
	if totals.Calls["light_file"] != 3 {
		t.Fatalf("calls = %d, want the 3 recovered by the retry", totals.Calls["light_file"])
	}
}
