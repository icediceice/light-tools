package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A caller-supplied path outside every configured root must be refused. Before
// this was enforced, log_window path:/etc/hostname returned the file contents.
func TestCallerSuppliedPathOutsideRootsIsRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.log")
	if err := os.WriteFile(outside, []byte("classified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := New([]string{root}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, verb := range []string{"log_window", "log_search", "probe_file"} {
		request := map[string]any{"verb": verb, "path": outside}
		if verb == "log_search" {
			request["pattern"] = "classified"
		}
		result, callErr := callOpsErr(t, handler, request)
		if callErr == nil {
			t.Fatalf("%s: expected refusal for a path outside the roots, got %v", verb, result)
		}
		if !strings.Contains(callErr.Error(), "outside the configured log roots") {
			t.Fatalf("%s: refusal should name the boundary, got %v", verb, callErr)
		}
		if strings.Contains(callErr.Error(), "classified") {
			t.Fatalf("%s: refusal leaked file content", verb)
		}
	}

	// The same path reached through the file: service prefix is also caller
	// supplied and must be refused identically.
	if _, callErr := callOpsErr(t, handler, map[string]any{
		"verb": "log_window", "service": "file:" + outside,
	}); callErr == nil {
		t.Fatal("file: service prefix bypassed confinement")
	}
}

// A path under a configured log root is allowed even though it is nowhere near
// allowed_roots — that separation is the whole point of log_roots.
func TestPathUnderLogRootIsAllowed(t *testing.T) {
	allowed := t.TempDir()
	logRoot := t.TempDir()
	logFile := filepath.Join(logRoot, "app.log")
	if err := os.WriteFile(logFile, []byte("2026-08-16T10:00:00Z INFO up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := New([]string{allowed}, []string{logRoot}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result := callOps(t, handler, map[string]any{"verb": "log_window", "path": logFile})
	if content, _ := result["content"].(string); !strings.Contains(content, "INFO up") {
		t.Fatalf("log root path should be readable, got %v", result)
	}
}

// An absent root must not disable the roots that DO exist. NewConfiner
// canonicalizes every root up front and errors on the first
// missing one, so shipping ~/.local/log as a default would otherwise have made
// every light_ops call fail on any machine lacking that directory.
func TestAbsentRootDoesNotPoisonTheUnion(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "app.log")
	if err := os.WriteFile(logFile, []byte("alive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	handler, err := New([]string{root}, []string{missing}, nil)
	if err != nil {
		t.Fatalf("New should drop the absent root, not fail: %v", err)
	}
	result := callOps(t, handler, map[string]any{"verb": "log_window", "path": logFile})
	if content, _ := result["content"].(string); !strings.Contains(content, "alive") {
		t.Fatalf("existing root unusable after an absent root was configured: %v", result)
	}
}

// With no readable root at all, light_ops must refuse to start rather than
// come up with an empty boundary that would refuse or admit everything.
func TestNewFailsWithNoReadableRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	if _, err := New([]string{missing}, []string{missing}, nil); err == nil {
		t.Fatal("ops.New must fail when no configured root exists")
	}
}

// Registry-discovered service logs are deliberately NOT confined: journalctl,
// docker and pm2 write wherever they write, and reading them is what light_ops
// is for. grepPool swallows fetch errors with a bare continue, so an accidental
// confinement of this branch would look like "no matches" rather than an error
// — which is exactly why this regression test exists.
func TestRegistryDiscoveredLogOutsideRootsStaysReadable(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(outside, []byte("2026-08-16T11:00:00Z ERROR boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, err := New([]string{root}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler.registry.services = []Service{{ID: "pm2:api", Source: "pm2", Name: "api", OutLog: outside}}
	handler.registry.updated = time.Now()

	direct := callOps(t, handler, map[string]any{"verb": "log_window", "service": "pm2:api"})
	if content, _ := direct["content"].(string); !strings.Contains(content, "ERROR boom") {
		t.Fatalf("registry service log must stay readable outside the roots, got %v", direct)
	}

	pooled := callOps(t, handler, map[string]any{"verb": "log_grep", "pattern": "ERROR"})
	// Re-marshal: callOps hands back the handler's own map, so the rows are a
	// concrete []row rather than []any.
	encoded, err := json.Marshal(pooled)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "pm2:api") {
		t.Fatalf("log_grep returned no hits: the registry branch was confined and the error was swallowed by grepPool, got %s", encoded)
	}
}
