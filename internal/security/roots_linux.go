//go:build linux

package security

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformBeneath(root, relative string) error {
	rootFD, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open allowed root: %w", err)
	}
	defer unix.Close(rootFD)

	if relative == "." {
		relative = ""
	}
	relative = filepath.ToSlash(relative)
	fd, err := unix.Openat2(rootFD, relative, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == nil {
		return unix.Close(fd)
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || (relative == "" && errors.Is(err, unix.ENOENT)) {
		return nil // guarded fallback remains protected by EvalSymlinks + final recheck
	}
	return fmt.Errorf("openat2 beneath check: %w", err)
}
