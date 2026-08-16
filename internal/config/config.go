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
	AllowedRoots []string
	Remote       map[string]RemoteProfile
}

func Load(path string, defaultRoot string) (Config, error) {
	value := Config{AllowedRoots: []string{defaultRoot}, Remote: make(map[string]RemoteProfile)}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
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
			if key == "allowed_roots" {
				roots, err := parseArray(raw)
				if err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNumber, err)
				}
				value.AllowedRoots = make([]string, 0, len(roots))
				for _, root := range roots {
					if !filepath.IsAbs(root) {
						root = filepath.Join(defaultRoot, root)
					}
					value.AllowedRoots = append(value.AllowedRoots, filepath.Clean(root))
				}
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
