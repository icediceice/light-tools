package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func callOps(t *testing.T, handler *Handler, request any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	value, err := handler.Handle(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result %#v", value)
	}
	return result
}

// callOpsErr returns the error instead of failing, for refusal assertions.
func callOpsErr(t *testing.T, handler *Handler, request any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return handler.Handle(context.Background(), raw)
}

func testLogHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()
	root := t.TempDir()
	api := filepath.Join(root, "api.log")
	worker := filepath.Join(root, "worker.log")
	os.WriteFile(api, []byte(strings.Join([]string{
		"2026-08-16T10:00:00Z INFO boot",
		"2026-08-16T11:00:00Z ERROR request_id=trace-123456789 failed",
		"2026-08-16T11:00:02Z INFO recovered",
	}, "\n")+"\n"), 0o600)
	os.WriteFile(worker, []byte(strings.Join([]string{
		"2026-08-16T10:30:00Z INFO idle",
		"2026-08-16T11:00:01Z WARN request_id=trace-123456789 retry",
	}, "\n")+"\n"), 0o600)
	handler, err := New([]string{root}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler.registry.services = []Service{
		{ID: "pm2:api", Source: "pm2", Name: "api", OutLog: api},
		{ID: "pm2:worker", Source: "pm2", Name: "worker", OutLog: worker},
	}
	handler.registry.updated = time.Now()
	return handler, api, worker
}

func TestFileLogFiltersAndSince(t *testing.T) {
	handler, api, _ := testLogHandler(t)
	result := callOps(t, handler, map[string]any{
		"verb": "log_search", "path": api, "pattern": "ERROR",
		"include": "request_id", "exclude": "ignored", "context": 1,
	})
	content := result["content"].(string)
	if !strings.Contains(content, "ERROR request_id") || !strings.Contains(content, "INFO recovered") {
		t.Fatalf("context/filter result incomplete: %s", content)
	}

	result = callOps(t, handler, map[string]any{
		"verb": "log_since", "path": api, "since_ts": "2026-08-16T10:30:00Z",
	})
	content = result["content"].(string)
	if strings.Contains(content, "INFO boot") || !strings.Contains(content, "ERROR request_id") {
		t.Fatalf("since_ts was not applied: %s", content)
	}
}

func TestPoolGrepCorrelationAndInvestigation(t *testing.T) {
	handler, api, worker := testLogHandler(t)
	grep := callOps(t, handler, map[string]any{
		"verb": "log_grep", "pattern": "trace-123456789",
	})
	encodedRows, _ := json.Marshal(grep["services"])
	var rows []map[string]any
	if err := json.Unmarshal(encodedRows, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("pool grep returned %#v", rows)
	}

	correlated := callOps(t, handler, map[string]any{
		"verb": "log_correlate", "services": []string{"file:" + worker, "file:" + api},
		"pattern": "trace-123456789",
	})
	timeline := correlated["timeline"].(string)
	first := strings.Index(timeline, "11:00:00Z")
	second := strings.Index(timeline, "11:00:01Z")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("timeline is not timestamp ordered: %s", timeline)
	}

	investigation := callOps(t, handler, map[string]any{
		"verb": "log_investigate", "pattern": "ERROR",
	})
	identifiers := investigation["identifiers"].([]string)
	foundTrace := false
	for _, identifier := range identifiers {
		if identifier == "trace-123456789" {
			foundTrace = true
		}
	}
	if !foundTrace {
		t.Fatalf("identifier extraction failed: %#v", identifiers)
	}
	traces := investigation["traces"].(map[string]any)
	if _, ok := traces["trace-123456789"]; !ok {
		t.Fatalf("identifier trace missing: %#v", traces)
	}
}

func TestAsyncLocalLogLifecycle(t *testing.T) {
	handler, api, _ := testLogHandler(t)
	started := callOps(t, handler, map[string]any{
		"verb": "log_search", "path": api, "pattern": "ERROR", "async": true,
	})
	id := started["task_id"].(string)
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := callOps(t, handler, map[string]any{"verb": "status", "task_id": id})
		if status["status"] == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("async task did not finish: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
	collected := callOps(t, handler, map[string]any{"verb": "collect", "task_id": id})
	if collected["status"] != "done" || collected["result"] == nil {
		t.Fatalf("collect failed: %#v", collected)
	}
}
