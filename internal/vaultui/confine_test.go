package vaultui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/settings"
	"github.com/icediceice/light-tools/internal/telemetry"
)

// newConfineUI mirrors newTestUI but lets a case inject the ConfigConfined
// probe, which is the only way to exercise the operator-config-wins branch.
func newConfineUI(t *testing.T, configConfined func() (bool, error)) (*testUI, string, string) {
	t.Helper()
	root := t.TempDir()
	server, err := New(Options{
		Vault: secret.New(root), Auth: secret.NewPasswordAuth(root),
		Tools:    testTools,
		Settings: settings.New(root, testTools),
		Telemetry: func() (telemetry.Totals, error) {
			return telemetry.Load(filepath.Join(root, "telemetry"))
		},
		ConfigConfined: configConfined,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	server.mu.Lock()
	server.host = strings.TrimPrefix(httpServer.URL, "http://")
	server.mu.Unlock()
	t.Cleanup(httpServer.Close)
	ui := &testUI{server: server, http: httpServer}
	token := ui.pair(t)
	ui.setup(t, token)
	return ui, root, token
}

func readSettings(t *testing.T, ui *testUI, token string) (confine bool, authoritative bool) {
	t.Helper()
	response := ui.request(t, http.MethodGet, "/api/settings", nil, token)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var state struct {
		Confine                  bool `json:"confine"`
		ConfigRootsAuthoritative bool `json:"config_roots_authoritative"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("settings body %s: %v", body, err)
	}
	return state.Confine, state.ConfigRootsAuthoritative
}

// The toggle round-trips through one marker and nothing else, and the default
// posture it reports is unconfined.
func TestConfineToggleRoundTripsThroughOneMarker(t *testing.T) {
	ui, root, token := newConfineUI(t, nil)

	if confine, _ := readSettings(t, ui, token); confine {
		t.Fatal("a fresh install reported itself confined")
	}

	response := ui.request(t, http.MethodPost, "/api/settings/confine", map[string]any{"confine": true}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("confine status = %d", response.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "confine")); err != nil {
		t.Fatalf("confine marker not written: %v", err)
	}
	if confine, _ := readSettings(t, ui, token); !confine {
		t.Fatal("settings did not report the confinement it just stored")
	}

	response = ui.request(t, http.MethodPost, "/api/settings/confine", map[string]any{"confine": false}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unconfine status = %d", response.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "confine")); !os.IsNotExist(err) {
		t.Fatalf("confine marker survived removal: %v", err)
	}
}

// When config.toml owns the boundary the UI must SAY so, so the client can
// render the switch inert. A toggle that appears to work while a config file
// silently overrides it is the failure this reporting exists to prevent.
func TestConfineReportsWhenOperatorConfigIsAuthoritative(t *testing.T) {
	ui, _, token := newConfineUI(t, func() (bool, error) { return true, nil })
	if _, authoritative := readSettings(t, ui, token); !authoritative {
		t.Fatal("operator config.toml was not reported as authoritative")
	}
}

// An unreadable config must not blank the whole settings view; the rest of the
// page is still true and useful.
func TestConfineSurvivesAnUnreadableConfig(t *testing.T) {
	ui, _, token := newConfineUI(t, func() (bool, error) { return false, os.ErrPermission })
	if _, authoritative := readSettings(t, ui, token); authoritative {
		t.Fatal("an unreadable config was reported as authoritative")
	}
}

// The route is a mutation and must refuse an unauthenticated caller, exactly
// like every other mutating settings route.
func TestConfineRefusesUnauthenticatedCallers(t *testing.T) {
	ui, root, _ := newConfineUI(t, nil)
	response := ui.request(t, http.MethodPost, "/api/settings/confine", map[string]any{"confine": true}, "")
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("an unauthenticated caller changed the confinement setting")
	}
	if _, err := os.Stat(filepath.Join(root, "confine")); !os.IsNotExist(err) {
		t.Fatal("an unauthenticated caller wrote the confine marker")
	}
}
