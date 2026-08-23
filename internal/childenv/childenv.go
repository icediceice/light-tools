// Package childenv owns the single minimal environment policy for every child
// process launched by light-tools.
package childenv

import (
	"os"
	"runtime"
	"sort"
	"strings"
)

var posixEnvironmentNames = [...]string{
	"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "SSH_AUTH_SOCK", "SYSTEMROOT",
}

var windowsEnvironmentNames = [...]string{
	"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TMPDIR", "SSH_AUTH_SOCK", "SYSTEMROOT",
	"PATHEXT", "USERPROFILE", "TEMP", "TMP", "COMSPEC", "WINDIR", "APPDATA", "LOCALAPPDATA",
	"SYSTEMDRIVE", "PROGRAMDATA", "PSMODULEPATH",
}

// Minimal filters the current process environment for the current platform.
func Minimal() []string {
	return Filter(os.Environ(), runtime.GOOS)
}

// Filter is pure so every platform policy can be tested on every host.
func Filter(entries []string, goos string) []string {
	names := posixEnvironmentNames[:]
	foldCase := false
	if goos == "windows" {
		names = windowsEnvironmentNames[:]
		foldCase = true
	}

	// A nil exec.Cmd.Env inherits the complete parent environment, so construct
	// a non-nil result even when no allowlisted entries exist.
	environment := make([]string, 0, len(names)+1)
	hasPATHEXT := false
	for _, allowedName := range names {
		for _, item := range entries {
			name, _, ok := strings.Cut(item, "=")
			if !ok || name == "" {
				continue
			}
			matches := name == allowedName
			if foldCase {
				matches = strings.EqualFold(name, allowedName)
			}
			if !matches {
				continue
			}
			environment = append(environment, item)
			if allowedName == "PATHEXT" {
				hasPATHEXT = true
			}
			break
		}
	}
	if goos == "windows" && !hasPATHEXT {
		environment = append(environment, "PATHEXT=.COM;.EXE;.BAT;.CMD")
	}
	sort.Strings(environment)
	return environment
}
