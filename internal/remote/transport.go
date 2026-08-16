package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/security"
)

type SSHRequest struct {
	Profile   string `json:"profile"`
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type SCPRequest struct {
	Profile   string `json:"profile"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Direction string `json:"direction,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type Transport struct {
	profiles map[string]config.RemoteProfile
	roots    []string
}

func New(profiles map[string]config.RemoteProfile, roots []string) *Transport {
	return &Transport{profiles: profiles, roots: roots}
}

func (t *Transport) SSH(ctx context.Context, raw json.RawMessage) (any, error) {
	var request SSHRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	profile, err := t.profile(request.Profile)
	if err != nil {
		return nil, err
	}
	args := sshArgs(profile)
	args = append(args, destination(profile), request.Command)
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
	profile, err := t.profile(request.Profile)
	if err != nil {
		return nil, err
	}
	args := sshOptions(profile)
	if request.Recursive {
		args = append(args, "-r")
	}
	source, target := request.Source, request.Target
	if request.Direction == "download" {
		target, err = security.ResolveBeneath(target, t.roots)
		if err != nil {
			return nil, err
		}
		source = destination(profile) + ":" + source
	} else {
		source, err = security.ResolveBeneath(source, t.roots)
		if err != nil {
			return nil, err
		}
		target = destination(profile) + ":" + target
	}
	args = append(args, source, target)
	stdout, stderr, exitCode, err := runRetryTimeout(ctx, "scp", args, request.TimeoutMS)
	if err != nil {
		return nil, err
	}
	return map[string]any{"stdout": stdout, "stderr": stderr, "exit_code": exitCode}, nil
}

func (t *Transport) profile(name string) (config.RemoteProfile, error) {
	profile, ok := t.profiles[name]
	if !ok {
		return config.RemoteProfile{}, fmt.Errorf("unknown remote profile %q", name)
	}
	if profile.Host == "" {
		return config.RemoteProfile{}, fmt.Errorf("remote profile %q has no host", name)
	}
	return profile, nil
}

func sshArgs(profile config.RemoteProfile) []string {
	return sshOptions(profile)
}

func sshOptions(profile config.RemoteProfile) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes"}
	if profile.Port > 0 {
		args = append(args, "-p", strconv.Itoa(profile.Port))
	}
	if profile.ProxyJump != "" {
		args = append(args, "-J", profile.ProxyJump)
	}
	if profile.KeyPath != "" {
		args = append(args, "-i", profile.KeyPath)
	}
	return args
}

func destination(profile config.RemoteProfile) string {
	if profile.User == "" {
		return profile.Host
	}
	return profile.User + "@" + profile.Host
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
