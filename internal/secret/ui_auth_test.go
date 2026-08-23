package secret

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPasswordAuthSetupAndVerify(t *testing.T) {
	root := t.TempDir()
	auth := NewPasswordAuth(root)
	configured, err := auth.Configured()
	if err != nil || configured {
		t.Fatalf("Configured before setup = %t, %v", configured, err)
	}
	if err := auth.Setup("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	configured, err = auth.Configured()
	if err != nil || !configured {
		t.Fatalf("Configured after setup = %t, %v", configured, err)
	}
	ok, err := auth.Verify("correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("correct password = %t, %v", ok, err)
	}
	auth.sleep = func(time.Duration) {}
	ok, err = auth.Verify("wrong password")
	if err != nil || ok {
		t.Fatalf("wrong password = %t, %v", ok, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "ui.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "correct horse") {
		t.Fatal("UI password was persisted in plaintext")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(root, "ui.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("ui.json mode = %o", info.Mode().Perm())
		}
	}
}

func TestPasswordAuthSetupIsSingleWinner(t *testing.T) {
	root := t.TempDir()
	authA := NewPasswordAuth(root)
	authB := NewPasswordAuth(root)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for _, auth := range []*PasswordAuth{authA, authB} {
		group.Add(1)
		go func(auth *PasswordAuth) {
			defer group.Done()
			results <- auth.Setup("long enough password")
		}(auth)
	}
	group.Wait()
	close(results)
	var success, configured int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrUIAuthConfigured):
			configured++
		default:
			t.Fatalf("unexpected setup result: %v", err)
		}
	}
	if success != 1 || configured != 1 {
		t.Fatalf("setup results success=%d configured=%d", success, configured)
	}
}

func TestPasswordAuthRejectsConcurrentKDFWithoutQueue(t *testing.T) {
	auth := NewPasswordAuth(t.TempDir())
	if err := auth.Setup("long enough password"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	auth.sleep = func(time.Duration) {}
	auth.derive = func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte {
		close(entered)
		<-release
		return make([]byte, keyLength)
	}
	done := make(chan error, 1)
	go func() {
		_, err := auth.Verify("long enough password")
		done <- err
	}()
	<-entered
	if _, err := auth.Verify("long enough password"); !errors.Is(err, ErrUIAuthBusy) {
		t.Fatalf("concurrent verify = %v, want busy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPasswordAuthRateLimitsFailures(t *testing.T) {
	auth := NewPasswordAuth(t.TempDir())
	if err := auth.Setup("long enough password"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	auth.now = func() time.Time { return now }
	auth.sleep = func(time.Duration) {}
	auth.derive = func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte {
		return make([]byte, keyLength)
	}
	for index := 0; index < uiFailureLimit; index++ {
		ok, err := auth.Verify("definitely wrong")
		if err != nil || ok {
			t.Fatalf("failure %d = %t, %v", index, ok, err)
		}
	}
	if _, err := auth.Verify("definitely wrong"); !errors.Is(err, ErrUIAuthLimited) {
		t.Fatalf("post-limit verify = %v, want rate limit", err)
	}
	now = now.Add(uiFailureCooldown)
	if _, err := auth.Verify("definitely wrong"); err != nil {
		t.Fatalf("verify remained blocked after cooldown: %v", err)
	}
}

func TestPasswordAuthValidatesInputAndConfig(t *testing.T) {
	auth := NewPasswordAuth(t.TempDir())
	if err := auth.Setup("short"); err == nil {
		t.Fatal("short setup password accepted")
	}
	if _, err := auth.Verify("short"); err == nil {
		t.Fatal("short verify password accepted")
	}
	if _, err := auth.Verify("long enough password"); !errors.Is(err, ErrUIAuthNotSetup) {
		t.Fatalf("verify before setup = %v", err)
	}
}

func TestPasswordAuthResetPreservesEncryptedVault(t *testing.T) {
	root := t.TempDir()
	auth := NewPasswordAuth(root)
	if err := auth.Setup("original password"); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"master.key": "unchanged master key",
		"vault.enc":  "unchanged ciphertext",
	}
	for name, value := range fixtures {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := auth.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := auth.Reset(); err != nil {
		t.Fatalf("idempotent reset: %v", err)
	}
	configured, err := auth.Configured()
	if err != nil || configured {
		t.Fatalf("Configured after reset = %t, %v", configured, err)
	}
	for name, value := range fixtures {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s after reset: %v", name, err)
		}
		if string(data) != value {
			t.Fatalf("%s changed during reset: %q", name, data)
		}
	}

	if err := auth.Setup("replacement password"); err != nil {
		t.Fatalf("setup after reset: %v", err)
	}
	auth.sleep = func(time.Duration) {}
	if ok, err := auth.Verify("original password"); err != nil || ok {
		t.Fatalf("original password after reset = %t, %v", ok, err)
	}
	if ok, err := auth.Verify("replacement password"); err != nil || !ok {
		t.Fatalf("replacement password after reset = %t, %v", ok, err)
	}
}
