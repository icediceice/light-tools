package childenv

import (
	"strings"
	"testing"
)

func TestFilterNeverReturnsNil(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			if got := Filter(nil, goos); got == nil {
				t.Fatal("Filter returned nil; assigning nil to exec.Cmd.Env inherits the full parent environment")
			}
		})
	}
}

func TestFilterWindowsFoldsCasePreservesSpellingAndStaysMinimal(t *testing.T) {
	expected := []string{
		`Path=C:\Program Files\Go\bin;C:\Users\a b\bin`,
		`PATHEXT=.COM;.EXE;.BAT;.CMD`,
		`SystemRoot=C:\Windows`,
		`windir=C:\Windows`,
		`ComSpec=C:\Windows\System32\cmd.exe`,
		`USERPROFILE=C:\Users\runner`,
		`APPDATA=C:\Users\runner\AppData\Roaming`,
		`LOCALAPPDATA=C:\Users\runner\AppData\Local`,
		`SystemDrive=C:`,
		`ProgramData=C:\ProgramData`,
		`PSModulePath=C:\Windows\System32\WindowsPowerShell\v1.0\Modules`,
		`TEMP=C:\Users\runner\AppData\Local\Temp`,
		`TMP=C:\Users\runner\AppData\Local\Temp`,
	}
	entries := append([]string{}, expected...)
	entries = append(entries, `AWS_SECRET_ACCESS_KEY=must-not-leak`, `=C:=C:\work`)

	got := Filter(entries, "windows")
	for _, want := range expected {
		if !containsEntry(got, want) {
			t.Fatalf("allowed Windows entry was not preserved verbatim: want=%q got=%#v", want, got)
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("Windows filter retained entries outside the allowlist: %#v", got)
	}
	for _, item := range got {
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" {
			t.Fatalf("invalid or per-drive environment entry survived: %q", item)
		}
		if strings.EqualFold(name, "AWS_SECRET_ACCESS_KEY") {
			t.Fatalf("unauthorized environment variable survived: %q", item)
		}
	}
}

func TestFilterPosixStaysExactCase(t *testing.T) {
	got := Filter([]string{"Path=/nope", "PATH=/usr/bin", "HOME=/home/runner", "SECRET=x"}, "linux")
	if !containsEntry(got, "PATH=/usr/bin") || !containsEntry(got, "HOME=/home/runner") {
		t.Fatalf("allowed POSIX entries missing: %#v", got)
	}
	if containsEntry(got, "Path=/nope") || containsEntry(got, "SECRET=x") {
		t.Fatalf("POSIX filtering became case-insensitive or leaked a secret: %#v", got)
	}
}

func TestFilterSuppliesPATHEXTDefault(t *testing.T) {
	got := Filter([]string{`Path=C:\Go\bin`}, "windows")
	if !containsEntry(got, "PATHEXT=.COM;.EXE;.BAT;.CMD") {
		t.Fatalf("PATHEXT default missing: %#v", got)
	}
}

func containsEntry(entries []string, want string) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
}
