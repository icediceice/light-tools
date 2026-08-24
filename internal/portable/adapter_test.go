package portable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// demoSchema is a minimal verb-dispatching schema: "verb" lets Invoke attribute
// the call, and "limit" gives both a coercion failure and a rename target.
func demoSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verb":  map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer"},
		},
		"additionalProperties": false,
	}
}

func demoTool(handler Handler) Tool {
	return Tool{Name: "light_demo", InputSchema: demoSchema(), Handler: handler}
}

func failingWith(err error) Handler {
	return func(context.Context, json.RawMessage) (any, error) { return nil, err }
}

func invokeErr(t *testing.T, tool Tool, input string) string {
	t.Helper()
	_, err := Invoke(context.Background(), tool, json.RawMessage(input))
	if err == nil {
		t.Fatal("expected an error from Invoke")
	}
	return err.Error()
}

func mustContain(t *testing.T, rendered string, expected ...string) {
	t.Helper()
	for _, want := range expected {
		if !strings.Contains(rendered, want) {
			t.Fatalf("envelope missing %q:\n%s", want, rendered)
		}
	}
}

// A handler that returns a plain error still reaches the caller as a full
// envelope, attributed to the tool/verb pair that produced it.
func TestInvokeWrapsPlainHandlerErrorAsAttributedEnvelope(t *testing.T) {
	rendered := invokeErr(t, demoTool(failingWith(errors.New("disk offline"))), `{"verb":"read"}`)
	mustContain(t, rendered,
		"error[E_TOOL]",
		"at: light_demo/read",
		"fix: read detail, then adjust the call",
		"detail: disk offline",
	)
}

// A schema refusal names the offending field rooted at the tool, not at the
// anonymous "$" the validator speaks in.
func TestInvokeRootsSchemaPathAtTheTool(t *testing.T) {
	handler := func(context.Context, json.RawMessage) (any, error) { return "unreached", nil }
	rendered := invokeErr(t, demoTool(handler), `{"verb":"read","limit":"abc"}`)
	mustContain(t, rendered,
		"error[E_SCHEMA]",
		"at: light_demo.limit",
		"fix: correct the call arguments and retry",
	)
	if strings.Contains(rendered, "at: $") {
		t.Fatalf("schema path was left un-rooted:\n%s", rendered)
	}
}

// A producer that knows the real remedy outranks the code's default phrase.
func TestExplicitFixWinsOverDefaultFix(t *testing.T) {
	explicit := &DiagnosticError{Code: "E_USAGE", Fix: "pass force:true to overwrite", Message: "destination exists"}
	rendered := invokeErr(t, demoTool(failingWith(explicit)), `{"verb":"write"}`)
	mustContain(t, rendered, "fix: pass force:true to overwrite")
	if strings.Contains(rendered, defaultFix("E_USAGE")) {
		t.Fatalf("default fix overrode the producer's own:\n%s", rendered)
	}
}

// Producers hand back long-lived diagnostics. Decorating one must not let the
// first call's attribution bleed into the second.
func TestDecorationCopiesSharedDiagnosticOnWrite(t *testing.T) {
	shared := &DiagnosticError{Code: "E_USAGE", Message: "path is required"}
	before := *shared

	first := invokeErr(t, Tool{Name: "light_file", InputSchema: demoSchema(), Handler: failingWith(shared)}, `{"verb":"read"}`)
	second := invokeErr(t, Tool{Name: "light_ops", InputSchema: demoSchema(), Handler: failingWith(shared)}, `{"verb":"probe_port"}`)

	mustContain(t, first, "at: light_file/read")
	mustContain(t, second, "at: light_ops/probe_port")
	if *shared != before {
		t.Fatalf("decoration mutated the shared diagnostic: %#v became %#v", before, *shared)
	}
}

// errors.As unwraps to the inner diagnostic and drops the wrapper's words. The
// prefix has to survive without nesting a second rendered envelope in detail.
func TestOuterWrapperPrefixSurvivesWithoutNesting(t *testing.T) {
	inner := &DiagnosticError{Code: "E_SYS", Message: "permission denied"}
	wrapped := fmt.Errorf("snapshot commit failed: %w", inner)
	rendered := invokeErr(t, demoTool(failingWith(wrapped)), `{"verb":"write"}`)

	mustContain(t, rendered,
		"error[E_SYS]",
		"detail: snapshot commit failed: permission denied",
		"fix: a platform fault, not a bad call — retry, and report it if it persists",
	)
	if count := strings.Count(rendered, "error["); count != 1 {
		t.Fatalf("expected exactly one envelope, got %d:\n%s", count, rendered)
	}
}

// Warnings ride the success path only. A call that was repaired and then failed
// must still tell the caller what was repaired.
func TestRepairWarningSurvivesAFailingCall(t *testing.T) {
	rendered := invokeErr(t, demoTool(failingWith(errors.New("upstream refused"))), `{"verb":"read","lmit":5}`)
	mustContain(t, rendered,
		"detail: upstream refused",
		"repairs applied",
		`light_demo: renamed "lmit" to "limit"`,
	)
}

// A missing handler is a platform fault, so it must not tell the caller to go
// fix their arguments.
func TestInternalCodeSelectsTheSystemFixPhrase(t *testing.T) {
	_, err := Invoke(context.Background(), Tool{Name: "light_demo", InputSchema: demoSchema()}, json.RawMessage(`{"verb":"read"}`))
	if err == nil {
		t.Fatal("expected a missing-handler error")
	}
	rendered := err.Error()
	mustContain(t, rendered,
		"error[E_INTERNAL]",
		"at: light_demo",
		"fix: a platform fault, not a bad call — retry, and report it if it persists",
	)
}
