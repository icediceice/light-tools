package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	fileop "github.com/icediceice/light-tools/internal/file"
	"golang.org/x/crypto/argon2"
)

const (
	uiAuthVersion      = 1
	uiArgonMemoryKiB   = 19 * 1024
	uiArgonIterations  = 2
	uiArgonParallelism = 1
	uiArgonKeyBytes    = 32
	uiFailureFloor     = time.Second
	uiFailureLimit     = 5
	uiFailureCooldown  = 30 * time.Second
)

var (
	ErrUIAuthBusy       = errors.New("password verification is busy")
	ErrUIAuthLimited    = errors.New("too many password failures; try again later")
	ErrUIAuthNotSetup   = errors.New("vault UI password is not configured")
	ErrUIAuthConfigured = errors.New("vault UI password is already configured")
)

type uiVerifier struct {
	Version     int    `json:"version"`
	Algorithm   string `json:"algorithm"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
	Salt        string `json:"salt"`
	Verifier    string `json:"verifier"`
}

type PasswordAuth struct {
	root string
	gate chan struct{}

	mu           sync.Mutex
	failures     int
	blockedUntil time.Time
	now          func() time.Time
	sleep        func(time.Duration)
	derive       func(password, salt []byte, iterations, memory uint32, parallelism uint8, keyLength uint32) []byte
}

func NewPasswordAuth(secretsRoot string) *PasswordAuth {
	return &PasswordAuth{
		root:   filepath.Clean(secretsRoot),
		gate:   make(chan struct{}, 1),
		now:    time.Now,
		sleep:  time.Sleep,
		derive: argon2.IDKey,
	}
}

func (a *PasswordAuth) Configured() (bool, error) {
	_, err := a.load()
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func (a *PasswordAuth) Setup(password string) error {
	if err := validateUIPassword(password); err != nil {
		return err
	}
	if err := os.MkdirAll(a.root, 0o700); err != nil {
		return err
	}
	release, err := acquireFileLock(filepath.Join(a.root, ".lock"))
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Stat(a.path()); err == nil {
		return ErrUIAuthConfigured
	} else if !os.IsNotExist(err) {
		return err
	}

	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return err
	}
	verifier := a.derive([]byte(password), salt, uiArgonIterations, uiArgonMemoryKiB, uiArgonParallelism, uiArgonKeyBytes)
	config := uiVerifier{
		Version: uiAuthVersion, Algorithm: "argon2id",
		MemoryKiB: uiArgonMemoryKiB, Iterations: uiArgonIterations, Parallelism: uiArgonParallelism,
		Salt: base64.RawStdEncoding.EncodeToString(salt), Verifier: base64.RawStdEncoding.EncodeToString(verifier),
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return writePrivate(a.path(), data)
}

// Reset removes only the UI password verifier. The vault key and ciphertext
// remain untouched so terminal access can always recover password setup.
func (a *PasswordAuth) Reset() error {
	if err := os.MkdirAll(a.root, 0o700); err != nil {
		return err
	}
	release, err := acquireFileLock(filepath.Join(a.root, ".lock"))
	if err != nil {
		return err
	}
	defer release()
	if err := os.Remove(a.path()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove UI password config: %w", err)
	}
	return fileop.SyncDirectory(a.root)
}

func (a *PasswordAuth) Verify(password string) (bool, error) {
	if err := validateUIPassword(password); err != nil {
		return false, err
	}
	a.mu.Lock()
	now := a.now()
	if now.Before(a.blockedUntil) {
		a.mu.Unlock()
		return false, ErrUIAuthLimited
	}
	a.mu.Unlock()

	select {
	case a.gate <- struct{}{}:
	default:
		return false, ErrUIAuthBusy
	}
	started := a.now()
	config, err := a.load()
	if err != nil {
		<-a.gate
		if os.IsNotExist(err) {
			return false, ErrUIAuthNotSetup
		}
		return false, err
	}
	salt, expected, err := decodeVerifier(config)
	if err != nil {
		<-a.gate
		return false, err
	}
	actual := a.derive([]byte(password), salt, config.Iterations, config.MemoryKiB, config.Parallelism, uint32(len(expected)))
	ok := hmac.Equal(actual, expected)
	<-a.gate

	if ok {
		a.mu.Lock()
		a.failures = 0
		a.blockedUntil = time.Time{}
		a.mu.Unlock()
		return true, nil
	}
	if remaining := uiFailureFloor - a.now().Sub(started); remaining > 0 {
		a.sleep(remaining)
	}
	a.mu.Lock()
	a.failures++
	if a.failures >= uiFailureLimit {
		a.failures = 0
		a.blockedUntil = a.now().Add(uiFailureCooldown)
	}
	a.mu.Unlock()
	return false, nil
}

func (a *PasswordAuth) path() string {
	return filepath.Join(a.root, "ui.json")
}

func (a *PasswordAuth) load() (uiVerifier, error) {
	data, err := os.ReadFile(a.path())
	if err != nil {
		return uiVerifier{}, err
	}
	var config uiVerifier
	if err := json.Unmarshal(data, &config); err != nil {
		return uiVerifier{}, fmt.Errorf("decode UI password config: %w", err)
	}
	if config.Version != uiAuthVersion || config.Algorithm != "argon2id" {
		return uiVerifier{}, fmt.Errorf("unsupported UI password config")
	}
	return config, nil
}

func decodeVerifier(config uiVerifier) ([]byte, []byte, error) {
	if config.MemoryKiB == 0 || config.Iterations == 0 || config.Parallelism == 0 {
		return nil, nil, fmt.Errorf("invalid UI password parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(config.Salt)
	if err != nil || len(salt) < 16 {
		return nil, nil, fmt.Errorf("invalid UI password salt")
	}
	verifier, err := base64.RawStdEncoding.DecodeString(config.Verifier)
	if err != nil || len(verifier) != uiArgonKeyBytes {
		return nil, nil, fmt.Errorf("invalid UI password verifier")
	}
	return salt, verifier, nil
}

func validateUIPassword(password string) error {
	size := len([]byte(password))
	if size < 8 {
		return fmt.Errorf("password must be at least 8 bytes")
	}
	if size > 1024 {
		return fmt.Errorf("password exceeds 1024 bytes")
	}
	return nil
}
