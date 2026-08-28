package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
)

// memSpiller stands in for the shared bash spill store. fail drives the
// fail-open path without needing to exhaust a real 64-record store.
type memSpiller struct {
	items map[string]string
	fail  bool
}

func (m *memSpiller) Store(data []byte) (string, error) {
	if m.fail {
		return "", fmt.Errorf("spill limit reached")
	}
	if m.items == nil {
		m.items = map[string]string{}
	}
	id := strconv.Itoa(len(m.items) + 1)
	m.items[id] = string(data)
	return id, nil
}

func compactionTransport(t *testing.T, stdout, stderr string, spills *memSpiller) *Transport {
	t.Helper()
	root := t.TempDir()
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var spiller interface {
		Store([]byte) (string, error)
	}
	if spills != nil {
		spiller = spills
	}
	transport := New(map[string]config.RemoteProfile{}, confiner,
		secret.New(filepath.Join(root, "secrets")), spiller, nil)
	transport.runner = func(context.Context, string, []string, int, bool) (string, string, int, error) {
		return stdout, stderr, 0, nil
	}
	return transport
}

func sshResult(t *testing.T, transport *Transport, request SSHRequest) map[string]any {
	t.Helper()
	request.Remote = "user@example"
	request.Command = "run it"
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := transport.SSH(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

func repetitiveProse(count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "fetching object %d of %d from the remote store\n", i, count)
	}
	return b.String()
}

func ndjson(count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, `{"seq":%d,"level":"info","msg":"heartbeat"}`+"\n", i)
	}
	return b.String()
}

func TestSSHCompactsRepetitiveProseUnderAuto(t *testing.T) {
	spills := &memSpiller{}
	transport := compactionTransport(t, repetitiveProse(500), "", spills)
	result := sshResult(t, transport, SSHRequest{})

	stdout, _ := result["stdout"].(string)
	if strings.Count(stdout, "fetching object") != 1 {
		t.Fatalf("repetitive remote output was not collapsed:\n%s", stdout)
	}
	spillID, _ := result["stdout_spill_id"].(string)
	if spillID == "" {
		t.Fatalf("an elided remote stream carried no recovery pointer: %#v", result)
	}
	if !strings.HasPrefix(spills.items[spillID], "fetching object 1 of 500") {
		t.Fatalf("the spill does not hold this stream from its own line 1: %q", spills.items[spillID][:40])
	}
}

// The exact-output contract light_ssh already has. Callers decode stdout, and
// an outline would break json.Unmarshal somewhere far downstream where nothing
// points back here.
func TestSSHLeavesStructuredPayloadsExactUnderAuto(t *testing.T) {
	for name, payload := range map[string]string{
		"ndjson":     ndjson(500),
		"jsonObject": `{"items":[` + strings.Repeat(`{"a":1},`, 500) + `{"a":1}]}`,
		"jsonArray":  `[` + strings.Repeat(`{"a":1},`, 500) + `{"a":1}]`,
	} {
		t.Run(name, func(t *testing.T) {
			transport := compactionTransport(t, payload, "", &memSpiller{})
			result := sshResult(t, transport, SSHRequest{})
			if got, _ := result["stdout"].(string); got != payload {
				t.Fatalf("auto compacted a structured payload; %d bytes in, %d out", len(payload), len(got))
			}
			if _, present := result["stdout_spill_id"]; present {
				t.Fatal("an exact payload emitted a recovery pointer")
			}
		})
	}
}

func TestSSHCompactValveOverridesAutoBothWays(t *testing.T) {
	off, on := false, true

	// compact:false must return prose verbatim even though auto would compact.
	prose := repetitiveProse(500)
	transport := compactionTransport(t, prose, "", &memSpiller{})
	if got, _ := sshResult(t, transport, SSHRequest{Compact: &off})["stdout"].(string); got != prose {
		t.Fatalf("compact:false did not guarantee exact output: %d bytes in, %d out", len(prose), len(got))
	}

	// compact:true must compact NDJSON that auto would have left alone.
	transport = compactionTransport(t, ndjson(500), "", &memSpiller{})
	got, _ := sshResult(t, transport, SSHRequest{Compact: &on})["stdout"].(string)
	if strings.Count(got, "heartbeat") != 1 {
		t.Fatalf("compact:true did not force compaction:\n%s", got)
	}
}

// No shared spill store means no resolvable pointer, so compaction must stand
// down entirely rather than emit an outline nothing can recover.
func TestSSHWithoutASpillStoreReturnsExactOutput(t *testing.T) {
	prose := repetitiveProse(500)
	transport := compactionTransport(t, prose, "", nil)
	result := sshResult(t, transport, SSHRequest{})
	if got, _ := result["stdout"].(string); got != prose {
		t.Fatalf("compaction ran with no spill store behind it: %d bytes out", len(got))
	}
	if _, present := result["stdout_spill_id"]; present {
		t.Fatal("a pointer was emitted with no store to resolve it")
	}
}

// Fail-open: a store that refuses must yield exact output, never an outline
// whose pointer does not resolve.
func TestSSHFailedSpillFallsBackToExactOutput(t *testing.T) {
	prose := repetitiveProse(500)
	transport := compactionTransport(t, prose, "", &memSpiller{fail: true})
	result := sshResult(t, transport, SSHRequest{})
	if got, _ := result["stdout"].(string); got != prose {
		t.Fatalf("a failed spill did not fail open: %d bytes out", len(got))
	}
	if _, present := result["stdout_spill_id"]; present {
		t.Fatal("a pointer was emitted for a spill that failed")
	}
	if result["stdout_compaction_skipped"] != true {
		t.Fatalf("fail-open was not reported: %#v", result)
	}
}

func TestSSHStreamsSpillSeparately(t *testing.T) {
	spills := &memSpiller{}
	var errOut strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&errOut, "warning %d: retrying\n", i)
	}
	transport := compactionTransport(t, repetitiveProse(300), errOut.String(), spills)
	result := sshResult(t, transport, SSHRequest{})

	outID, _ := result["stdout_spill_id"].(string)
	errID, _ := result["stderr_spill_id"].(string)
	if outID == "" || errID == "" || outID == errID {
		t.Fatalf("streams did not spill independently: %#v", result)
	}
	if !strings.HasPrefix(spills.items[outID], "fetching object 1") {
		t.Fatal("the stdout spill does not start at stdout's line 1")
	}
	if !strings.HasPrefix(spills.items[errID], "warning 1") {
		t.Fatal("the stderr spill does not start at stderr's line 1")
	}
}

func TestSSHNoCompactEnvReturnsExactOutput(t *testing.T) {
	t.Setenv("LIGHT_NO_COMPACT", "1")
	prose := repetitiveProse(500)
	transport := compactionTransport(t, prose, "", &memSpiller{})
	result := sshResult(t, transport, SSHRequest{})
	if got, _ := result["stdout"].(string); got != prose {
		t.Fatalf("the escape hatch did not return exact output: %d bytes out", len(got))
	}
	for _, key := range []string{"stdout_spill_id", "stdout_recover", "stdout_compaction_skipped"} {
		if _, present := result[key]; present {
			t.Fatalf("escape hatch leaked compaction key %q", key)
		}
	}
}
