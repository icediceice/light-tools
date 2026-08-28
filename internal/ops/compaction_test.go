package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// memSpiller stands in for the shared bash spill store.
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

// repetitiveLogHandler writes a log whose lines differ only by a climbing
// counter — the shape a real restart loop or retry storm produces.
func repetitiveLogHandler(t *testing.T, spills *memSpiller, lines int) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "noisy.log")

	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "2026-08-16T10:00:00Z WARN connection to upstream refused, retry %d\n", i)
	}
	b.WriteString("2026-08-16T11:00:00Z FATAL giving up after too many retries\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	var spiller interface {
		Store([]byte) (string, error)
	}
	if spills != nil {
		spiller = spills
	}
	handler, err := New(testPolicy(root), nil, spiller, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler.registry.services = []Service{{ID: "pm2:noisy", Source: "pm2", Name: "noisy", OutLog: path}}
	handler.registry.updated = time.Now()
	return handler, path
}

func opsResult(t *testing.T, handler *Handler, request map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := handler.Handle(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]any)
}

// capOutput used to elide with a bare capped:true and NO recovery path at all.
// Compaction lands at that same chokepoint and brings a pointer with it.
func TestOpsLogsCompactAndCarryARecoveryPointer(t *testing.T) {
	spills := &memSpiller{}
	handler, path := repetitiveLogHandler(t, spills, 500)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if strings.Count(content, "connection to upstream refused") != 1 {
		t.Fatalf("repetitive log lines were not collapsed:\n%s", content)
	}
	spillID, _ := result["spill_id"].(string)
	if spillID == "" {
		t.Fatalf("an elided ops body carried no recovery pointer: %#v", result)
	}
	if hint, _ := result["recover"].(string); !strings.Contains(hint, spillID) {
		t.Fatalf("recovery hint does not name its own spill: %q", hint)
	}
	if !strings.Contains(spills.items[spillID], "retry 500") {
		t.Fatal("the spill does not hold the text the outline indexes")
	}
}

// A verdict occurs once. Collapsing the 500 retries must not take the FATAL
// line with them — that line is the reason anyone opened the log.
func TestOpsKeepsTheLoneVerdictLine(t *testing.T) {
	handler, path := repetitiveLogHandler(t, &memSpiller{}, 500)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if !strings.Contains(content, "giving up after too many retries") {
		t.Fatalf("the lone FATAL line was summarised away:\n%s", content)
	}
}

// The climbing counter is what a one-line-per-kind summary hides, and what the
// template slot exists to surface.
func TestOpsSurfacesTheVaryingCounter(t *testing.T) {
	handler, path := repetitiveLogHandler(t, &memSpiller{}, 500)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if !strings.Contains(content, "1..500") {
		t.Fatalf("the retry counter range never reached the view:\n%s", content)
	}
}

// Fail-open at the ops seam too: no resolvable pointer means no outline.
func TestOpsFailedSpillFallsBackToExactOutput(t *testing.T) {
	handler, path := repetitiveLogHandler(t, &memSpiller{fail: true}, 500)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if strings.Count(content, "connection to upstream refused") != 500 {
		t.Fatalf("a failed spill did not fail open to exact output (%d lines kept)",
			strings.Count(content, "connection to upstream refused"))
	}
	if _, present := result["spill_id"]; present {
		t.Fatal("a pointer was emitted for a spill that failed")
	}
	if result["compaction_skipped"] != true {
		t.Fatalf("fail-open was not reported: %#v", result)
	}
}

func TestOpsNoCompactEnvReturnsExactOutput(t *testing.T) {
	t.Setenv("LIGHT_NO_COMPACT", "1")
	handler, path := repetitiveLogHandler(t, &memSpiller{}, 500)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if strings.Count(content, "connection to upstream refused") != 500 {
		t.Fatalf("the escape hatch did not return all 500 lines (got %d)",
			strings.Count(content, "connection to upstream refused"))
	}
	for _, key := range []string{"spill_id", "recover", "compaction_skipped"} {
		if _, present := result[key]; present {
			t.Fatalf("escape hatch leaked compaction key %q", key)
		}
	}
}

// Small output must pass through untouched: minting a spill record for a log
// the reader can already read costs a slot from a 64-record budget.
func TestOpsSmallLogPassesThroughUntouched(t *testing.T) {
	spills := &memSpiller{}
	handler, path := repetitiveLogHandler(t, spills, 3)
	result := opsResult(t, handler, map[string]any{"verb": "log_window", "path": path, "lines": 600})

	content, _ := result["content"].(string)
	if strings.Count(content, "connection to upstream refused") != 3 {
		t.Fatalf("a small log was rewritten:\n%s", content)
	}
	if len(spills.items) != 0 {
		t.Fatalf("a spill record was minted for output that did not need one: %d", len(spills.items))
	}
}
