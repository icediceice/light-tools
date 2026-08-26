package snapshot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A capture pins the whole surface of one shell command under a single id.
//
// The per-path ring in vault.go already stores preimages; what a shell mutator
// needs on top of it is one handle covering EVERY path the command touched, so
// a revert is one call rather than a path-by-path reconstruction the caller
// would have to derive from a command it no longer has.
//
// AfterSHA256 is what makes a non-force revert honest. It records what the
// command LEFT behind, so a later writer is detectable: if the file no longer
// matches, something wrote after us and a non-force restore must skip it rather
// than silently discard that work.

const captureDirectory = "captures"

const captureCeilingBytes int64 = 64 << 20

// TooLarge reports a capture surface whose regular-file preimages exceed the
// bounded lane's byte ceiling.
type TooLarge struct {
	Path  string
	Limit int64
}

func (e *TooLarge) Error() string {
	return fmt.Sprintf("capture surface exceeds %d-byte ceiling while reading %s", e.Limit, e.Path)
}

// Kinds a capture can restore. A symlink is not a file with link-shaped
// content: the delete matrix admits symlinks as the ONLY hazard, so a revert
// that turns one back into a regular file has changed the filesystem rather
// than restored it.
const (
	kindFile    = "file"
	kindSymlink = "symlink"
)

type CaptureEntry struct {
	Path string `json:"path"`
	// Existed distinguishes "we snapshotted bytes" from "this path was absent
	// at capture time" — a modeled mv destination. Reverting the latter means
	// removing the file, not restoring content.
	Existed bool   `json:"existed"`
	Kind    string `json:"kind,omitempty"`
	// LinkTarget carries the whole preimage of a symlink; SHA256 carries the
	// preimage of a regular file. Exactly one is set on an entry that existed.
	LinkTarget string `json:"link_target,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	// Blob names this capture's OWN copy of the preimage, under
	// captures/<id>/. The per-path ring keeps only three versions and deletes
	// what it evicts, so a handle that resolved through the ring stopped
	// working after three later snapshots of the same path — the capture has
	// to own its bytes to keep the promise the terminal printed.
	Blob            string `json:"blob,omitempty"`
	AfterKind       string `json:"after_kind,omitempty"`
	AfterLinkTarget string `json:"after_link_target,omitempty"`
	AfterSHA256     string `json:"after_sha256,omitempty"`
	Mode            uint32 `json:"mode,omitempty"`
}

type CaptureRecord struct {
	ID      string         `json:"id"`
	Command string         `json:"command"`
	Created time.Time      `json:"created"`
	Entries []CaptureEntry `json:"entries"`
}

func NewCaptureID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// CaptureSurface snapshots every path in the surface before the command runs.
// A path that does not exist is recorded as absent rather than skipped: the
// record has to describe the whole surface or a revert cannot undo a creation.
func (v *Vault) CaptureSurface(id, command string, paths []string) (CaptureRecord, error) {
	record := CaptureRecord{ID: id, Command: command, Created: time.Now().UTC()}
	for index, path := range paths {
		clean := filepath.Clean(path)
		info, err := os.Lstat(clean)
		if os.IsNotExist(err) {
			record.Entries = append(record.Entries, CaptureEntry{Path: clean, Existed: false})
			continue
		}
		if err != nil {
			return CaptureRecord{}, err
		}
		// Lstat, then read the link ITSELF. Reading through it stored the
		// target's bytes under the link's path, which restored as a regular
		// file — and a dangling link failed the read outright, refusing the
		// whole command over a path the matrix had already admitted.
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(clean)
			if err != nil {
				return CaptureRecord{}, err
			}
			record.Entries = append(record.Entries, CaptureEntry{
				Path: clean, Existed: true, Kind: kindSymlink, LinkTarget: target,
			})
			continue
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return CaptureRecord{}, err
		}
		if err := v.Capture(clean, data, info.Mode()); err != nil {
			return CaptureRecord{}, err
		}
		blob, err := v.writeCaptureBlob(id, index, data)
		if err != nil {
			return CaptureRecord{}, err
		}
		record.Entries = append(record.Entries, CaptureEntry{
			Path: clean, Existed: true, Kind: kindFile, SHA256: hash(data),
			Blob: blob, Mode: uint32(info.Mode().Perm()),
		})
	}
	if err := v.writeCapture(record); err != nil {
		return CaptureRecord{}, err
	}
	return record, nil
}

// stateOf describes what is on disk now in the same terms the capture recorded.
// An unreadable or absent path is the empty state, which is exactly what a
// completed delete should look like.
func stateOf(path string) (kind, fingerprint string) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", ""
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", ""
		}
		return kindSymlink, target
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return kindFile, hash(data)
}

// SealCapture records what the command actually left behind. It is called after
// the command returns, whatever its exit code — a partial effect is exactly the
// case where a revert handle matters most.
func (v *Vault) SealCapture(id string) error {
	record, err := v.LoadCapture(id)
	if err != nil {
		return err
	}
	for index, entry := range record.Entries {
		// Absent after the command is the normal outcome of a delete; the
		// empty after-state records exactly that.
		kind, fingerprint := stateOf(entry.Path)
		record.Entries[index].AfterKind = kind
		record.Entries[index].AfterSHA256 = ""
		record.Entries[index].AfterLinkTarget = ""
		switch kind {
		case kindSymlink:
			record.Entries[index].AfterLinkTarget = fingerprint
		case kindFile:
			record.Entries[index].AfterSHA256 = fingerprint
		}
	}
	return v.writeCapture(record)
}

func (v *Vault) LoadCapture(id string) (CaptureRecord, error) {
	if err := validCaptureID(id); err != nil {
		return CaptureRecord{}, err
	}
	data, err := os.ReadFile(v.capturePath(id))
	if os.IsNotExist(err) {
		return CaptureRecord{}, fmt.Errorf("capture %s not found", id)
	}
	if err != nil {
		return CaptureRecord{}, err
	}
	var record CaptureRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return CaptureRecord{}, err
	}
	return record, nil
}

func (v *Vault) ListCaptures() ([]CaptureRecord, error) {
	entries, err := os.ReadDir(filepath.Join(v.root, captureDirectory))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []CaptureRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := v.LoadCapture(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Created.After(records[j].Created) })
	return records, nil
}

// RestoreResult reports one path's outcome. Skipped is not a failure: it is the
// non-force guard refusing to discard a later writer's work.
type RestoreResult struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// RestoreCapture reverts every entry. Without force, a path whose current bytes
// differ from what the command left is skipped and named, because something
// wrote after the captured command and clobbering it would destroy work the
// caller never saw.
func (v *Vault) RestoreCapture(id string, force bool) ([]RestoreResult, error) {
	record, err := v.LoadCapture(id)
	if err != nil {
		return nil, err
	}
	results := make([]RestoreResult, 0, len(record.Entries))
	for _, entry := range record.Entries {
		currentKind, currentFingerprint := stateOf(entry.Path)
		afterKind, afterFingerprint := entry.AfterKind, entry.AfterSHA256
		if afterKind == kindSymlink {
			afterFingerprint = entry.AfterLinkTarget
		}
		if !force && (currentKind != afterKind || currentFingerprint != afterFingerprint) {
			results = append(results, RestoreResult{
				Path: entry.Path, Action: "skipped", Skipped: true,
				Reason: "changed since the captured command ran — pass force:true to overwrite",
			})
			continue
		}
		if !entry.Existed {
			if currentKind == "" {
				results = append(results, RestoreResult{Path: entry.Path, Action: "absent"})
				continue
			}
			if err := os.Remove(entry.Path); err != nil {
				return results, err
			}
			results = append(results, RestoreResult{Path: entry.Path, Action: "removed"})
			continue
		}
		wantKind, wantFingerprint := entry.Kind, entry.SHA256
		if wantKind == kindSymlink {
			wantFingerprint = entry.LinkTarget
		}
		if currentKind == wantKind && currentFingerprint == wantFingerprint {
			results = append(results, RestoreResult{Path: entry.Path, Action: "unchanged"})
			continue
		}
		if err := os.MkdirAll(filepath.Dir(entry.Path), 0o700); err != nil {
			return results, err
		}
		// Clear whatever is there first. Writing over a symlink would follow it
		// and corrupt the target instead of replacing the link.
		if currentKind != "" {
			if err := os.Remove(entry.Path); err != nil && !os.IsNotExist(err) {
				return results, err
			}
		}
		if wantKind == kindSymlink {
			if err := os.Symlink(entry.LinkTarget, entry.Path); err != nil {
				return results, err
			}
			results = append(results, RestoreResult{Path: entry.Path, Action: "restored"})
			continue
		}
		data, mode, err := v.loadPreimage(id, entry)
		if err != nil {
			return results, err
		}
		if err := os.WriteFile(entry.Path, data, mode); err != nil {
			return results, err
		}
		results = append(results, RestoreResult{Path: entry.Path, Action: "restored"})
	}
	return results, nil
}

// loadPreimage prefers the capture's own copy and falls back to the per-path
// ring. The fallback is what keeps records written before captures owned their
// bytes restorable; it is not a substitute, because the ring evicts.
func (v *Vault) loadPreimage(id string, entry CaptureEntry) ([]byte, os.FileMode, error) {
	if entry.Blob != "" {
		data, err := os.ReadFile(v.captureBlobPath(id, entry.Blob))
		if err == nil {
			return data, os.FileMode(entry.Mode), nil
		}
		if !os.IsNotExist(err) {
			return nil, 0, err
		}
	}
	return v.loadBySHA(entry.Path, entry.SHA256)
}

// loadBySHA walks the path's ring for the exact preimage this capture pinned.
// Matching on content rather than on version number keeps the capture correct
// when later writes have rotated the ring underneath it.
func (v *Vault) loadBySHA(path, sha string) ([]byte, os.FileMode, error) {
	entries, err := v.List(path)
	if err != nil {
		return nil, 0, err
	}
	for _, entry := range entries {
		if entry.SHA256 != sha {
			continue
		}
		return v.Load(path, entry.Version)
	}
	return nil, 0, fmt.Errorf("captured preimage for %s is no longer in the ring", path)
}

// writeCaptureBlob stores this capture's own copy of one preimage. The name is
// index-scoped so the same path appearing twice in a surface keeps both.
func (v *Vault) writeCaptureBlob(id string, index int, data []byte) (string, error) {
	if err := validCaptureID(id); err != nil {
		return "", err
	}
	directory := filepath.Join(v.root, captureDirectory, id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d.blob", index)
	if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
		return "", err
	}
	return name, nil
}

func (v *Vault) captureBlobPath(id, blob string) string {
	return filepath.Join(v.root, captureDirectory, id, filepath.Base(blob))
}

func (v *Vault) capturePath(id string) string {
	return filepath.Join(v.root, captureDirectory, id+".json")
}

func (v *Vault) writeCapture(record CaptureRecord) error {
	if err := validCaptureID(record.ID); err != nil {
		return err
	}
	directory := filepath.Join(v.root, captureDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return writeJSONAtomic(v.capturePath(record.ID), record)
}

// validCaptureID keeps an id from escaping the captures directory: it is echoed
// back to the model in a terminal and comes back as caller input on restore.
func validCaptureID(id string) error {
	if id == "" {
		return fmt.Errorf("capture id is required")
	}
	for _, character := range id {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return fmt.Errorf("capture id must be lowercase hex")
	}
	return nil
}
