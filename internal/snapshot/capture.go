package snapshot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

type CaptureEntry struct {
	Path string `json:"path"`
	// Existed distinguishes "we snapshotted bytes" from "this path was absent
	// at capture time" — a modeled mv destination. Reverting the latter means
	// removing the file, not restoring content.
	Existed     bool   `json:"existed"`
	SHA256      string `json:"sha256,omitempty"`
	AfterSHA256 string `json:"after_sha256,omitempty"`
	Mode        uint32 `json:"mode,omitempty"`
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
	for _, path := range paths {
		clean := filepath.Clean(path)
		info, err := os.Lstat(clean)
		if os.IsNotExist(err) {
			record.Entries = append(record.Entries, CaptureEntry{Path: clean, Existed: false})
			continue
		}
		if err != nil {
			return CaptureRecord{}, err
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return CaptureRecord{}, err
		}
		if err := v.Capture(clean, data, info.Mode()); err != nil {
			return CaptureRecord{}, err
		}
		record.Entries = append(record.Entries, CaptureEntry{
			Path: clean, Existed: true, SHA256: hash(data), Mode: uint32(info.Mode().Perm()),
		})
	}
	if err := v.writeCapture(record); err != nil {
		return CaptureRecord{}, err
	}
	return record, nil
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
		data, err := os.ReadFile(entry.Path)
		if err != nil {
			// Absent after the command is the normal outcome of a delete; the
			// empty AfterSHA256 records exactly that.
			record.Entries[index].AfterSHA256 = ""
			continue
		}
		record.Entries[index].AfterSHA256 = hash(data)
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
		current, readErr := os.ReadFile(entry.Path)
		currentSHA := ""
		if readErr == nil {
			currentSHA = hash(current)
		}
		if !force && currentSHA != entry.AfterSHA256 {
			results = append(results, RestoreResult{
				Path: entry.Path, Action: "skipped", Skipped: true,
				Reason: "changed since the captured command ran — pass force:true to overwrite",
			})
			continue
		}
		if !entry.Existed {
			if readErr != nil {
				results = append(results, RestoreResult{Path: entry.Path, Action: "absent"})
				continue
			}
			if err := os.Remove(entry.Path); err != nil {
				return results, err
			}
			results = append(results, RestoreResult{Path: entry.Path, Action: "removed"})
			continue
		}
		if currentSHA == entry.SHA256 {
			results = append(results, RestoreResult{Path: entry.Path, Action: "unchanged"})
			continue
		}
		data, mode, err := v.loadBySHA(entry.Path, entry.SHA256)
		if err != nil {
			return results, err
		}
		if err := os.MkdirAll(filepath.Dir(entry.Path), 0o700); err != nil {
			return results, err
		}
		if err := os.WriteFile(entry.Path, data, mode); err != nil {
			return results, err
		}
		results = append(results, RestoreResult{Path: entry.Path, Action: "restored"})
	}
	return results, nil
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
