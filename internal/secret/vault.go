package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type diskStore struct {
	Values map[string]string `json:"values"`
}

type Vault struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Vault { return &Vault{root: filepath.Clean(root)} }

func (v *Vault) Set(name, value string) error {
	if err := validateName(name); err != nil {
		return err
	v.mu.Lock()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.load()
	if err != nil {
		return err
	}
	if store.Values == nil {
		store.Values = make(map[string]string)
	}
	store.Values[name] = value
	return v.save(store)
}

func (v *Vault) Remove(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.load()
	if err != nil {
		return err
	}
	delete(store.Values, name)
	return v.save(store)
}

func (v *Vault) List() ([]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(store.Values))
	for name := range store.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (v *Vault) Resolve(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.load()
	if err != nil {
		return "", err
	}
	value, ok := store.Values[name]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return value, nil
}

func (v *Vault) load() (diskStore, error) {
	key, err := v.key()
	if err != nil {
		return diskStore{}, err
	}
	data, err := os.ReadFile(filepath.Join(v.root, "vault.enc"))
	if os.IsNotExist(err) {
		return diskStore{Values: make(map[string]string)}, nil
	}
	if err != nil {
		return diskStore{}, err
	}
	encoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		return diskStore{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return diskStore{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return diskStore{}, err
	}
	if len(encoded) < aead.NonceSize() {
		return diskStore{}, fmt.Errorf("encrypted vault is truncated")
	}
	plaintext, err := aead.Open(nil, encoded[:aead.NonceSize()], encoded[aead.NonceSize():], []byte("light-tools-v1"))
	if err != nil {
		return diskStore{}, fmt.Errorf("decrypt vault: %w", err)
	}
	var store diskStore
	if err := json.Unmarshal(plaintext, &store); err != nil {
		return diskStore{}, err
	}
	return store, nil
}

func (v *Vault) save(store diskStore) error {
	key, err := v.key()
	if err != nil {
		return err
	}
	plaintext, err := json.Marshal(store)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ciphertext := aead.Seal(nonce, nonce, plaintext, []byte("light-tools-v1"))
	return writePrivate(filepath.Join(v.root, "vault.enc"), []byte(base64.RawStdEncoding.EncodeToString(ciphertext)))
}

func (v *Vault) key() ([]byte, error) {
	if err := os.MkdirAll(v.root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(v.root, "master.key")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("invalid vault key length")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := writePrivate(path, key); err != nil {
		return nil, err
	}
	return key, nil
}

func writePrivate(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("secret name is required")
	}
	for _, character := range name {
		if !(character == '-' || character == '_' || character == '.' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return fmt.Errorf("invalid secret name %q", name)
		}
	}
	return nil
}
