package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A .env in the process working directory must NOT be config authority. The
// working tree is agent-writable, so honouring it would let a repo-local file
// silently widen the boundary light_ops reads within.
func TestWorkingDirectoryEnvFileIsNotAuthority(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("LIGHT_TOOLS_LOG_ROOTS=/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)

	value, err := Load(filepath.Join(configDir, "config.toml"), workDir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(value.LogRoots, "/") {
		t.Fatalf("a cwd .env widened the log roots to /: %v", value.LogRoots)
	}
}

// The .env beside config.toml in the XDG config dir IS authority.
func TestXDGEnvFileIsHonoured(t *testing.T) {
	configDir := t.TempDir()
	logRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".env"),
		[]byte("# comment\nLIGHT_TOOLS_LOG_ROOTS="+logRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(filepath.Join(configDir, "config.toml"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(value.LogRoots, logRoot) {
		t.Fatalf("XDG .env log root missing: %v", value.LogRoots)
	}
}

// The process environment outranks the .env file.
func TestProcessEnvironmentOutranksEnvFile(t *testing.T) {
	configDir := t.TempDir()
	fromFile := t.TempDir()
	fromEnv := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, ".env"),
		[]byte("LIGHT_TOOLS_LOG_ROOTS="+fromFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(LogRootsEnv, fromEnv)

	value, err := Load(filepath.Join(configDir, "config.toml"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(value.LogRoots, fromEnv) || slices.Contains(value.LogRoots, fromFile) {
		t.Fatalf("process environment should win: %v", value.LogRoots)
	}
}

// A tilde root must expand against the home directory. Joining it to the
// working directory instead produced "$CWD/~/.pm2/logs", which matches nothing.
func TestTildeRootExpandsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	expanded, err := expandRoot("~/.pm2/logs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(expanded, home) {
		t.Fatalf("~ should expand to %s, got %s", home, expanded)
	}
}

// An explicitly configured root that does not exist is an error, so a typo
// surfaces at startup instead of silently narrowing what is readable.
func TestMissingExplicitRootFailsLoudly(t *testing.T) {
	configDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope")
	t.Setenv(LogRootsEnv, missing)
	if _, err := Load(filepath.Join(configDir, "config.toml"), t.TempDir()); err == nil {
		t.Fatal("a missing explicit log root must fail startup")
	}
}

// Built-in defaults are optional: absent ones are dropped rather than failing,
// because security.ResolveBeneath errors on the first missing root and would
// otherwise disable every other root too.
func TestAbsentBuiltinDefaultsAreDropped(t *testing.T) {
	value, err := Load(filepath.Join(t.TempDir(), "config.toml"), t.TempDir())
	if err != nil {
		t.Fatalf("absent built-in defaults must not fail startup: %v", err)
	}
	for _, root := range value.LogRoots {
		if _, statErr := os.Stat(root); statErr != nil {
			t.Fatalf("resolved log root %s does not exist", root)
		}
	}
}
