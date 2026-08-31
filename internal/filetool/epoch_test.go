package filetool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/security"
)

// newEpochHandler mirrors newTestHandler but seeds the server-derived epoch the
// way main.go does. The ordinary test handler leaves DefaultEpoch empty, so it
// cannot observe this behaviour at all — which is exactly why the rest of the
// suite stayed green when the default was introduced.
func newEpochHandler(t *testing.T, defaultEpoch string) (*Handler, string) {
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
	handler, err := New(Options{Confiner: confiner, SnapshotRoot: snapshotRoot, DefaultEpoch: defaultEpoch})
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

func seedFile(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "subject.txt")
	body := strings.Repeat("the quick brown fox\n", 40)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The point of the whole change: a client that never heard of context_epoch
// still gets dedup. Before this, ShouldElide returned false on every call and
// the saving was silently zero.
func TestRepeatReadDedupsWithoutAClientSuppliedEpoch(t *testing.T) {
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := seedFile(t, root)
	request := map[string]any{"verb": "read", "path": path, "offset": 0, "limit": 20}

	first := invokeRequest(t, handler, request)
	if strings.Contains(first, "[dedup]") {
		t.Fatalf("the FIRST read was elided; nothing had been served yet: %s", first)
	}
	if !strings.Contains(first, "quick brown fox") {
		t.Fatalf("first read did not return the file: %s", first)
	}

	second := invokeRequest(t, handler, request)
	if !strings.Contains(second, "[dedup]") {
		t.Fatalf("an identical repeat read was not deduped: %s", second)
	}
	// The stub must name its own escape: a reader who no longer holds the
	// bytes has to be told that force:true brings them back.
	if !strings.Contains(second, "force:true") {
		t.Fatalf("the dedup stub does not name the force:true escape: %s", second)
	}
}

// force:true is the documented escape hatch for the case this default creates —
// a compaction leaves the model without bytes the server believes it holds.
func TestForceStillReturnsBytesUnderTheDerivedEpoch(t *testing.T) {
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := seedFile(t, root)
	request := map[string]any{"verb": "read", "path": path, "offset": 0, "limit": 20}

	invokeRequest(t, handler, request)
	forced := invokeRequest(t, handler, map[string]any{
		"verb": "read", "path": path, "offset": 0, "limit": 20, "force": true,
	})
	if strings.Contains(forced, "[dedup]") {
		t.Fatalf("force:true was elided, so there is no way back to the bytes: %s", forced)
	}
	if !strings.Contains(forced, "quick brown fox") {
		t.Fatalf("force:true did not re-serve the content: %s", forced)
	}
}

// The operator kill switch must beat BOTH the derived default and an explicit
// client epoch: off means off, not "off unless the caller asks".
func TestKillSwitchDisablesDedupEvenWithAnExplicitEpoch(t *testing.T) {
	t.Setenv(noReadDedup, "1")
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := seedFile(t, root)

	for _, request := range []map[string]any{
		{"verb": "read", "path": path, "offset": 0, "limit": 20},
		{"verb": "read", "path": path, "offset": 0, "limit": 20, "context_epoch": "client-chosen"},
	} {
		invokeRequest(t, handler, request)
		repeat := invokeRequest(t, handler, request)
		if strings.Contains(repeat, "[dedup]") {
			t.Fatalf("%s=1 did not disable dedup for %#v: %s", noReadDedup, request, repeat)
		}
	}
}

// An explicit epoch still wins over the derived one, and still scopes the
// ledger: a different epoch is a different context and must re-serve.
func TestExplicitEpochOverridesTheDerivedDefaultAndScopesTheLedger(t *testing.T) {
	handler, root := newEpochHandler(t, "server-derived-epoch")
	path := seedFile(t, root)
	clientA := map[string]any{"verb": "read", "path": path, "offset": 0, "limit": 20, "context_epoch": "client-a"}
	clientB := map[string]any{"verb": "read", "path": path, "offset": 0, "limit": 20, "context_epoch": "client-b"}

	invokeRequest(t, handler, clientA)
	if repeat := invokeRequest(t, handler, clientA); !strings.Contains(repeat, "[dedup]") {
		t.Fatalf("a repeat read under the same explicit epoch was not deduped: %s", repeat)
	}
	if other := invokeRequest(t, handler, clientB); strings.Contains(other, "[dedup]") {
		t.Fatalf("a different explicit epoch reused another epoch's ledger entry: %s", other)
	}
}
