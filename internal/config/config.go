package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type RemoteProfile struct {
	Host      string
	User      string
	Port      int
	ProxyJump string
	KeyPath   string
}

type Config struct {
	// AllowedRoots is empty when no allowed_roots key was present, which means
	// UNCONFINED. That is the default posture: light-tools is meant to replace
	// the agent's native edit tool, and a tool confined to the spawn directory
	// cannot replace one that edits anywhere — it only sends the agent back to
	// the unbounded tool. Setting the key narrows; nothing widens it.
	AllowedRoots []string
	// RootsConfigured distinguishes an absent allowed_roots key from one present
	// but empty, so an operator who writes allowed_roots = [] gets a hard error
	// rather than silently inheriting the unconfined default.
	RootsConfigured bool
	// LogRoots widens light_ops beyond AllowedRoots for caller-supplied log
	// paths only. Registry-discovered service logs are never checked against it.
	LogRoots []string
	Remote   map[string]RemoteProfile
}

func Load(path string, defaultRoot string) (Config, error) {
	// No AllowedRoots seed: an absent allowed_roots key means unconfined. This
	// is the line that used to pin every file tool to the server's working
	// directory. defaultRoot is still the base for RELATIVE roots below.
	value := Config{Remote: make(map[string]RemoteProfile)}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		// No config file is a supported setup, but log roots still resolve from
		// the environment, the sibling .env and the built-in defaults.
		logRoots, resolveErr := resolveLogRoots(nil, filepath.Dir(path))
		if resolveErr != nil {
			return Config{}, resolveErr
		}
		value.LogRoots = logRoots
		return value, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	currentRemote := ""
	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.TrimSpace(line[1 : len(line)-1])
			if strings.HasPrefix(section, "remote.") {
				currentRemote = strings.Trim(strings.TrimPrefix(section, "remote."), "\"")
				if currentRemote == "" {
					return Config{}, fmt.Errorf("config line %d: empty remote profile", lineNumber)
				}
				continue
			}
			currentRemote = ""
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("config line %d: expected key = value", lineNumber)
		}
		key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
		if currentRemote == "" {
			switch key {
			case "allowed_roots":
				roots, err := parseArray(raw)
				if err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNumber, err)
				}
				value.RootsConfigured = true
				value.AllowedRoots = make([]string, 0, len(roots))
				for _, root := range roots {
					if strings.HasPrefix(root, "~") {
						expanded, err := expandRoot(root)
						if err != nil {
							return Config{}, fmt.Errorf("config line %d: %w", lineNumber, err)
						}
						value.AllowedRoots = append(value.AllowedRoots, expanded)
						continue
					}
					if !filepath.IsAbs(root) {
						root = filepath.Join(defaultRoot, root)
					}
					value.AllowedRoots = append(value.AllowedRoots, filepath.Clean(root))
				}
			case "log_roots":
				roots, err := parseArray(raw)
				if err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNumber, err)
				}
				value.LogRoots = roots
			}
			continue
		}
		profile := value.Remote[currentRemote]
		switch key {
		case "host":
			profile.Host, err = parseString(raw)
		case "user":
			profile.User, err = parseString(raw)
		case "proxy_jump":
			profile.ProxyJump, err = parseString(raw)
		case "key_path":
			profile.KeyPath, err = parseString(raw)
		case "port":
			profile.Port, err = strconv.Atoi(raw)
		default:
			err = fmt.Errorf("unknown remote key %q", key)
		}
		if err != nil {
			return Config{}, fmt.Errorf("config line %d: %w", lineNumber, err)
		}
		value.Remote[currentRemote] = profile
	}
	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	// An operator who writes allowed_roots = [] is asking for confinement and
	// would otherwise get the unconfined default — the opposite of the intent.
	// Refuse rather than guess.
	if value.RootsConfigured && len(value.AllowedRoots) == 0 {
		return Config{}, fmt.Errorf("allowed_roots is present but empty; remove the key to run unconfined, or list at least one root")
	}
	logRoots, err := resolveLogRoots(value.LogRoots, filepath.Dir(path))
	if err != nil {
		return Config{}, err
	}
	value.LogRoots = logRoots
	return value, nil
}

func parseString(raw string) (string, error) {
	value, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("expected quoted string")
	}
	return value, nil
}

func parseArray(raw string) ([]string, error) {
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("expected string array")
	}
	raw = strings.TrimSpace(raw[1 : len(raw)-1])
	if raw == "" {
		return []string{}, nil
	}
	var values []string
	for _, part := range strings.Split(raw, ",") {
		value, err := parseString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// LogRootsEnv overrides log roots from the process environment. It is the
// highest-precedence source so a launcher can set it without touching disk.
const LogRootsEnv = "LIGHT_TOOLS_LOG_ROOTS"

// defaultLogRoots are OPTIONAL. An absent default is dropped rather than
// failing, because security.ResolveBeneath canonicalizes every root up front
// and errors on the first one that does not exist — so a missing default would
// otherwise disable every other root too.
var defaultLogRoots = []string{"/var/log", "~/.local/log", "~/.pm2/logs"}

// resolveLogRoots applies precedence LogRootsEnv > .env > config.toml >
// built-in defaults. Configured roots are explicit: a missing one is a startup
// error, so a typo surfaces immediately instead of silently narrowing reads.
func resolveLogRoots(configured []string, configDirectory string) ([]string, error) {
	explicit, source := configured, "config.toml"
	fromFile, found, err := logRootsFromEnvFile(filepath.Join(configDirectory, ".env"))
	if err != nil {
		return nil, err
	}
	if found {
		explicit, source = fromFile, filepath.Join(configDirectory, ".env")
	}
	if raw, ok := os.LookupEnv(LogRootsEnv); ok {
		explicit, source = splitPathList(raw), LogRootsEnv
	}
	if len(explicit) > 0 {
		roots := make([]string, 0, len(explicit))
		for _, root := range explicit {
			expanded, err := expandRoot(root)
			if err != nil {
				return nil, fmt.Errorf("log root %q from %s: %w", root, source, err)
			}
			if _, err := os.Stat(expanded); err != nil {
				return nil, fmt.Errorf("log root %q from %s: %w", root, source, err)
			}
			roots = append(roots, expanded)
		}
		return roots, nil
	}
	roots := make([]string, 0, len(defaultLogRoots))
	for _, root := range defaultLogRoots {
		expanded, err := expandRoot(root)
		if err != nil {
			continue
		}
		if _, err := os.Stat(expanded); err != nil {
			continue
		}
		roots = append(roots, expanded)
	}
	return roots, nil
}

// logRootsFromEnvFile reads the .env sitting beside config.toml in the XDG
// config directory. It deliberately never looks at the process working
// directory: that tree is agent-writable, so a repo-local file must not be
// able to widen the filesystem boundary.
func logRootsFromEnvFile(path string) ([]string, bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	var values []string
	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != LogRootsEnv {
			continue
		}
		raw = strings.TrimSpace(raw)
		if unquoted, err := strconv.Unquote(raw); err == nil {
			raw = unquoted
		}
		values, found = splitPathList(raw), true
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return values, found, nil
}

func splitPathList(raw string) []string {
	var values []string
	for _, part := range strings.Split(raw, string(os.PathListSeparator)) {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

// expandRoot turns a configured root into an absolute cleaned path, expanding a
// leading ~ via the home directory. Without this a literal "~/.pm2/logs" is
// joined to the process working directory and never matches anything.
func expandRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("empty root")
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", root, err)
		}
		root = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(root, "~"), "/"))
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}
