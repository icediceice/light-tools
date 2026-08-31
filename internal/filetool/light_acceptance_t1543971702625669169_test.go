//go:build light_acceptance_pending_t1543971702625669169

package filetool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/security"
)

// A dedup hit must credit the delivery it actually suppresses, including
// spill_id and the recovery note minted for an oversized-line response.
func TestVerifyOversizedLineDedupCreditUsesDeliveredSpillForm(t *testing.T) {
	root := t.TempDir()
	spills := &fakeSpills{}
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureRecorder{}
	handler, err := New(Options{
		Confiner: confiner, SnapshotRoot: filepath.Join(root, ".snap"),
		Spills: spills, Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "long.txt")
	huge := strings.Repeat("x", 200*1024)
	if err := os.WriteFile(path, []byte(huge+"\ntail\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := handler.readWindow(path, 0, 5000, "verify-spill-credit", false, ""); err != nil {
		t.Fatal(err)
	}
	stub, err := handler.readWindow(path, 0, 5000, "verify-spill-credit", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.dedup) != 1 {
		t.Fatalf("dedup observations = %v, want exactly one", recorder.dedup)
	}
	forced, err := handler.readWindow(path, 0, 5000, "verify-spill-credit", true, "")
	if err != nil {
		t.Fatal(err)
	}

	got := recorder.dedup[0]
	want := len(resultText(t, forced)) - len(resultText(t, stub))
	if got != want {
		t.Fatalf("oversized dedup credit %d, want delivered spill form %d (forced %d - stub %d)",
			got, want, len(resultText(t, forced)), len(resultText(t, stub)))
	}
}

// A plain result must preserve a legal path containing a newline instead of
// letting that byte terminate the header and become apparent file content.
func TestVerifyPlainReadRoundTripsNewlinePath(t *testing.T) {
	handler, root := newTestHandler(t)
	path := filepath.Join(root, "line\nbreak.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := handler.read(nil, Request{Verb: "read", Path: path, Offset: 0, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	delivered := resultText(t, value)
	if !strings.HasPrefix(delivered, "=== ") {
		t.Fatalf("fixture did not exercise the plain renderer: %q", truncate(delivered))
	}
	decoded := decodeReadText(t, delivered)
	if got := decoded["path"]; got != path {
		t.Fatalf("plain path = %q, want %q", got, path)
	}
}
