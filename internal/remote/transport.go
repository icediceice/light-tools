package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
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

type Transport struct {
	profiles map[string]config.RemoteProfile
	confiner *security.Confiner
	secrets  *secret.Vault
}

func New(profiles map[string]config.RemoteProfile, confiner *security.Confiner, secrets *secret.Vault) *Transport {
	return &Transport{profiles: profiles, confiner: confiner, secrets: secrets}
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
	stdout, stderr, exitCode, err := runRetryTimeout(ctx, "ssh", args, request.TimeoutMS)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": exitCode}, nil
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
	stdout, stderr, exitCode, err := runRetryTimeout(ctx, "scp", args, request.TimeoutMS)
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
	return colon > 0 && !strings.Contains(path[:colon], string(filepath.Separator))
}

func runRetryTimeout(ctx context.Context, executable string, args []string, timeoutMS int) (string, string, int, error) {
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	for attempt := 0; attempt < 2; attempt++ {
		stdout, stderr, exitCode, timedOut, err := runOnce(ctx, executable, args, timeout)
		if !timedOut || attempt == 1 {
			return stdout, stderr, exitCode, err
		}
	}
	panic("unreachable")
}

func runOnce(parent context.Context, executable string, args []string, timeout time.Duration) (string, string, int, bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = inheritedRemoteEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), -1, true, ctx.Err()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exit.ExitCode(), false, nil
	}
	if err != nil {
		return "", "", -1, false, err
	}
	return stdout.String(), stderr.String(), 0, false, nil
}

func inheritedRemoteEnvironment() []string {
	var result []string
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "PATH=") || strings.HasPrefix(item, "HOME=") || strings.HasPrefix(item, "SSH_AUTH_SOCK=") || strings.HasPrefix(item, "SYSTEMROOT=") {
			result = append(result, item)
		}
	}
	return result
}
