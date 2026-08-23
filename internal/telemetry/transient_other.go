//go:build !windows

package telemetry

import (
	"errors"
	"io/fs"
)

// Everywhere but Windows, a snapshot the writer superseded between the
// directory listing and the read simply disappears. Nothing else is treated as
// transient: a genuinely unreadable snapshot must still surface as a warning.
func isTransientReadError(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
