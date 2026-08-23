//go:build windows

package telemetry

import (
	"errors"
	"io/fs"
	"syscall"
)

// Windows refuses a read of a file another process is replacing, and it does
// so with a sharing or lock violation rather than ENOENT. Both mean the same
// thing here that ENOENT means on POSIX: the writer superseded this snapshot
// between the directory listing and the read, so the right answer is to
// rescan, not to warn about a store that is perfectly healthy.
//
// Declared numerically rather than by name so this compiles against any Go
// version's syscall table.
const (
	errorSharingViolation = syscall.Errno(32)
	errorLockViolation    = syscall.Errno(33)
)

func isTransientReadError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}
