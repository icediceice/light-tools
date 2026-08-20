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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/security"
)

const (
	normalDrillCap = 128 * 1024
	largeDrillCap  = 512 * 1024
)

type Request struct {
	Verb     string   `json:"verb"`
	TaskID   string   `json:"task_id,omitempty"`
	Async    bool     `json:"async,omitempty"`
	Service  string   `json:"service,omitempty"`
	Services []string `json:"services,omitempty"`
	Port     int      `json:"port,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Path     string   `json:"path,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
	Context  int      `json:"context,omitempty"`
	Lines    int      `json:"lines,omitempty"`
	Since    string   `json:"since,omitempty"`
	SinceTS  string   `json:"since_ts,omitempty"`
	Include  string   `json:"include,omitempty"`
	Exclude  string   `json:"exclude,omitempty"`
	Drill    bool     `json:"drill,omitempty"`
	Refresh  bool     `json:"refresh,omitempty"`
}

type Handler struct {
	registry *Registry
	tasks    *taskStore
	// confiner applies the same private-state deny list to caller paths and
	// registry-discovered file logs.
	confiner *security.Confiner
}

// New compiles the caller-path root union ONCE. security.ResolveBeneath
// canonicalizes every root on each call and errors on the first one that does
// not exist, so a single absent root would otherwise disable every other root
// on every request. Absent roots are dropped here instead.
func New(allowedRoots, logRoots, deniedRoots []string) (*Handler, error) {
	union := make([]string, 0, len(allowedRoots)+len(logRoots))
	seen := make(map[string]bool)
	for _, root := range append(append([]string{}, allowedRoots...), logRoots...) {
		if root == "" || seen[root] {
			continue
		}
		if _, err := os.Stat(root); err != nil {
			continue
		}
		seen[root] = true
		union = append(union, root)
	}
	if len(union) == 0 {
		return nil, fmt.Errorf("light_ops needs at least one readable root; check allowed_roots and log_roots")
	}
	confiner, err := security.NewConfiner(union, deniedRoots)
	if err != nil {
		return nil, err
	}
	return &Handler{registry: &Registry{}, tasks: newTaskStore(), confiner: confiner}, nil
}

// resolveCallerPath confines a path the CALLER supplied. Registry-discovered
// service log paths never come through here.
func (h *Handler) resolveCallerPath(path string) (string, error) {
	resolved, err := h.confiner.Resolve(path)
	if err != nil {
		return "", fmt.Errorf("path is outside the configured log roots: %w", err)
	}
	return resolved, nil
}

func (h *Handler) Handle(ctx context.Context, raw json.RawMessage) (any, error) {
	var request Request
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	switch request.Verb {
	case "status", "collect", "cancel":
		return h.tasks.action(request.Verb, request.TaskID)
	}
	if request.Async {
		request.Async = false
		id, err := h.tasks.start(func(taskContext context.Context) (any, error) {
			return h.handleSync(taskContext, request)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": id, "status": "queued"}, nil
	}
	return h.handleSync(ctx, request)
}

func (h *Handler) handleSync(ctx context.Context, request Request) (any, error) {
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
		return h.probeFile(request.Path)
	case "log_grep":
		return h.grepPool(ctx, request)
	case "log_correlate":
		return h.correlate(ctx, request)
	case "log_investigate":
		return h.investigate(ctx, request)
	case "log_window", "log_trace", "log_search", "log_errors", "log_since":
		return h.singleLogs(ctx, request)
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

// probeFile is caller-supplied by definition, so it is confined. A path outside
// the roots is REFUSED rather than answered with exists:false — reporting stat
// metadata for an arbitrary path is itself the leak.
func (h *Handler) probeFile(path string) (map[string]any, error) {
	resolved, err := h.resolveCallerPath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return map[string]any{"path": path, "exists": false, "error": err.Error()}, nil
	}
	return map[string]any{"path": path, "exists": true, "size": info.Size(), "mode": info.Mode().String(), "modified": info.ModTime().UTC()}, nil
}

func (h *Handler) singleLogs(ctx context.Context, request Request) (map[string]any, error) {
	content, id, err := h.sourceLogs(ctx, request)
	if err != nil {
		return nil, err
	}
	filtered := filterLines(content, request)
	filtered, capped := capOutput(filtered, request.Drill)
	return map[string]any{"service": id, "content": filtered, "capped": capped}, nil
}

// sourceLogs has three branches and only the FIRST TWO are confined. Those two
// take a path straight from the caller, so without a root check light_ops is a
// read-anything primitive (log_window path:/etc/shadow). The third branch
// resolves a path from the service registry, and it stays UNCONFINED on
// purpose: journalctl, docker and pm2 put logs wherever they put them, and
// reading service logs is what this tool is for.
func (h *Handler) sourceLogs(ctx context.Context, request Request) (string, string, error) {
	if request.Path != "" && request.Service == "" {
		path, err := h.resolveCallerPath(request.Path)
		if err != nil {
			return "", "", err
		}
		data, err := os.ReadFile(path)
		return string(data), "file:" + request.Path, err
	}
	if strings.HasPrefix(request.Service, "file:") {
		path := strings.TrimPrefix(request.Service, "file:")
		if path == "" {
			return "", "", fmt.Errorf("file service path is required")
		}
		path, err := h.resolveCallerPath(path)
		if err != nil {
			return "", "", err
		}
		data, err := os.ReadFile(path)
		return string(data), request.Service, err
	}
	service, err := h.registry.Resolve(ctx, request.Service)
	if err != nil {
		return "", "", err
	}
	if request.Lines <= 0 {
		request.Lines = 500
	}
	var content string
	switch service.Source {
	case "systemd":
		args := []string{"--no-pager", "-u", service.Name, "-n", strconv.Itoa(request.Lines)}
		if request.SinceTS != "" {
			args = append(args, "--since", request.SinceTS)
		} else if request.Since != "" {
			args = append(args, "--since", request.Since)
		}
		content, err = commandOutput(ctx, "journalctl", args...)
	case "docker":
		args := []string{"logs", "--tail", strconv.Itoa(request.Lines), "--timestamps"}
		if request.SinceTS != "" {
			args = append(args, "--since", request.SinceTS)
		} else if request.Since != "" {
			args = append(args, "--since", request.Since)
		}
		args = append(args, service.Name)
		content, err = commandOutput(ctx, "docker", args...)
	case "pm2":
		content, err = h.readPM2Logs(service, request.Lines)
	default:
		err = fmt.Errorf("unsupported service source")
	}
	return content, service.ID, err
}

func (h *Handler) grepPool(ctx context.Context, request Request) (map[string]any, error) {
	if request.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	services, err := h.registry.Discover(ctx, request.Refresh)
	if err != nil {
		return nil, err
	}
	type row struct {
		Service string `json:"service"`
		Hits    int    `json:"hits"`
		First   string `json:"first,omitempty"`
		Last    string `json:"last,omitempty"`
		Sample  string `json:"sample,omitempty"`
	}
	var rows []row
	for _, service := range services {
		local := request
		local.Service, local.Lines = service.ID, max(request.Lines, 5000)
		content, _, fetchErr := h.sourceLogs(ctx, local)
		if fetchErr != nil {
			continue
		}
		matches := matchingLines(content, request)
		if len(matches) == 0 {
			continue
		}
		rows = append(rows, row{Service: service.ID, Hits: len(matches), First: timestampOf(matches[0]), Last: timestampOf(matches[len(matches)-1]), Sample: matches[len(matches)-1]})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Last > rows[j].Last })
	return map[string]any{"pattern": request.Pattern, "services": rows}, nil
}

func (h *Handler) correlate(ctx context.Context, request Request) (map[string]any, error) {
	if len(request.Services) == 0 {
		return nil, fmt.Errorf("services is required")
	}
	var timeline []string
	for _, name := range request.Services {
		local := request
		local.Service = name
		content, id, err := h.sourceLogs(ctx, local)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(filterLines(content, local), "\n") {
			if line != "" {
				timeline = append(timeline, timestampOf(line)+" ["+id+"] "+line)
			}
		}
	}
	sort.Strings(timeline)
	content, capped := capOutput(strings.Join(timeline, "\n"), request.Drill)
	return map[string]any{"services": request.Services, "timeline": content, "capped": capped}, nil
}

func (h *Handler) investigate(ctx context.Context, request Request) (map[string]any, error) {
	if request.Pattern == "" {
		request.Pattern = "(?i)error|fatal|panic|exception"
	}
	grep, err := h.grepPool(ctx, request)
	if err != nil {
		return nil, err
	}
	rows := grep["services"]
	var samples []struct {
		Service string `json:"service"`
		Sample  string `json:"sample"`
	}
	encoded, _ := json.Marshal(rows)
	_ = json.Unmarshal(encoded, &samples)
	identifierPattern := regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_.:-]{7,}`)
	seen := make(map[string]bool)
	var identifiers []string
	for _, sample := range samples {
		for _, identifier := range identifierPattern.FindAllString(sample.Sample, -1) {
			if seen[identifier] {
				continue
			}
			seen[identifier] = true
			identifiers = append(identifiers, identifier)
			if len(identifiers) == 12 {
				break
			}
		}
		if len(identifiers) == 12 {
			break
		}
	}
	traces := make(map[string]any, len(identifiers))
	for _, identifier := range identifiers {
		traceRequest := request
		traceRequest.Pattern = regexp.QuoteMeta(identifier)
		traceRequest.Include, traceRequest.Exclude = "", ""
		trace, traceErr := h.grepPool(ctx, traceRequest)
		if traceErr == nil {
			traces[identifier] = trace["services"]
		}
	}
	summary := fmt.Sprintf("local investigation traced %d identifiers across registered services", len(identifiers))
	return map[string]any{"summary": summary, "errors": rows, "identifiers": identifiers, "traces": traces, "scope": "local-only"}, nil
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
	lines := strings.Split(content, "\n")
	pattern := request.Pattern
	if request.Verb == "log_errors" && pattern == "" {
		pattern = "(?i)error|fatal|panic|exception"
	}
	signal := compileFilter(pattern)
	include := compileFilter(request.Include)
	exclude := compileFilter(request.Exclude)
	cutoff := logCutoff(request)
	selected := make(map[int]bool)
	for index, line := range lines {
		if !cutoff.IsZero() {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			stamp, err := time.Parse(time.RFC3339Nano, fields[0])
			if err != nil || stamp.Before(cutoff) {
				continue
			}
		}
		if signal != nil && !signal.MatchString(line) {
			continue
		}
		if include != nil && !include.MatchString(line) {
			continue
		}
		if exclude != nil && exclude.MatchString(line) {
			continue
		}
		contextLines := request.Context
		for cursor := max(0, index-contextLines); cursor <= min(len(lines)-1, index+contextLines); cursor++ {
			selected[cursor] = true
		}
	}
	if signal == nil && include == nil && exclude == nil && cutoff.IsZero() {
		return content
	}
	var output []string
	for index, line := range lines {
		if selected[index] {
			output = append(output, line)
		}
	}
	return strings.Join(output, "\n")
}

func matchingLines(content string, request Request) []string {
	filtered := filterLines(content, Request{Verb: request.Verb, Pattern: request.Pattern, Include: request.Include, Exclude: request.Exclude})
	if filtered == "" {
		return nil
	}
	return strings.Split(filtered, "\n")
}

func logCutoff(request Request) time.Time {
	if request.SinceTS != "" {
		stamp, _ := time.Parse(time.RFC3339Nano, request.SinceTS)
		return stamp
	}
	if request.Since == "" {
		return time.Time{}
	}
	if duration, err := time.ParseDuration(request.Since); err == nil {
		return time.Now().Add(-duration)
	}
	stamp, _ := time.Parse(time.RFC3339Nano, request.Since)
	return stamp
}

func compileFilter(pattern string) *regexp.Regexp {
	if pattern == "" {
		return nil
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return regexp.MustCompile(regexp.QuoteMeta(pattern))
	}
	return expression
}

func capOutput(content string, drill bool) (string, bool) {
	limit := normalDrillCap
	if drill {
		limit = largeDrillCap
	}
	if len(content) <= limit {
		return content, false
	}
	return content[len(content)-limit:], true
}

func timestampOf(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	if _, err := time.Parse(time.RFC3339Nano, fields[0]); err == nil {
		return fields[0]
	}
	if len(line) > 32 {
		return line[:32]
	}
	return line
}
