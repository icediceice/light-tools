package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

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
	transport := New(map[string]config.RemoteProfile{
		"prod": {Host: "example", User: "deploy", Port: 2222, ProxyJump: "jump", KeyPath: "/keys/id"},
	}, []string{root}, vault)
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
