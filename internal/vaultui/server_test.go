package vaultui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/settings"
	"github.com/icediceice/light-tools/internal/telemetry"
)

type testUI struct {
	server *Server
	http   *httptest.Server
	token  string
}

// testTools mirrors the production tool-name surface for settings assertions.
var testTools = []string{"light_bash", "light_file", "light_ops", "light_scp", "light_ssh"}

func newTestUI(t *testing.T, root string) *testUI {
	t.Helper()
	server, err := New(Options{
		Vault: secret.New(root), Auth: secret.NewPasswordAuth(root),
		Tools:    testTools,
		Settings: settings.New(root, testTools),
		Telemetry: func() (telemetry.Totals, error) {
			return telemetry.Load(filepath.Join(root, "telemetry"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	server.mu.Lock()
	server.host = strings.TrimPrefix(httpServer.URL, "http://")
	server.mu.Unlock()
	t.Cleanup(httpServer.Close)
	return &testUI{server: server, http: httpServer}
}

func (ui *testUI) request(t *testing.T, method, route string, body any, token string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, ui.http.URL+route, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", ui.http.URL)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := ui.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func (ui *testUI) pair(t *testing.T) string {
	t.Helper()
	response := ui.request(t, http.MethodPost, "/api/pair", map[string]any{"code": ui.server.PairingCode()}, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("pair status %d: %s", response.StatusCode, data)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Token == "" {
		t.Fatal("pair response omitted token")
	}
	ui.token = result.Token
	return result.Token
}

func (ui *testUI) setup(t *testing.T, token string) {
	t.Helper()
	response := ui.request(t, http.MethodPost, "/api/setup", map[string]any{"password": "long enough password"}, token)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("setup status %d: %s", response.StatusCode, data)
	}
}

func TestPairingGatesSetupAndIsSingleUse(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	response := ui.request(t, http.MethodPost, "/api/setup", map[string]any{"password": "long enough password"}, "")
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tokenless setup status = %d", response.StatusCode)
	}
	token := ui.pair(t)
	response = ui.request(t, http.MethodPost, "/api/pair", map[string]any{"code": ui.server.PairingCode()}, "")
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused pairing status = %d", response.StatusCode)
	}
	ui.setup(t, token)
	response = ui.request(t, http.MethodGet, "/api/status", nil, token)
	defer response.Body.Close()
	var status map[string]bool
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status["configured"] || !status["authenticated"] {
		t.Fatalf("status after setup = %#v", status)
	}
}

func TestPairingExpires(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	ui.server.mu.Lock()
	ui.server.now = func() time.Time { return ui.server.pairExpires.Add(time.Second) }
	ui.server.mu.Unlock()
	response := ui.request(t, http.MethodPost, "/api/pair", map[string]any{"code": ui.server.PairingCode()}, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired pairing status = %d", response.StatusCode)
	}
}

func TestVaultAPIIsWriteOnlyAndPreservesHostileMetadata(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	token := ui.pair(t)
	ui.setup(t, token)
	hostileGroup := `<img src=x onerror=alert(1)>`
	response := ui.request(t, http.MethodPost, "/api/group/add", map[string]any{"name": hostileGroup}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("group add status = %d", response.StatusCode)
	}
	value := "-----BEGIN OPENSSH PRIVATE KEY-----\nexact multiline key text\n-----END OPENSSH PRIVATE KEY-----\n"
	response = ui.request(t, http.MethodPost, "/api/secret/set", map[string]any{
		"name": "api-token", "value": value, "group": hostileGroup,
	}, token)
	setBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(setBody, []byte(value)) {
		t.Fatalf("set response leaked value or failed: %d %s", response.StatusCode, setBody)
	}
	stored, err := ui.server.vault.Resolve("api-token")
	if err != nil {
		t.Fatal(err)
	}
	if stored != value {
		t.Fatalf("stored key text changed: got %q, want %q", stored, value)
	}
	response = ui.request(t, http.MethodGet, "/api/vault", nil, token)
	overviewBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("vault status = %d", response.StatusCode)
	}
	if bytes.Contains(overviewBody, []byte(value)) {
		t.Fatalf("overview leaked value: %s", overviewBody)
	}
	var overview secret.Overview
	if err := json.Unmarshal(overviewBody, &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Secrets) != 1 || overview.Secrets[0].Name != "api-token" || overview.Secrets[0].Group != hostileGroup ||
		len(overview.Groups) != 1 || overview.Groups[0] != hostileGroup {
		t.Fatalf("overview lost metadata: %#v", overview)
	}
	page, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`id="secret-file"`, `id="secret-import"`, `type="button">Discard file`} {
		if !bytes.Contains(page, []byte(marker)) {
			t.Fatalf("key import markup missing %q", marker)
		}
	}

	script, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		`new TextDecoder("utf-8", { fatal: true })`,
		`file.size > maxImportedFileBytes`,
		`sequence !== importSequence`,
		`pendingImport ? pendingImport.text : valueInput.value`,
		`input.value = ""`,
	} {
		if !bytes.Contains(script, []byte(marker)) {
			t.Fatalf("key import behavior missing %q", marker)
		}
	}
	if bytes.Contains(script, []byte("innerHTML")) {
		t.Fatal("UI renders dynamic metadata through innerHTML")
	}
}

func TestSessionExpiryAndLogout(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	token := ui.pair(t)
	ui.setup(t, token)

	response := ui.request(t, http.MethodPost, "/api/logout", map[string]any{}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d", response.StatusCode)
	}
	response = ui.request(t, http.MethodGet, "/api/vault", nil, token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged-out token status = %d", response.StatusCode)
	}

	ui = newTestUI(t, t.TempDir())
	token = ui.pair(t)
	ui.setup(t, token)
	ui.server.mu.Lock()
	current := ui.server.sessions[token]
	ui.server.now = func() time.Time { return current.LastSeen.Add(sessionIdle + time.Second) }
	ui.server.mu.Unlock()
	response = ui.request(t, http.MethodGet, "/api/vault", nil, token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d", response.StatusCode)
	}
}

func TestSecurityHeadersHostOriginAndBodyLimits(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	response := ui.request(t, http.MethodGet, "/", nil, "")
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("Referrer-Policy") != "no-referrer" ||
		!strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("security headers incomplete: %#v", response.Header)
	}

	request, err := http.NewRequest(http.MethodGet, ui.http.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.invalid"
	response, err = ui.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("host mismatch status = %d", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost, ui.http.URL+"/api/pair", strings.NewReader(`{"code":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://attacker.invalid")
	response, err = ui.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin mismatch status = %d", response.StatusCode)
	}

	token := ui.pair(t)
	ui.setup(t, token)
	oversized := strings.Repeat("x", (1<<20)+1)
	response = ui.request(t, http.MethodPost, "/api/secret/set", map[string]any{"name": "large", "value": oversized}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized secret status = %d", response.StatusCode)
	}
}

func TestNewEndpointsRefuseUnauthenticatedAndUnpairedCallers(t *testing.T) {
	ui := newTestUI(t, t.TempDir())
	paired := ui.pair(t) // paired but NOT password-authenticated

	cases := []struct {
		method string
		route  string
		body   any
	}{
		{http.MethodGet, "/api/settings", nil},
		{http.MethodGet, "/api/telemetry", nil},
		{http.MethodPost, "/api/settings/tools", map[string]any{"tool": "light_ops", "disabled": true}},
	}
	for _, attempt := range cases {
		response := ui.request(t, attempt.method, attempt.route, attempt.body, "")
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("tokenless %s %s status = %d", attempt.method, attempt.route, response.StatusCode)
		}
		response = ui.request(t, attempt.method, attempt.route, attempt.body, paired)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s status = %d", attempt.method, attempt.route, response.StatusCode)
		}
	}

	// The Host pin still wraps the new endpoints.
	request, err := http.NewRequest(http.MethodGet, ui.http.URL+"/api/settings", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "attacker.invalid"
	response, err := ui.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("host-mismatch /api/settings status = %d", response.StatusCode)
	}
}

func TestSettingsEndpointsManageExactlyOneMarker(t *testing.T) {
	root := t.TempDir()
	ui := newTestUI(t, root)
	token := ui.pair(t)
	ui.setup(t, token)

	response := ui.request(t, http.MethodGet, "/api/settings", nil, token)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d: %s", response.StatusCode, body)
	}
	var state struct {
		Tools []struct {
			Name     string `json:"name"`
			Disabled bool   `json:"disabled"`
		} `json:"tools"`
		UIDisabled                []string `json:"ui_disabled"`
		LaunchWithholdingObserved bool     `json:"launch_withholding_observable"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tools) != len(testTools) || state.LaunchWithholdingObserved || len(state.UIDisabled) != 0 {
		t.Fatalf("fresh settings = %s", body)
	}

	// One POST touches exactly its own marker.
	response = ui.request(t, http.MethodPost, "/api/settings/tools", map[string]any{"tool": "light_ops", "disabled": true}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d", response.StatusCode)
	}
	markers, err := os.ReadDir(filepath.Join(root, "disabled-tools"))
	if err != nil || len(markers) != 1 || markers[0].Name() != "light_ops" {
		t.Fatalf("markers after one POST = %v err=%v", markers, err)
	}
	response = ui.request(t, http.MethodGet, "/api/settings", nil, token)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), `"ui_disabled":["light_ops"]`) {
		t.Fatalf("settings after disable = %s", body)
	}

	// Enabling a tool with no marker succeeds and creates nothing: the UI can
	// only manage markers, and launch withholding stays outside its reach.
	response = ui.request(t, http.MethodPost, "/api/settings/tools", map[string]any{"tool": "light_scp", "disabled": false}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("enable-absent status = %d", response.StatusCode)
	}
	markers, err = os.ReadDir(filepath.Join(root, "disabled-tools"))
	if err != nil || len(markers) != 1 {
		t.Fatalf("enable-absent touched markers: %v err=%v", markers, err)
	}

	// Unknown tools and unknown fields are refused.
	response = ui.request(t, http.MethodPost, "/api/settings/tools", map[string]any{"tool": "light_shell", "disabled": true}, token)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown tool status = %d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodPost, ui.http.URL+"/api/settings/tools", strings.NewReader(`{"tool":"light_ops","disabled":true,"extra":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", ui.http.URL)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = ui.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("replacement-set shape accepted: %d", response.StatusCode)
	}
}

func TestTelemetryEndpointReadsFreshStoreAsCleanZero(t *testing.T) {
	root := t.TempDir()
	telemetryRoot := filepath.Join(root, "telemetry")
	if err := os.MkdirAll(telemetryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// A resolved store root always carries SCHEMA and .lock; the read must be
	// a defined zero, not a decode error.
	for _, name := range []string{"SCHEMA", ".lock"} {
		if err := os.WriteFile(filepath.Join(telemetryRoot, name), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ui := newTestUI(t, root)
	token := ui.pair(t)
	ui.setup(t, token)
	response := ui.request(t, http.MethodGet, "/api/telemetry", nil, token)
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("telemetry status = %d: %s", response.StatusCode, body)
	}
	var totals telemetry.Totals
	if err := json.Unmarshal(body, &totals); err != nil {
		t.Fatal(err)
	}
	if totals.Sessions != 0 || totals.TerseTokensSaved != 0 || totals.DedupBytesSaved != 0 || totals.WriteBytesSaved != 0 || len(totals.Warnings) != 0 {
		t.Fatalf("fresh telemetry = %s", body)
	}
}

func TestServeStopsWithContext(t *testing.T) {
	root := t.TempDir()
	server, err := New(Options{Vault: secret.New(root), Auth: secret.NewPasswordAuth(root)})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	context, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(context, listener) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}
