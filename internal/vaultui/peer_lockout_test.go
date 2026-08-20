package vaultui

import (
	"io"
	"net/http"
	"testing"
)

// Peer verify-ship acceptance test (G1).
//
// Once Secrets/ui.json exists, the shipped surface offers NO route back for a
// user who forgets the UI password: /api/setup answers 409 forever, /api/login
// answers 401 forever, and no change/reset route is served. Restarting
// `light-tools vault ui` does not help, because the verifier is on disk, not in
// process memory. This test pins that dead end so the behaviour is a decision
// rather than an accident.
func TestForgottenPasswordHasNoRecoveryRoute(t *testing.T) {
	root := t.TempDir()

	first := newTestUI(t, root)
	first.setup(t, first.pair(t))

	// A fresh process over the same secrets root: pairing still works, so the
	// dead end is not a session problem — it is the on-disk verifier.
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

	// No recovery affordance is served under any plausible name.
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
			t.Fatalf("%s = %d, want 404/405 (a recovery route now exists; update this test)", route, response.StatusCode)
		}
	}

	// The vault itself stays unreachable to that session.
	response = restarted.request(t, http.MethodGet, "/api/vault", nil, token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("vault read after lockout = %d, want 401", response.StatusCode)
	}
}
