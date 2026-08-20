package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	fileop "github.com/icediceice/light-tools/internal/file"
)

const (
	// MaxValueBytes is the shared CLI and HTTP limit for one secret value.
	MaxValueBytes     = 1 << 20
	maxSecretBytes    = MaxValueBytes
	maxVaultPlaintext = 8 << 20
)

type SecretMetadata struct {
	Group     string    `json:"group,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type SecretInfo struct {
	Name      string    `json:"name"`
	Group     string    `json:"group,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Overview struct {
	Secrets []SecretInfo `json:"secrets"`
	Groups  []string     `json:"groups"`
}

type diskStore struct {
	Values   map[string]string         `json:"values"`
	Metadata map[string]SecretMetadata `json:"metadata,omitempty"`
	Groups   map[string]bool           `json:"groups,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

func (s *diskStore) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if value := raw["values"]; value != nil {
		if err := json.Unmarshal(value, &s.Values); err != nil {
			return fmt.Errorf("decode vault values: %w", err)
		}
	}
	if value := raw["metadata"]; value != nil {
		if err := json.Unmarshal(value, &s.Metadata); err != nil {
			return fmt.Errorf("decode vault metadata: %w", err)
		}
	}
	if value := raw["groups"]; value != nil {
		if err := json.Unmarshal(value, &s.Groups); err != nil {
			return fmt.Errorf("decode vault groups: %w", err)
		}
	}
	delete(raw, "values")
	delete(raw, "metadata")
	delete(raw, "groups")
	s.Extra = raw
	s.ensure()
	return nil
}

func (s diskStore) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(s.Extra)+3)
	for name, value := range s.Extra {
		raw[name] = value
	}
	values, err := json.Marshal(s.Values)
	if err != nil {
		return nil, err
	}
	raw["values"] = values
	if len(s.Metadata) > 0 {
		metadata, err := json.Marshal(s.Metadata)
		if err != nil {
			return nil, err
		}
		raw["metadata"] = metadata
	}
	if len(s.Groups) > 0 {
		groups, err := json.Marshal(s.Groups)
		if err != nil {
			return nil, err
		}
		raw["groups"] = groups
	}
	return json.Marshal(raw)
}

func (s *diskStore) ensure() {
	if s.Values == nil {
		s.Values = make(map[string]string)
	}
	if s.Metadata == nil {
		s.Metadata = make(map[string]SecretMetadata)
	}
	if s.Groups == nil {
		s.Groups = make(map[string]bool)
	}
	if s.Extra == nil {
		s.Extra = make(map[string]json.RawMessage)
	}
}

type Vault struct {
	root string
	mu   sync.Mutex
}

func New(root string) *Vault { return &Vault{root: filepath.Clean(root)} }

func (v *Vault) Set(name, value string) error {
	return v.set(name, value, nil)
}

func (v *Vault) SetWithGroup(name, value, group string) error {
	normalized := ""
	if group != "" {
		var err error
		normalized, err = normalizeGroup(group)
		if err != nil {
			return err
		}
	}
	return v.set(name, value, &normalized)
}

func (v *Vault) set(name, value string, group *string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if len([]byte(value)) > maxSecretBytes {
		return fmt.Errorf("secret value exceeds %d bytes", maxSecretBytes)
	}
	return v.mutate(func(store *diskStore) error {
		store.Values[name] = value
		metadata := store.Metadata[name]
		if group != nil {
			metadata.Group = *group
			if *group != "" {
				store.Groups[*group] = true
			}
		}
		metadata.UpdatedAt = time.Now().UTC()
		store.Metadata[name] = metadata
		return nil
	})
}

func (v *Vault) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	return v.mutate(func(store *diskStore) error {
		delete(store.Values, name)
		delete(store.Metadata, name)
		return nil
	})
}

func (v *Vault) AddGroup(group string) error {
	normalized, err := normalizeGroup(group)
	if err != nil {
		return err
	}
	return v.mutate(func(store *diskStore) error {
		store.Groups[normalized] = true
		return nil
	})
}

func (v *Vault) DeleteGroup(group string) error {
	normalized, err := normalizeGroup(group)
	if err != nil {
		return err
	}
	return v.mutate(func(store *diskStore) error {
		delete(store.Groups, normalized)
		for name, metadata := range store.Metadata {
			if metadata.Group == normalized {
				metadata.Group = ""
				metadata.UpdatedAt = time.Now().UTC()
				store.Metadata[name] = metadata
			}
		}
		return nil
	})
}

func (v *Vault) RenameGroup(from, to string) error {
	source, err := normalizeGroup(from)
	if err != nil {
		return err
	}
	target, err := normalizeGroup(to)
	if err != nil {
		return err
	}
	if source == target {
		return nil
	}
	return v.mutate(func(store *diskStore) error {
		if !store.Groups[source] {
			return fmt.Errorf("group %q not found", source)
		}
		if store.Groups[target] {
			return fmt.Errorf("group %q already exists", target)
		}
		delete(store.Groups, source)
		store.Groups[target] = true
		for name, metadata := range store.Metadata {
			if metadata.Group == source {
				metadata.Group = target
				metadata.UpdatedAt = time.Now().UTC()
				store.Metadata[name] = metadata
			}
		}
		return nil
	})
}

func (v *Vault) AssignGroup(name, group string) error {
	if err := validateName(name); err != nil {
		return err
	}
	normalized := ""
	var err error
	if group != "" {
		normalized, err = normalizeGroup(group)
		if err != nil {
			return err
		}
	}
	return v.mutate(func(store *diskStore) error {
		if _, ok := store.Values[name]; !ok {
			return fmt.Errorf("secret %q not found", name)
		}
		metadata := store.Metadata[name]
		metadata.Group = normalized
		metadata.UpdatedAt = time.Now().UTC()
		store.Metadata[name] = metadata
		if normalized != "" {
			store.Groups[normalized] = true
		}
		return nil
	})
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

func (v *Vault) Overview() (Overview, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	store, err := v.load()
	if err != nil {
		return Overview{}, err
	}
	result := Overview{
		Secrets: make([]SecretInfo, 0, len(store.Values)),
		Groups:  make([]string, 0, len(store.Groups)),
	}
	for name := range store.Values {
		metadata := store.Metadata[name]
		result.Secrets = append(result.Secrets, SecretInfo{Name: name, Group: metadata.Group, UpdatedAt: metadata.UpdatedAt})
	}
	for group := range store.Groups {
		result.Groups = append(result.Groups, group)
	}
	sort.Slice(result.Secrets, func(i, j int) bool { return result.Secrets[i].Name < result.Secrets[j].Name })
	sort.Strings(result.Groups)
	return result, nil
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

func (v *Vault) mutate(change func(*diskStore) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := os.MkdirAll(v.root, 0o700); err != nil {
		return err
	}
	release, err := acquireFileLock(filepath.Join(v.root, ".lock"))
	if err != nil {
		return err
	}
	defer release()

	store, err := v.load()
	if err != nil {
		return err
	}
	before, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if err := change(&store); err != nil {
		return err
	}
	after, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if len(after) > maxVaultPlaintext && len(after) > len(before) {
		return fmt.Errorf("vault exceeds %d bytes; remove data before adding more", maxVaultPlaintext)
	}
	return v.savePlaintext(after)
}

func (v *Vault) load() (diskStore, error) {
	path := filepath.Join(v.root, "vault.enc")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		store := diskStore{}
		store.ensure()
		return store, nil
	}
	if err != nil {
		return diskStore{}, err
	}
	key, err := v.key(false)
	if err != nil {
		return diskStore{}, err
	}
	encoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		return diskStore{}, fmt.Errorf("decode encrypted vault: %w", err)
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
		return diskStore{}, fmt.Errorf("decode vault: %w", err)
	}
	store.ensure()
	return store, nil
}

func (v *Vault) save(store diskStore) error {
	plaintext, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return v.savePlaintext(plaintext)
}

func (v *Vault) savePlaintext(plaintext []byte) error {
	key, err := v.key(true)
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

func (v *Vault) key(create bool) ([]byte, error) {
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
	if !create {
		return nil, fmt.Errorf("vault key is missing; restore %s before using vault.enc", path)
	}
	if _, err := os.Stat(filepath.Join(v.root, "vault.enc")); err == nil {
		return nil, fmt.Errorf("vault key is missing; refusing to replace the key for existing vault.enc")
	} else if !os.IsNotExist(err) {
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
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".secret-*")
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
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return fileop.SyncDirectory(parent)
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

func normalizeGroup(group string) (string, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return "", fmt.Errorf("group name is required")
	}
	if !utf8.ValidString(group) || utf8.RuneCountInString(group) > 64 {
		return "", fmt.Errorf("invalid group name")
	}
	for _, character := range group {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("invalid group name")
		}
	}
	return group, nil
}
