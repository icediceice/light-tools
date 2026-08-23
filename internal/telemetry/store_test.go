package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// enableForTest forces the environment on; ambient DO_NOT_TRACK on a developer
// machine must not silently disable the tests.
func enableForTest(t *testing.T) {
	t.Helper()
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("LIGHT_NO_TELEMETRY", "")
}

func newTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	enableForTest(t)
	store := New(dir)
	if store == nil {
		t.Fatal("New returned a disabled store")
	}
	t.Cleanup(store.Close)
	return store
}

func TestDisabledEnvironmentYieldsNilNoOpStore(t *testing.T) {
	for name, env := range map[string]func(t *testing.T){
		"DO_NOT_TRACK":       func(t *testing.T) { t.Setenv("DO_NOT_TRACK", "1") },
		"LIGHT_NO_TELEMETRY": func(t *testing.T) { t.Setenv("LIGHT_NO_TELEMETRY", "1") },
	} {
		t.Run(name, func(t *testing.T) {
			enableForTest(t)
			env(t)
			var store *Store
			if store = New(t.TempDir()); store != nil {
				t.Fatal("New returned a store despite the opt-out")
			}
			// A nil store must remain a usable no-op Recorder.
			store.RecordCall("light_file")
			store.RecordTerseTokens(10)
			store.RecordDedupBytes(10)
			store.RecordWriteBytes(10)
			store.Close()
		})
	}
}

func TestFlushAdvancesGenerationsAndRemovesPriors(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	store.RecordCall("light_file")
	store.RecordTerseTokens(500)
	store.flush()

	first := snapshotPath(dir, store.session, 1)
	data, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("first generation missing: %v", err)
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Session != store.session || snap.Generation != 1 || snap.Calls["light_file"] != 1 || snap.TerseTokensSaved != 500 {
		t.Fatalf("first generation = %#v", snap)
	}

	store.RecordCall("light_file")
	store.flush()
	if _, err := os.Stat(snapshotPath(dir, store.session, 2)); err != nil {
		t.Fatalf("second generation missing: %v", err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("prior generation survived: %v", err)
	}
}

func TestFlushWithoutRecordsWritesNothing(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	store.flush()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("idle store wrote %d files", len(entries))
	}
}

func TestCloseFlushesPendingCounters(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	store.RecordCall("light_bash")
	store.RecordDedupBytes(4096)
	store.RecordWriteBytes(2048)
	store.Close()
	store.Close() // second Close must be safe

	totals, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Sessions != 1 || totals.Calls["light_bash"] != 1 ||
		totals.DedupBytesSaved != 4096 || totals.WriteBytesSaved != 2048 || totals.TerseTokensSaved != 0 {
		t.Fatalf("totals after Close = %#v", totals)
	}
}

func TestFreshStoreWithSchemaAndLockReadsAsCleanZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SCHEMA"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	totals, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Sessions != 0 || totals.Calls != nil || totals.TerseTokensSaved != 0 ||
		totals.DedupBytesSaved != 0 || totals.WriteBytesSaved != 0 || len(totals.Warnings) != 0 {
		t.Fatalf("fresh store = %#v, want a clean zero", totals)
	}
}

func writeFakeSnapshot(t *testing.T, dir, session string, generation int64, snap snapshot) {
	t.Helper()
	if snap.Session == "" {
		snap.Session = session
	}
	if snap.Generation == 0 {
		snap.Generation = generation
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath(dir, session, generation), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeSessionID(seed int) string {
	return fmt.Sprintf("%032x", seed)
}

func TestLoadSumsHighestGenerationPerSession(t *testing.T) {
	dir := t.TempDir()
	first, second := fakeSessionID(1), fakeSessionID(2)
	// Session one: generation 1 is superseded by generation 2 — only gen 2 counts.
	writeFakeSnapshot(t, dir, first, 1, snapshot{Calls: map[string]int64{"light_file": 100}, TerseTokensSaved: 1000})
	writeFakeSnapshot(t, dir, first, 2, snapshot{Calls: map[string]int64{"light_file": 3}, TerseTokensSaved: 300, DedupBytesSaved: 50})
	writeFakeSnapshot(t, dir, second, 1, snapshot{Calls: map[string]int64{"light_ops": 7}, WriteBytesSaved: 900})

	totals, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Sessions != 2 || totals.Calls["light_file"] != 3 || totals.Calls["light_ops"] != 7 ||
		totals.TerseTokensSaved != 300 || totals.DedupBytesSaved != 50 || totals.WriteBytesSaved != 900 {
		t.Fatalf("totals = %#v", totals)
	}
	if len(totals.Warnings) != 0 {
		t.Fatalf("healthy snapshots produced warnings: %v", totals.Warnings)
	}
}

func TestLoadReportsMalformedSnapshotsAsWarnings(t *testing.T) {
	dir := t.TempDir()
	good := fakeSessionID(7)
	writeFakeSnapshot(t, dir, good, 1, snapshot{Calls: map[string]int64{"light_file": 1}})

	corrupt := fakeSessionID(8)
	if err := os.WriteFile(snapshotPath(dir, corrupt, 1), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A snapshot whose content disagrees with its filename is a warning too,
	// and its counters contribute nothing.
	mismatch := fakeSessionID(9)
	writeFakeSnapshot(t, dir, mismatch, 1, snapshot{Generation: 5, Calls: map[string]int64{"light_bash": 1}})
	// Names outside the grammar (bad hex, generation zero, wrong shape) are
	// ignored entirely rather than warned about.
	for _, name := range []string{
		"session-v1-ZZZZ-1.json", "session-v1-" + fakeSessionID(1) + "-0.json",
		"session-v1-" + fakeSessionID(2) + "-1.txt", "counters-1.json",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	totals, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if totals.Sessions != 1 || totals.Calls["light_file"] != 1 || totals.Calls["light_bash"] != 0 {
		t.Fatalf("totals = %#v, want only the healthy session to contribute", totals)
	}
	if len(totals.Warnings) != 2 {
		t.Fatalf("warnings = %v, want the corrupt and the mismatched snapshot", totals.Warnings)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPruneEnforcesRetentionAndSessionCap(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	now := time.Now()

	old := fakeSessionID(1)
	writeFakeSnapshot(t, dir, old, 1, snapshot{})
	if err := os.Chtimes(snapshotPath(dir, old, 1), now.Add(-retention-time.Hour), now.Add(-retention-time.Hour)); err != nil {
		t.Fatal(err)
	}
	// One session beyond the cap, so retention alone cannot account for the
	// eviction: sessionCap+1 fresh sessions plus the expired one leave
	// sessionCap+1 sessions in the map after retention fires.
	for index := 0; index < sessionCap+1; index++ {
		session := fakeSessionID(100 + index)
		writeFakeSnapshot(t, dir, session, 1, snapshot{})
		age := time.Duration(sessionCap-index+1) * time.Hour // index 0 is the oldest
		if err := os.Chtimes(snapshotPath(dir, session, 1), now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	store.prune()

	if _, err := os.Stat(snapshotPath(dir, old, 1)); !os.IsNotExist(err) {
		t.Fatalf("expired session survived pruning: %v", err)
	}
	remaining := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filenamePattern.MatchString(entry.Name()) {
			remaining++
		}
	}
	if remaining != sessionCap {
		t.Fatalf("pruning left %d sessions, want the cap of %d", remaining, sessionCap)
	}
	if _, err := os.Stat(snapshotPath(dir, fakeSessionID(100), 1)); !os.IsNotExist(err) {
		t.Fatal("cap pruning did not evict the oldest session first")
	}
	if _, err := os.Stat(snapshotPath(dir, fakeSessionID(100+sessionCap-1), 1)); err != nil {
		t.Fatal("cap pruning evicted the newest foreign session")
	}
}

// Record paths are in-memory only: even when every flush fails (the directory
// was replaced by a file), recording must complete immediately.
func TestRecordNeverBlocksWhenFlushesFail(t *testing.T) {
	dir := t.TempDir()
	store := newTestStore(t, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.RecordCall("light_file")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 100; index++ {
			store.RecordCall("light_file")
			store.RecordDedupBytes(index)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked behind a failing writer")
	}
	store.flush() // must not panic or hang on the unusable directory
}

func TestFilenameGenerationParses(t *testing.T) {
	match := filenamePattern.FindStringSubmatch("session-v1-" + fakeSessionID(3) + "-12.json")
	if match == nil || match[1] != fakeSessionID(3) {
		t.Fatalf("grammar rejected a valid name: %v", match)
	}
	if value, err := strconv.ParseInt("012", 10, 64); err != nil || value != 12 {
		t.Fatalf("generations must parse without octal semantics: %d %v", value, err)
	}
}
