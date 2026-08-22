package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
)

func TestSSHAndSCPOptionParity(t *testing.T) {
	settings := connection{
		remote: "deploy@example", key: "/keys/id", cert: "/keys/id-cert.pub",
		port: 2222, proxyJump: "jump@example",
	}
	sshWant := []string{
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-p", "2222", "-J", "jump@example", "-i", "/keys/id",
		"-o", "CertificateFile=/keys/id-cert.pub",
	}
	scpWant := append([]string(nil), sshWant...)
	scpWant[4] = "-P"
	if got := sshOptions(settings, false); !reflect.DeepEqual(got, sshWant) {
		t.Fatalf("ssh options\n got: %#v\nwant: %#v", got, sshWant)
	}
	if got := sshOptions(settings, true); !reflect.DeepEqual(got, scpWant) {
		t.Fatalf("scp options\n got: %#v\nwant: %#v", got, scpWant)
	}
}

func TestConnectionProfileAndSecureRefs(t *testing.T) {
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	if err := vault.Set("private-key", "PRIVATE KEY BYTES"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("certificate", "CERTIFICATE BYTES"); err != nil {
		t.Fatal(err)
	}
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := New(map[string]config.RemoteProfile{
		"prod": {Host: "example", User: "deploy", Port: 2222, ProxyJump: "jump", KeyPath: "/keys/id"},
	}, confiner, vault)
	settings, err := transport.connection("prod")
	if err != nil {
		t.Fatal(err)
	}
	if settings.remote != "deploy@example" || settings.port != 2222 || settings.proxyJump != "jump" || settings.key != "/keys/id" {
		t.Fatalf("profile lost fields: %#v", settings)
	}

	cleanup, err := transport.materializeRefs(&settings, "private-key", "certificate")
	if err != nil {
		t.Fatal(err)
	}
	keyPath, certPath := settings.key, settings.cert
	for _, path := range []string{keyPath, certPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("secret ref mode = %o", info.Mode().Perm())
		}
	}
	key, _ := os.ReadFile(keyPath)
	cert, _ := os.ReadFile(certPath)
	if string(key) != "PRIVATE KEY BYTES" || string(cert) != "CERTIFICATE BYTES" {
		t.Fatal("materialized secret contents differ")
	}
	cleanup()
	for _, path := range []string{keyPath, certPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary secret survived cleanup: %s", path)
		}
	}
}

func TestRemotePathDirectionInference(t *testing.T) {
	cases := map[string]bool{
		"host:/tmp/a":        true,
		"user@host:relative": true,
		"/tmp/a:b":           false,
		"relative/path":      false,
	}
	if runtime.GOOS == "windows" {
		cases[`C:\tmp\a`] = false
	}
	for path, want := range cases {
		if got := isRemotePath(path); got != want {
			t.Errorf("isRemotePath(%q) = %t, want %t", path, got, want)
		}
	}
}

func TestRunOnceUsesMinimalEnvironmentAndRealProcess(t *testing.T) {
	t.Setenv("LIGHT_TOOLS_REMOTE_CANARY", "must-not-leak")
	stdout, stderr, exitCode, timedOut, err := runOnce(
		context.Background(),
		os.Args[0],
		[]string{"-test.run=^TestRemoteHelperProcess$", "helper:environment"},
		10*time.Second,
	)
	if err != nil || timedOut || exitCode != 0 {
		t.Fatalf("helper failed: stdout=%q stderr=%q exit=%d timeout=%t err=%v", stdout, stderr, exitCode, timedOut, err)
	}
	if !strings.Contains(stdout, "path=true") {
		t.Fatalf("allowlisted PATH did not reach helper: %q", stdout)
	}
	if strings.Contains(stdout, "must-not-leak") {
		t.Fatalf("parent-only canary reached helper: %q", stdout)
	}
}

func TestRunOnceReturnsProgramExitAndContextErrorsHonestly(t *testing.T) {
	t.Run("nonzero", func(t *testing.T) {
		stdout, stderr, exitCode, timedOut, err := runOnce(
			context.Background(),
			os.Args[0],
			[]string{"-test.run=^TestRemoteHelperProcess$", "helper:nonzero"},
			10*time.Second,
		)
		if err != nil || timedOut || exitCode != 7 || stdout != "remote-stdout" || stderr != "remote-stderr" {
			t.Fatalf("nonzero result drifted: stdout=%q stderr=%q exit=%d timeout=%t err=%v", stdout, stderr, exitCode, timedOut, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		_, _, exitCode, timedOut, err := runOnce(
			context.Background(),
			os.Args[0],
			[]string{"-test.run=^TestRemoteHelperProcess$", "helper:sleep"},
			50*time.Millisecond,
		)
		if !errors.Is(err, context.DeadlineExceeded) || !timedOut || exitCode != -1 {
			t.Fatalf("timeout masqueraded as program exit: exit=%d timeout=%t err=%v", exitCode, timedOut, err)
		}
	})
	t.Run("parent cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, exitCode, timedOut, err := runOnce(
			ctx,
			os.Args[0],
			[]string{"-test.run=^TestRemoteHelperProcess$", "helper:sleep"},
			10*time.Second,
		)
		if !errors.Is(err, context.Canceled) || timedOut || exitCode != -1 {
			t.Fatalf("cancellation masqueraded as program exit: exit=%d timeout=%t err=%v", exitCode, timedOut, err)
		}
	})
}

func TestOperationAwareTimeoutRetry(t *testing.T) {
	newExecute := func(calls *int) runOnceFunc {
		return func(context.Context, string, []string, time.Duration) (string, string, int, bool, error) {
			*calls++
			if *calls == 1 {
				return "partial", "timed out", -1, true, context.DeadlineExceeded
			}
			return "complete", "", 0, false, nil
		}
	}
	t.Run("ssh at most once", func(t *testing.T) {
		calls := 0
		stdout, _, _, err := runCommandWith(context.Background(), "ssh", nil, 10, false, newExecute(&calls))
		if calls != 1 || stdout != "partial" || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("SSH retry contract drifted: calls=%d stdout=%q err=%v", calls, stdout, err)
		}
	})
	t.Run("scp retries overwrite once", func(t *testing.T) {
		calls := 0
		stdout, _, exitCode, err := runCommandWith(context.Background(), "scp", nil, 10, true, newExecute(&calls))
		if calls != 2 || stdout != "complete" || exitCode != 0 || err != nil {
			t.Fatalf("SCP retry contract drifted: calls=%d stdout=%q exit=%d err=%v", calls, stdout, exitCode, err)
		}
	})
	t.Run("canceled parent prevents retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, _, _, _ = runCommandWith(ctx, "scp", nil, 10, true, newExecute(&calls))
		if calls != 1 {
			t.Fatalf("canceled SCP retried %d times", calls)
		}
	})
}

func TestTransportRunnerSeamCapturesSSHAndSCPContracts(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "artifact.bin")
	if err := os.WriteFile(local, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := New(map[string]config.RemoteProfile{
		"base": {Host: "profile.example", User: "profile", Port: 22, ProxyJump: "old-jump", KeyPath: "/old/key"},
	}, confiner, secret.New(filepath.Join(root, "secrets")))

	type invocation struct {
		executable string
		args       []string
		retry      bool
	}
	var calls []invocation
	transport.runner = func(_ context.Context, executable string, args []string, _ int, retry bool) (string, string, int, error) {
		calls = append(calls, invocation{executable: executable, args: append([]string(nil), args...), retry: retry})
		return "", "", 0, nil
	}

	sshRaw, _ := json.Marshal(SSHRequest{
		Profile: "base", Remote: "override@example", Command: "deploy once",
		Key: "/override/key", Port: 2200, ProxyJump: "jump@example",
	})
	if _, err := transport.SSH(context.Background(), sshRaw); err != nil {
		t.Fatal(err)
	}
	sshWant := []string{
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-p", "2200", "-J", "jump@example", "-i", "/override/key",
		"override@example", "deploy once",
	}
	if len(calls) != 1 || calls[0].executable != "ssh" || calls[0].retry || !reflect.DeepEqual(calls[0].args, sshWant) {
		t.Fatalf("SSH invocation drifted: %#v", calls)
	}

	scpRaw, _ := json.Marshal(SCPRequest{Profile: "base", Src: local, Dst: "deploy@example:/tmp/artifact", Port: 2222})
	value, err := transport.SCP(context.Background(), scpRaw)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	if result["ok"] != true || result["bytes"] != int64(len("payload")) {
		t.Fatalf("SCP accounting drifted: %#v", result)
	}
	if len(calls) != 2 || calls[1].executable != "scp" || !calls[1].retry {
		t.Fatalf("SCP retry mode drifted: %#v", calls)
	}
	resolvedLocal, err := confiner.Resolve(local)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls[1].args; len(got) < 2 || got[len(got)-2] != resolvedLocal || got[len(got)-1] != "deploy@example:/tmp/artifact" {
		t.Fatalf("SCP direction or path drifted: %#v", calls[1].args)
	}

	outside := filepath.Join(filepath.Dir(root), "outside-artifact")
	outsideRaw, _ := json.Marshal(SCPRequest{Src: outside, Dst: "deploy@example:/tmp/outside"})
	if _, err := transport.SCP(context.Background(), outsideRaw); err == nil {
		t.Fatal("SCP accepted a local source outside the configured roots")
	}
	if len(calls) != 2 {
		t.Fatalf("confined SCP reached runner: %#v", calls)
	}
}

func TestRemoteHelperProcess(t *testing.T) {
	if len(os.Args) == 0 || !strings.HasPrefix(os.Args[len(os.Args)-1], "helper:") {
		return
	}
	switch strings.TrimPrefix(os.Args[len(os.Args)-1], "helper:") {
	case "environment":
		fmt.Printf("path=%t canary=%s", os.Getenv("PATH") != "", os.Getenv("LIGHT_TOOLS_REMOTE_CANARY"))
	case "nonzero":
		fmt.Fprint(os.Stdout, "remote-stdout")
		fmt.Fprint(os.Stderr, "remote-stderr")
		os.Exit(7)
	case "sleep":
		time.Sleep(5 * time.Second)
	default:
		os.Exit(9)
	}
}
