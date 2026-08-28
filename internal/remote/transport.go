package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/childenv"
	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/logs"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/telemetry"
)

type SSHRequest struct {
	Profile   string `json:"profile,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Command   string `json:"command"`
	Key       string `json:"key,omitempty"`
	KeyRef    string `json:"key_ref,omitempty"`
	CertRef   string `json:"cert_ref,omitempty"`
	Port      int    `json:"port,omitempty"`
	ProxyJump string `json:"proxy_jump,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
	// Compact is the exact-output valve, and it is a POINTER because its three
	// states are genuinely different: nil auto-detects, false guarantees the
	// bytes are returned verbatim, true compacts even a payload auto would have
	// left alone. light_ssh callers decode stdout — NDJSON, base64, tar bytes —
	// so an unconditional default-on would silently corrupt an existing
	// contract. The valve is what makes default-on safe rather than breaking.
	Compact *bool `json:"compact,omitempty"`
}

type SCPRequest struct {
	Profile   string `json:"profile,omitempty"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Key       string `json:"key,omitempty"`
	KeyRef    string `json:"key_ref,omitempty"`
	CertRef   string `json:"cert_ref,omitempty"`
	Port      int    `json:"port,omitempty"`
	ProxyJump string `json:"proxy_jump,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type connection struct {
	remote    string
	key       string
	cert      string
	port      int
	proxyJump string
}

type commandRunner func(context.Context, string, []string, int, bool) (string, string, int, error)

type Transport struct {
	profiles map[string]config.RemoteProfile
	confiner *security.Confiner
	secrets  *secret.Vault
	runner   commandRunner
	// spills is the SAME store light_bash owns, so a remote outline is
	// recovered through light_bash read_block. Nil disables compaction
	// entirely rather than emitting an outline nothing can recover.
	spills   logs.Spiller
	recorder telemetry.Recorder
}

func New(profiles map[string]config.RemoteProfile, confiner *security.Confiner, secrets *secret.Vault, spills logs.Spiller, recorder telemetry.Recorder) *Transport {
	return &Transport{profiles: profiles, confiner: confiner, secrets: secrets, runner: runCommand, spills: spills, recorder: recorder}
}

func (t *Transport) SSH(ctx context.Context, raw json.RawMessage) (any, error) {
	var request SSHRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	settings, err := t.connection(request.Profile)
	if err != nil {
		return nil, err
	}
	if request.Remote != "" {
		settings.remote = request.Remote
	}
	if request.Key != "" {
		settings.key = request.Key
	}
	if request.Port > 0 {
		settings.port = request.Port
	}
	if request.ProxyJump != "" {
		settings.proxyJump = request.ProxyJump
	}
	cleanup, err := t.materializeRefs(&settings, request.KeyRef, request.CertRef)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if settings.remote == "" {
		return nil, fmt.Errorf("remote or profile is required")
	}
	args := sshOptions(settings, false)
	args = append(args, settings.remote, request.Command)
	stdout, stderr, exitCode, err := t.runner(ctx, "ssh", args, request.TimeoutMS, false)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"exit_code": exitCode}
	if !t.compactionWanted(request, stdout) {
		result["stdout"], result["stderr"] = stdout, stderr
		return result, nil
	}
	opts := logs.Options{Budget: remoteOutputLimit}
	outStream := logs.Compact(stdout, opts, t.spills, t.recorder)
	errStream := logs.Compact(stderr, opts, t.spills, t.recorder)
	result["stdout"], result["stderr"] = outStream.Text, errStream.Text
	attachStream(result, "stdout", outStream)
	attachStream(result, "stderr", errStream)
	return result, nil
}

// remoteOutputLimit mirrors light_bash's own ceiling: the two tools return the
// same shape of thing to the same reader, so they bound it the same way.
const remoteOutputLimit = 128 * 1024

// compactionWanted resolves the three-state valve.
//
// AUTO BYPASSES STRUCTURED PAYLOADS. Today light_ssh returns stdout verbatim
// with no limit, filter or spill, so callers already decode it — NDJSON from a
// log query, a base64 blob, a JSON document. Those have an exact-output
// contract that a template outline would silently break, and unlike prose the
// breakage is not visible in the result: json.Unmarshal simply fails somewhere
// downstream. So auto errs toward exactness and the caller can force the other
// way with compact:true.
func (t *Transport) compactionWanted(request SSHRequest, stdout string) bool {
	if t.spills == nil {
		// No shared spill store means no resolvable recovery pointer, and an
		// outline that cannot be recovered is worse than no compaction at all.
		return false
	}
	if request.Compact != nil {
		return *request.Compact
	}
	return !looksStructured(stdout)
}

// looksStructured reports whether stdout is a machine-readable payload rather
// than human output. It judges the FIRST non-blank line only: NDJSON is a
// stream of independent documents, so its first line is representative, and
// scanning the whole body would make the verdict depend on how far a stream
// happened to get.
func looksStructured(stdout string) bool {
	trimmed := strings.TrimLeft(stdout, " \t\r\n")
	if trimmed == "" {
		return false
	}
	line := trimmed
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	switch line[0] {
	case '{', '[':
		return json.Valid([]byte(line)) || json.Valid([]byte(trimmed))
	}
	return false
}

// attachStream puts one compacted stream's recovery metadata on the result. A
// spill id travels WITH the outline it backs: light_ssh renders two streams and
// a bare id would not say which one its ranges address.
func attachStream(result map[string]any, name string, stream logs.Stream) {
	if stream.Skipped {
		if stream.SpillFailed {
			result[name+"_compaction_skipped"] = true
		}
		return
	}
	if stream.SpillID == "" {
		return
	}
	result[name+"_spill_id"] = stream.SpillID
	result[name+"_recover"] = logs.RecoveryHint(stream.SpillID)
}

func (t *Transport) SCP(ctx context.Context, raw json.RawMessage) (any, error) {
	var request SCPRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Src == "" || request.Dst == "" {
		return nil, fmt.Errorf("src and dst are required")
	}
	settings, err := t.connection(request.Profile)
	if err != nil {
		return nil, err
	}
	if request.Key != "" {
		settings.key = request.Key
	}
	if request.Port > 0 {
		settings.port = request.Port
	}
	if request.ProxyJump != "" {
		settings.proxyJump = request.ProxyJump
	}
	cleanup, err := t.materializeRefs(&settings, request.KeyRef, request.CertRef)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	sourceRemote, targetRemote := isRemotePath(request.Src), isRemotePath(request.Dst)
	if sourceRemote == targetRemote {
		return nil, fmt.Errorf("exactly one of src and dst must be remote")
	}
	source, target := request.Src, request.Dst
	if !sourceRemote {
		source, err = t.confiner.Resolve(source)
	} else {
		target, err = t.confiner.Resolve(target)
	}
	if err != nil {
		return nil, err
	}
	args := sshOptions(settings, true)
	args = append(args, source, target)
	stdout, stderr, exitCode, err := t.runner(ctx, "scp", args, request.TimeoutMS, true)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return map[string]any{"ok": false, "stdout": stdout, "stderr": stderr, "exit_code": exitCode}, nil
	}
	localPath := source
	if sourceRemote {
		localPath = target
	}
	var bytesTransferred int64
	if info, statErr := os.Stat(localPath); statErr == nil && info.Mode().IsRegular() {
		bytesTransferred = info.Size()
	}
	return map[string]any{"ok": true, "bytes": bytesTransferred, "stdout": stdout, "stderr": stderr, "exit_code": exitCode}, nil
}

func (t *Transport) connection(name string) (connection, error) {
	if name == "" {
		return connection{}, nil
	}
	profile, ok := t.profiles[name]
	if !ok {
		return connection{}, fmt.Errorf("unknown remote profile %q", name)
	}
	remoteName := profile.Host
	if profile.User != "" {
		remoteName = profile.User + "@" + profile.Host
	}
	return connection{remote: remoteName, key: profile.KeyPath, port: profile.Port, proxyJump: profile.ProxyJump}, nil
}

func (t *Transport) materializeRefs(settings *connection, keyRef, certRef string) (func(), error) {
	var paths []string
	cleanup := func() {
		for _, path := range paths {
			if info, err := os.Stat(path); err == nil {
				_ = os.WriteFile(path, make([]byte, info.Size()), 0o600)
			}
			_ = os.Remove(path)
		}
	}
	materialize := func(name, suffix string) (string, error) {
		if name == "" {
			return "", nil
		}
		value, err := t.secrets.Resolve(name)
		if err != nil {
			return "", err
		}
		file, err := os.CreateTemp("", "light-tools-ssh-*"+suffix)
		if err != nil {
			return "", err
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return "", err
		}
		if _, err := file.WriteString(value); err != nil {
			file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		paths = append(paths, file.Name())
		return file.Name(), nil
	}
	var err error
	if keyRef != "" {
		settings.key, err = materialize(keyRef, ".key")
		if err != nil {
			cleanup()
			return cleanup, err
		}
	}
	if certRef != "" {
		settings.cert, err = materialize(certRef, ".pub")
		if err != nil {
			cleanup()
			return cleanup, err
		}
	}
	return cleanup, nil
}

func sshOptions(settings connection, scp bool) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes"}
	if settings.port > 0 {
		flag := "-p"
		if scp {
			flag = "-P"
		}
		args = append(args, flag, strconv.Itoa(settings.port))
	}
	if settings.proxyJump != "" {
		args = append(args, "-J", settings.proxyJump)
	}
	if settings.key != "" {
		args = append(args, "-i", settings.key)
	}
	if settings.cert != "" {
		args = append(args, "-o", "CertificateFile="+settings.cert)
	}
	return args
}

func isRemotePath(path string) bool {
	volume := filepath.VolumeName(path)
	if volume != "" && strings.HasPrefix(strings.TrimPrefix(path, volume), string(filepath.Separator)) {
		return false
	}
	colon := strings.IndexByte(path, ':')
	return colon > 0 && !strings.ContainsAny(path[:colon], `/\\`)
}

type runOnceFunc func(context.Context, string, []string, time.Duration) (string, string, int, bool, error)

func runCommand(ctx context.Context, executable string, args []string, timeoutMS int, retryTimeout bool) (string, string, int, error) {
	return runCommandWith(ctx, executable, args, timeoutMS, retryTimeout, runOnce)
}

func runCommandWith(ctx context.Context, executable string, args []string, timeoutMS int, retryTimeout bool, execute runOnceFunc) (string, string, int, error) {
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	stdout, stderr, exitCode, timedOut, err := execute(ctx, executable, args, timeout)
	if !retryTimeout || !timedOut || ctx.Err() != nil {
		return stdout, stderr, exitCode, err
	}
	stdout, stderr, exitCode, _, err = execute(ctx, executable, args, timeout)
	return stdout, stderr, exitCode, err
}

func runOnce(parent context.Context, executable string, args []string, timeout time.Duration) (string, string, int, bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = childenv.Minimal()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if parentErr := parent.Err(); parentErr != nil {
		return stdout.String(), stderr.String(), -1, errors.Is(parentErr, context.DeadlineExceeded), parentErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.String(), stderr.String(), -1, errors.Is(ctxErr, context.DeadlineExceeded), ctxErr
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exit.ExitCode(), false, nil
	}
	if err != nil {
		return "", "", -1, false, err
	}
	return stdout.String(), stderr.String(), 0, false, nil
}
