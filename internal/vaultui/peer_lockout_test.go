package vaultui

import (
	"io"
	"net/http"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
)

// Peer verify-ship acceptance test (G1).
//
// Once Secrets/ui.json exists, the browser deliberately offers no password
// reset route. Terminal access remains the pairing trust anchor and can clear
// only the verifier, after which the existing paired browser can set a new
// password without exposing or replacing the encrypted vault.
func TestForgottenPasswordRecoversThroughTerminalReset(t *testing.T) {
	root := t.TempDir()

	first := newTestUI(t, root)
	first.setup(t, first.pair(t))

	// A fresh process over the same secrets root: pairing still works, so the
	// lockout is the on-disk verifier rather than stale session state.
	restarted := newTestUI(t, root)
	token := restarted.pair(t)

	response := restarted.request(t, http.MethodPost, "/api/setup",
		map[string]any{"password": "a different password"}, token)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("setup after restart = %d (%s), want 409", response.StatusCode, body)
	}

	response = restarted.request(t, http.MethodPost, "/api/login",
		map[string]any{"password": "a different password"}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login with forgotten password = %d, want 401", response.StatusCode)
	}

	// Recovery is intentionally not exposed over HTTP.
	for _, route := range []string{
		"/api/password/change",
		"/api/password/reset",
		"/api/setup/reset",
		"/api/reset",
	} {
		response := restarted.request(t, http.MethodPost, route,
			map[string]any{"password": "a different password"}, token)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s = %d, want 404/405", route, response.StatusCode)
		}
	}

	response = restarted.request(t, http.MethodGet, "/api/vault", nil, token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("vault read before reset = %d, want 401", response.StatusCode)
	}

	// This is the same operation exposed by the light-tools vault ui-reset CLI.
	if err := secret.NewPasswordAuth(root).Reset(); err != nil {
		t.Fatal(err)
	}
	restarted.setup(t, token)
	response = restarted.request(t, http.MethodGet, "/api/vault", nil, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("vault read after reset and setup = %d, want 200", response.StatusCode)
	}
}
