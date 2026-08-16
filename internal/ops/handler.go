package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const drillCap = 128 * 1024

type Request struct {
	Verb    string   `json:"verb"`
	Service string   `json:"service,omitempty"`
	Port    int      `json:"port,omitempty"`
	PID     int      `json:"pid,omitempty"`
	Path    string   `json:"path,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Lines   int      `json:"lines,omitempty"`
	Since   string   `json:"since,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Refresh bool     `json:"refresh,omitempty"`
}

type Handler struct {
	registry *Registry
}

func New() *Handler { return &Handler{registry: &Registry{}} }

func (h *Handler) Handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	switch request.Verb {
	case "list_services":
		return h.registry.Discover(ctx, request.Refresh)
	case "probe_service":
		return h.registry.Resolve(ctx, request.Service)
	case "probe_port":
		return probePort(request.Port)
	case "probe_process":
		return probeProcess(request.PID)
	case "probe_file":
		return probeFile(request.Path)
	case "log_window", "log_trace", "log_search", "log_grep", "log_errors", "log_since":
		return h.logs(ctx, request)
	default:
		return nil, fmt.Errorf("unsupported read-only ops verb %q", request.Verb)
	}
}

func probePort(port int) (map[string]any, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port")
	}
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 800*time.Millisecond)
	if err == nil {
		connection.Close()
	}
	return map[string]any{"port": port, "listening": err == nil}, nil
}

func probeProcess(pid int) (map[string]any, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid")
	}
	alive := true
	if runtime.GOOS == "linux" {
		_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
		alive = err == nil
	} else if _, err := os.FindProcess(pid); err != nil {
		alive = false
	}
	return map[string]any{"pid": pid, "alive": alive}, nil
}

func probeFile(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false, "error": err.Error()}, nil
	}
	return map[string]any{"path": path, "exists": true, "size": info.Size(), "mode": info.Mode().String(), "modified": info.ModTime().UTC()}, nil
}

func (h *Handler) logs(ctx context.Context, request Request) (map[string]any, error) {
	service, err := h.registry.Resolve(ctx, request.Service)
	if err != nil {
		return nil, err
	}
	if request.Lines <= 0 {
		request.Lines = 200
	}
	var content string
	switch service.Source {
	case "systemd":
		args := []string{"--no-pager", "-u", service.Name, "-n", strconv.Itoa(request.Lines)}
		if request.Since != "" {
			args = append(args, "--since", request.Since)
		}
		content, err = commandOutput(ctx, "journalctl", args...)
	case "docker":
		args := []string{"logs", "--tail", strconv.Itoa(request.Lines)}
		if request.Since != "" {
			args = append(args, "--since", request.Since)
		}
		args = append(args, service.Name)
		content, err = commandOutput(ctx, "docker", args...)
	case "pm2":
		content, err = readPM2Logs(service, request.Lines)
	default:
		err = fmt.Errorf("unsupported service source")
	}
	if err != nil {
		return nil, err
	}
	content = filterLines(content, request)
	if len(content) > drillCap {
		content = content[len(content)-drillCap:]
	}
	return map[string]any{"service": service.ID, "content": content, "capped": len(content) == drillCap}, nil
}

func readPM2Logs(service Service, limit int) (string, error) {
	var output []string
	for _, path := range []string{service.OutLog, service.ErrorLog} {
		if path == "" {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		var lines []string
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
			if len(lines) > limit {
				lines = lines[1:]
			}
		}
		file.Close()
		output = append(output, lines...)
	}
	return strings.Join(output, "\n"), nil
}

func filterLines(content string, request Request) string {
	var expression *regexp.Regexp
	pattern := request.Pattern
	if request.Verb == "log_errors" && pattern == "" {
		pattern = "(?i)error|fatal|panic|exception"
	}
	if pattern != "" {
		expression, _ = regexp.Compile(pattern)
		if expression == nil {
			expression = regexp.MustCompile(regexp.QuoteMeta(pattern))
		}
	}
	var output []string
	for _, line := range strings.Split(content, "\n") {
		if expression != nil && !expression.MatchString(line) {
			continue
		}
		if !matchesFilters(line, request.Include, request.Exclude) {
			continue
		}
		output = append(output, line)
	}
	return strings.Join(output, "\n")
}

func matchesFilters(line string, include, exclude []string) bool {
	lower := strings.ToLower(line)
	for _, value := range exclude {
		if strings.Contains(lower, strings.ToLower(value)) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, value := range include {
		if strings.Contains(lower, strings.ToLower(value)) {
			return true
		}
	}
	return false
}
