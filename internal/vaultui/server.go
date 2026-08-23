package vaultui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/settings"
	"github.com/icediceice/light-tools/internal/telemetry"
)

//go:embed index.html app.js app.css
var assets embed.FS

const (
	pairLifetime      = 5 * time.Minute
	sessionIdle       = 15 * time.Minute
	sessionAbsolute   = 8 * time.Hour
	normalBodyLimit   = 16 * 1024
	secretBodyLimit   = (1 << 20) + normalBodyLimit
	authorizationType = "Bearer "
)

type session struct {
	Created       time.Time
	LastSeen      time.Time
	Authenticated bool
}

type Server struct {
	vault *secret.Vault
	auth  *secret.PasswordAuth
	// tools is the complete tool-name surface the settings view can toggle;
	// settings holds the UI-owned disabled markers; telemetry reads aggregate
	// snapshots. Both are optional: without them the corresponding view
	// reports itself unavailable rather than the whole UI failing.
	tools     []string
	settings  *settings.Store
	telemetry func() (telemetry.Totals, error)

	mu          sync.Mutex
	host        string
	pairingCode string
	pairExpires time.Time
	paired      bool
	sessions    map[string]session
	now         func() time.Time
}

// Options assembles a vault UI server. Vault and Auth are required.
type Options struct {
	Vault     *secret.Vault
	Auth      *secret.PasswordAuth
	Tools     []string
	Settings  *settings.Store
	Telemetry func() (telemetry.Totals, error)
}

func New(options Options) (*Server, error) {
	if options.Vault == nil || options.Auth == nil {
		return nil, fmt.Errorf("vault and password auth are required")
	}
	code, err := randomPairingCode()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &Server{
		vault: options.Vault, auth: options.Auth, pairingCode: code, pairExpires: now.Add(pairLifetime),
		tools: options.Tools, settings: options.Settings, telemetry: options.Telemetry,
		sessions: make(map[string]session), now: time.Now,
	}, nil
}

func Listen() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func URL(listener net.Listener) string {
	return "http://" + listener.Addr().String()
}

func (s *Server) PairingCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pairingCode
}

func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	s.host = listener.Addr().String()
	s.mu.Unlock()
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = server.Shutdown(shutdownContext)
			cancel()
		case <-done:
		}
	}()
	err := server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveAsset)
	mux.HandleFunc("/api/pair", s.handlePair)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/vault", s.handleVault)
	mux.HandleFunc("/api/secret/set", s.handleSecretSet)
	mux.HandleFunc("/api/secret/remove", s.handleSecretRemove)
	mux.HandleFunc("/api/secret/group", s.handleSecretGroup)
	mux.HandleFunc("/api/group/add", s.handleGroupAdd)
	mux.HandleFunc("/api/group/rename", s.handleGroupRename)
	mux.HandleFunc("/api/group/remove", s.handleGroupRemove)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		s.securityHeaders(writer)
		s.mu.Lock()
		expectedHost := s.host
		s.mu.Unlock()
		if expectedHost == "" || request.Host != expectedHost {
			http.Error(writer, "invalid host", http.StatusBadRequest)
			return
		}
		mux.ServeHTTP(writer, request)
	})
}

func (s *Server) serveAsset(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	switch name {
	case "index.html":
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	case "app.js":
		writer.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case "app.css":
		writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(writer, request)
		return
	}
	data, err := assets.ReadFile(name)
	if err != nil {
		http.NotFound(writer, request)
		return
	}
	_, _ = writer.Write(data)
}

func (s *Server) handlePair(writer http.ResponseWriter, request *http.Request) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, normalBodyLimit) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	s.mu.Lock()
	now := s.now()
	valid := !s.paired && now.Before(s.pairExpires) &&
		subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(body.Code))), []byte(s.pairingCode)) == 1
	if valid {
		s.paired = true
	}
	s.mu.Unlock()
	if !valid {
		http.Error(writer, "invalid or expired pairing code", http.StatusUnauthorized)
		return
	}
	token, err := randomToken()
	if err != nil {
		http.Error(writer, "session creation failed", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions[token] = session{Created: now, LastSeen: now}
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"token": token})
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := s.authorize(writer, request, false)
	if !ok {
		return
	}
	configured, err := s.auth.Configured()
	if err != nil {
		http.Error(writer, "password configuration unavailable", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	current := s.sessions[token]
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"configured": configured, "authenticated": current.Authenticated})
}

func (s *Server) handleSetup(writer http.ResponseWriter, request *http.Request) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, normalBodyLimit) {
		return
	}
	token, ok := s.authorize(writer, request, false)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := s.auth.Setup(body.Password); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, secret.ErrUIAuthConfigured) {
			status = http.StatusConflict
		}
		http.Error(writer, "password setup failed", status)
		return
	}
	s.authenticate(token)
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogin(writer http.ResponseWriter, request *http.Request) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, normalBodyLimit) {
		return
	}
	token, ok := s.authorize(writer, request, false)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	valid, err := s.auth.Verify(body.Password)
	switch {
	case errors.Is(err, secret.ErrUIAuthBusy):
		http.Error(writer, "password verification busy", http.StatusServiceUnavailable)
		return
	case errors.Is(err, secret.ErrUIAuthLimited):
		http.Error(writer, "too many attempts", http.StatusTooManyRequests)
		return
	case err != nil:
		http.Error(writer, "login failed", http.StatusUnauthorized)
		return
	case !valid:
		http.Error(writer, "login failed", http.StatusUnauthorized)
		return
	}
	s.authenticate(token)
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, normalBodyLimit) {
		return
	}
	token, ok := s.authorize(writer, request, false)
	if !ok {
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleVault(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.authorize(writer, request, true); !ok {
		return
	}
	overview, err := s.vault.Overview()
	if err != nil {
		http.Error(writer, "vault unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(writer, http.StatusOK, overview)
}

func (s *Server) handleSecretSet(writer http.ResponseWriter, request *http.Request) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, secretBodyLimit) {
		return
	}
	if _, ok := s.authorize(writer, request, true); !ok {
		return
	}
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Group string `json:"group"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if err := s.vault.SetWithGroup(body.Name, body.Value, body.Group); err != nil {
		http.Error(writer, "secret update failed", http.StatusBadRequest)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSecretRemove(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	s.authenticatedMutation(writer, request, &body, func() error { return s.vault.Remove(body.Name) })
}

func (s *Server) handleSecretGroup(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Group string `json:"group"`
	}
	s.authenticatedMutation(writer, request, &body, func() error { return s.vault.AssignGroup(body.Name, body.Group) })
}

func (s *Server) handleGroupAdd(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	s.authenticatedMutation(writer, request, &body, func() error { return s.vault.AddGroup(body.Name) })
}

func (s *Server) handleGroupRename(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	s.authenticatedMutation(writer, request, &body, func() error { return s.vault.RenameGroup(body.From, body.To) })
}

func (s *Server) handleGroupRemove(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	s.authenticatedMutation(writer, request, &body, func() error { return s.vault.DeleteGroup(body.Name) })
}

func (s *Server) authenticatedMutation(writer http.ResponseWriter, request *http.Request, body any, action func() error) {
	if !s.requireJSONMutation(writer, request, http.MethodPost, normalBodyLimit) {
		return
	}
	if _, ok := s.authorize(writer, request, true); !ok {
		return
	}
	if !decodeJSON(writer, request, body) {
		return
	}
	if err := action(); err != nil {
		http.Error(writer, "vault update failed", http.StatusBadRequest)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) requireJSONMutation(writer http.ResponseWriter, request *http.Request, method string, limit int64) bool {
	if request.Method != method {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	s.mu.Lock()
	expectedOrigin := "http://" + s.host
	s.mu.Unlock()
	if request.Header.Get("Origin") != expectedOrigin {
		http.Error(writer, "invalid origin", http.StatusForbidden)
		return false
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		http.Error(writer, "application/json required", http.StatusUnsupportedMediaType)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	return true
}

func (s *Server) authorize(writer http.ResponseWriter, request *http.Request, requireAuthenticated bool) (string, bool) {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, authorizationType) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	token := strings.TrimPrefix(header, authorizationType)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, current := range s.sessions {
		if now.Sub(current.LastSeen) > sessionIdle || now.Sub(current.Created) > sessionAbsolute {
			delete(s.sessions, key)
		}
	}
	current, ok := s.sessions[token]
	if !ok || requireAuthenticated && !current.Authenticated {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return "", false
	}
	current.LastSeen = now
	s.sessions[token] = current
	return token, true
}

func (s *Server) authenticate(token string) {
	s.mu.Lock()
	current := s.sessions[token]
	current.Authenticated = true
	current.LastSeen = s.now()
	s.sessions[token] = current
	s.mu.Unlock()
}

func (s *Server) securityHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		http.Error(writer, "invalid JSON", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(writer, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomPairingCode() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	value := make([]byte, 8)
	random := make([]byte, len(value))
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", err
	}
	for index := range value {
		value[index] = alphabet[int(random[index])%len(alphabet)]
	}
	return string(value[:4]) + "-" + string(value[4:]), nil
}

func parseURLHost(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Host
}
