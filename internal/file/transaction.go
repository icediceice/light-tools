package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/icediceice/light-tools/internal/security"
)

type Snapshotter interface {
	Capture(path string, preimage []byte, mode os.FileMode) error
}

type CommitRequest struct {
	Path        string
	Data        []byte
	ExpectedSHA string
	AllowedRoots []string
	Snapshotter Snapshotter
	Mode        os.FileMode
}

type CommitResult struct {
	Path      string `json:"path"`
	SHA       string `json:"sha_after"`
	Unchanged bool   `json:"unchanged,omitempty"`
}

var pathLocks sync.Map

func Commit(ctx context.Context, request CommitRequest) (CommitResult, error) {
	if err := ctx.Err(); err != nil {
		return CommitResult{}, err
	}
	resolved, err := security.ResolveBeneath(request.Path, request.AllowedRoots)
	if err != nil {
		return CommitResult{}, err
	}
	lockValue, _ := pathLocks.LoadOrStore(resolved, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	preimage, mode, exists, err := readPreimage(resolved)
	if err != nil {
		return CommitResult{}, err
	}
	beforeSHA := digest(preimage)
	if request.ExpectedSHA != "" && request.ExpectedSHA != beforeSHA {
		return CommitResult{}, fmt.Errorf("CAS conflict: expected %s, found %s", request.ExpectedSHA, beforeSHA)
	}
	afterSHA := digest(request.Data)
	if exists && beforeSHA == afterSHA {
		return CommitResult{Path: resolved, SHA: afterSHA, Unchanged: true}, nil
	}
	if request.Snapshotter != nil && exists {
		if err := request.Snapshotter.Capture(resolved, preimage, mode); err != nil {
			return CommitResult{}, fmt.Errorf("capture snapshot: %w", err)
		}
	}

	parent := filepath.Dir(resolved)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return CommitResult{}, fmt.Errorf("create parent: %w", err)
	}
	if err := security.Recheck(parent, parent, request.AllowedRoots); err != nil {
		return CommitResult{}, fmt.Errorf("parent identity check: %w", err)
	}
	temp, err := os.CreateTemp(parent, ".light-tools-*")
	if err != nil {
		return CommitResult{}, err
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}
	defer cleanup()

	writeMode := request.Mode
	if exists {
		writeMode = mode.Perm()
	}
	if writeMode == 0 {
		writeMode = 0o600
	}
	if err := temp.Chmod(writeMode); err != nil {
		return CommitResult{}, err
	}
	if _, err := temp.Write(request.Data); err != nil {
		return CommitResult{}, err
	}
	if err := temp.Sync(); err != nil {
		return CommitResult{}, err
	}
	if err := temp.Close(); err != nil {
		return CommitResult{}, err
	}
	if err := security.Recheck(parent, parent, request.AllowedRoots); err != nil {
		return CommitResult{}, fmt.Errorf("parent swapped before rename: %w", err)
	}
	if info, err := os.Lstat(resolved); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return CommitResult{}, errors.New("refusing to replace symlink target")
	} else if err != nil && !os.IsNotExist(err) {
		return CommitResult{}, err
	}
	if err := replaceFile(tempName, resolved); err != nil {
		return CommitResult{}, err
	}
	if err := syncDirectory(parent); err != nil {
		return CommitResult{}, err
	}
	if err := security.Recheck(resolved, resolved, request.AllowedRoots); err != nil {
		return CommitResult{}, fmt.Errorf("post-write identity check: %w", err)
	}
	return CommitResult{Path: resolved, SHA: afterSHA}, nil
}

func readPreimage(path string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, errors.New("refusing symlink target")
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, errors.New("target is not a regular file")
	}
	data, err := os.ReadFile(path)
	return data, info.Mode(), true, err
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func replaceFile(source, target string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(target)
	}
	return os.Rename(source, target)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}
