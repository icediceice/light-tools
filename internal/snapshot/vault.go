package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const ringSize = 3

type Entry struct {
	Version int       `json:"version"`
	File    string    `json:"file"`
	SHA256  string    `json:"sha256"`
	Mode    uint32    `json:"mode"`
	Created time.Time `json:"created"`
}

type metadata struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type Vault struct {
	root  string
	locks sync.Map
}

func New(root string) *Vault { return &Vault{root: filepath.Clean(root)} }

func (v *Vault) Capture(path string, preimage []byte, mode os.FileMode) error {
	lock := v.lock(path)
	lock.Lock()
	defer lock.Unlock()

	directory := v.pathDirectory(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	meta, err := v.readMetadata(directory, path)
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("%d-%s.snapshot", time.Now().UnixNano(), shortHash(preimage))
	fullPath := filepath.Join(directory, filename)
	if err := os.WriteFile(fullPath, preimage, 0o600); err != nil {
		return err
	}
	meta.Entries = append([]Entry{{
		Version: 1, File: filename, SHA256: hash(preimage), Mode: uint32(mode.Perm()), Created: time.Now().UTC(),
	}}, meta.Entries...)
	if len(meta.Entries) > ringSize {
		for _, stale := range meta.Entries[ringSize:] {
			_ = os.Remove(filepath.Join(directory, stale.File))
		}
		meta.Entries = meta.Entries[:ringSize]
	}
	for index := range meta.Entries {
		meta.Entries[index].Version = index + 1
	}
	return writeJSONAtomic(filepath.Join(directory, "metadata.json"), meta)
}

func (v *Vault) List(path string) ([]Entry, error) {
	lock := v.lock(path)
	lock.Lock()
	defer lock.Unlock()
	meta, err := v.readMetadata(v.pathDirectory(path), path)
	if err != nil {
		return nil, err
	}
	return append([]Entry(nil), meta.Entries...), nil
}

func (v *Vault) Load(path string, version int) ([]byte, os.FileMode, error) {
	if version <= 0 {
		version = 1
	}
	lock := v.lock(path)
	lock.Lock()
	defer lock.Unlock()
	directory := v.pathDirectory(path)
	meta, err := v.readMetadata(directory, path)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range meta.Entries {
		if entry.Version == version {
			data, err := os.ReadFile(filepath.Join(directory, entry.File))
			if err != nil {
				return nil, 0, err
			}
			if hash(data) != entry.SHA256 {
				return nil, 0, fmt.Errorf("snapshot checksum mismatch")
			}
			return data, os.FileMode(entry.Mode), nil
		}
	}
	return nil, 0, fmt.Errorf("snapshot version %d not found", version)
}

// Reap only walks below the snapshots root owned by this Vault.
func (v *Vault) Reap(olderThan time.Time) error {
	entries, err := os.ReadDir(v.root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(olderThan) {
			_ = os.RemoveAll(filepath.Join(v.root, entry.Name()))
		}
	}
	return nil
}

func (v *Vault) pathDirectory(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return filepath.Join(v.root, hex.EncodeToString(sum[:]))
}

func (v *Vault) readMetadata(directory, path string) (metadata, error) {
	data, err := os.ReadFile(filepath.Join(directory, "metadata.json"))
	if os.IsNotExist(err) {
		return metadata{Path: filepath.Clean(path)}, nil
	}
	if err != nil {
		return metadata{}, err
	}
	var value metadata
	if err := json.Unmarshal(data, &value); err != nil {
		return metadata{}, err
	}
	if filepath.Clean(value.Path) != filepath.Clean(path) {
		return metadata{}, fmt.Errorf("snapshot path identity mismatch")
	}
	sort.SliceStable(value.Entries, func(i, j int) bool { return value.Entries[i].Version < value.Entries[j].Version })
	return value, nil
}

func (v *Vault) lock(path string) *sync.Mutex {
	value, _ := v.locks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	return value.(*sync.Mutex)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
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

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func shortHash(data []byte) string { return hash(data)[:12] }
