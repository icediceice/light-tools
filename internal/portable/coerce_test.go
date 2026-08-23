package portable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fileSchema mirrors the real light_file surface closely enough to exercise the
// repair pass: a verb, a handful of fields whose names models reliably
// near-miss, and additionalProperties:false — the setting that turns a one-
// character typo into a hard refusal without this layer.
func fileSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verb":       map[string]any{"type": "string"},
			"path":       map[string]any{"type": "string"},
			"offset":     map[string]any{"type": "integer"},
			"limit":      map[string]any{"type": "integer"},
			"pattern":    map[string]any{"type": "string"},
			"capture_id": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func repair(t *testing.T, tool string, raw string) (map[string]any, []string) {
	t.Helper()
	repaired, warnings, err := Repair(tool, fileSchema(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("Repair returned an error for %s: %v", raw, err)
	}
	var object map[string]any
	if err := json.Unmarshal(repaired, &object); err != nil {
		t.Fatalf("repaired payload is not an object: %v", err)
	}
	return object, warnings
}

func warningMentioning(warnings []string, fragments ...string) string {
	for _, warning := range warnings {
		matched := true
		for _, fragment := range fragments {
			if !strings.Contains(warning, fragment) {
				matched = false
				break
			}
		}
		if matched {
			return warning
		}
	}
	return ""
}

func TestVerbAliasIsFoldedAndReported(t *testing.T) {
	for _, alias := range []string{"cmd", "action", "op", "mode"} {
		object, warnings := repair(t, "light_file", `{"`+alias+`":"read","path":"/tmp/x"}`)
		if object["verb"] != "read" {
			t.Fatalf("alias %q was not folded onto verb: %#v", alias, object)
		}
		if _, lingering := object[alias]; lingering {
			t.Fatalf("alias %q survived the fold: %#v", alias, object)
		}
		if warningMentioning(warnings, alias, "verb") == "" {
			t.Fatalf("alias %q was folded silently; warnings=%v", alias, warnings)
		}
	}
}

func TestExistingVerbBeatsAnAlias(t *testing.T) {
	object, _ := repair(t, "light_file", `{"verb":"read","cmd":"write","path":"/tmp/x"}`)
	if object["verb"] != "read" {
		t.Fatalf("an explicit verb must win over an alias, got %#v", object)
	}
}

func TestNearMissFieldNameIsRepairedAndReported(t *testing.T) {
	object, warnings := repair(t, "light_file", `{"verb":"read","pth":"/tmp/x","ofset":3}`)
	if object["path"] != "/tmp/x" {
		t.Fatalf("pth was not repaired to path: %#v", object)
	}
	if object["offset"] == nil {
		t.Fatalf("ofset was not repaired to offset: %#v", object)
	}
	if warningMentioning(warnings, `"pth"`, `"path"`) == "" {
		t.Fatalf("the path repair was silent; warnings=%v", warnings)
	}
}

func TestUnknownFieldIsDroppedWithADidYouMean(t *testing.T) {
	object, warnings := repair(t, "light_file", `{"verb":"read","path":"/tmp/x","capture_di":"abc"}`)
	if _, lingering := object["capture_di"]; lingering {
		t.Fatalf("the unknown field survived: %#v", object)
	}
	// Distance 2 on an 10-char key is inside the threshold, so this one is a
	// rename rather than a drop — either way the model must be told.
	if object["capture_id"] != "abc" && warningMentioning(warnings, "capture_di") == "" {
		t.Fatalf("capture_di was neither repaired nor reported; object=%#v warnings=%v", object, warnings)
	}
}

func TestFarUnknownFieldIsDroppedNotGuessed(t *testing.T) {
	object, warnings := repair(t, "light_file", `{"verb":"read","path":"/tmp/x","recursive_depth":4}`)
	if _, lingering := object["recursive_depth"]; lingering {
		t.Fatalf("the unknown field survived: %#v", object)
	}
	for _, declared := range []string{"offset", "limit", "pattern", "capture_id"} {
		if _, invented := object[declared]; invented {
			t.Fatalf("recursive_depth was guessed onto %q: %#v", declared, object)
		}
	}
	if warningMentioning(warnings, "recursive_depth", "dropped") == "" {
		t.Fatalf("the drop was silent; warnings=%v", warnings)
	}
}

func TestSafeVerbIsCorrectedToItsNearestMatch(t *testing.T) {
	object, warnings := repair(t, "light_file", `{"verb":"raed","path":"/tmp/x"}`)
	if object["verb"] != "read" {
		t.Fatalf("raed was not corrected to read: %#v", object)
	}
	if warningMentioning(warnings, "raed", "read") == "" {
		t.Fatalf("the verb correction was silent; warnings=%v", warnings)
	}
}

// The load-bearing half of the port. A typo one edit away from a mutating verb
// must NOT be coerced onto it — a repair layer that can silently start writing
// files is worse than the refusal it replaced.
func TestNearestDestructiveVerbIsRefusedNotCoerced(t *testing.T) {
	for _, given := range []string{"writ", "renmae", "vault_restor"} {
		_, _, err := Repair("light_file", fileSchema(), json.RawMessage(`{"verb":"`+given+`","path":"/tmp/x"}`))
		if err == nil {
			t.Fatalf("verb %q was coerced onto a destructive verb instead of refused", given)
		}
		var diagnostic *DiagnosticError
		if !errors.As(err, &diagnostic) {
			t.Fatalf("verb %q produced a non-diagnostic error: %v", given, err)
		}
		if !strings.Contains(diagnostic.Message, "MUTATES") {
			t.Fatalf("the refusal for %q does not say why it refused: %s", given, diagnostic.Message)
		}
		if !strings.Contains(diagnostic.Message, given) {
			t.Fatalf("the refusal for %q does not echo what was sent: %s", given, diagnostic.Message)
		}
	}
}

func TestValidArgumentsAreReturnedUntouched(t *testing.T) {
	raw := json.RawMessage(`{"verb":"read","path":"/tmp/x","offset":0}`)
	repaired, warnings, err := Repair("light_file", fileSchema(), raw)
	if err != nil {
		t.Fatalf("a valid call was rejected: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("a valid call produced warnings: %v", warnings)
	}
	if string(repaired) != string(raw) {
		t.Fatalf("a valid call was rewritten: %s", repaired)
	}
}

func TestAmbiguousNearestMatchIsNotGuessed(t *testing.T) {
	// "log_erors" sits one edit from log_errors and nothing else, so it repairs;
	// a key equidistant from two candidates must not.
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cat": map[string]any{"type": "string"},
			"bat": map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
	repaired, warnings, err := Repair("light_file", schema, json.RawMessage(`{"hat":"x"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(repaired, &object); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if len(object) != 0 {
		t.Fatalf("an ambiguous key was guessed rather than dropped: %#v", object)
	}
	if warningMentioning(warnings, "hat") == "" {
		t.Fatalf("the ambiguous drop was silent; warnings=%v", warnings)
	}
}

func TestUnknownVerbMessageNamesTheClosestMatchAndTheVocabulary(t *testing.T) {
	message := UnknownVerbMessage("light_ops", "log_erors")
	if !strings.Contains(message, "log_errors") {
		t.Fatalf("the closest match is missing: %s", message)
	}
	if !strings.Contains(message, "list_services") || !strings.Contains(message, "probe_port") {
		t.Fatalf("the vocabulary is missing: %s", message)
	}
}

// The repair only pays off if the warning REACHES the model — a silently fixed
// call teaches nothing and the same malformed shape comes back next turn.
func TestWarningsRideBackOnTheResult(t *testing.T) {
	tool := Tool{
		Name:        "light_file",
		InputSchema: fileSchema(),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				return nil, err
			}
			return map[string]any{"verb": object["verb"], "path": object["path"]}, nil
		},
	}
	result, err := Invoke(context.Background(), tool, json.RawMessage(`{"cmd":"read","pth":"/tmp/x"}`))
	if err != nil {
		t.Fatalf("the repaired call did not reach the handler: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result shape %T", result)
	}
	if object["verb"] != "read" || object["path"] != "/tmp/x" {
		t.Fatalf("the handler received unrepaired arguments: %#v", object)
	}
	warnings, ok := object["warnings"].([]string)
	if !ok || len(warnings) != 2 {
		t.Fatalf("warnings did not ride back on the result: %#v", object["warnings"])
	}
	if warningMentioning(warnings, "cmd", "verb") == "" || warningMentioning(warnings, `"pth"`, `"path"`) == "" {
		t.Fatalf("the delivered warnings do not describe both repairs: %v", warnings)
	}
}

func TestARefusedVerbNeverReachesTheHandler(t *testing.T) {
	called := false
	tool := Tool{
		Name:        "light_file",
		InputSchema: fileSchema(),
		Handler: func(context.Context, json.RawMessage) (any, error) {
			called = true
			return map[string]any{}, nil
		},
	}
	if _, err := Invoke(context.Background(), tool, json.RawMessage(`{"verb":"writ","path":"/tmp/x"}`)); err == nil {
		t.Fatal("a verb one edit from write was admitted")
	}
	if called {
		t.Fatal("the handler ran despite the refusal")
	}
}

func TestVerbCatalogsAreSaneAndDisjointFromTheirDestructiveSet(t *testing.T) {
	for tool, catalog := range toolVerbs {
		if len(catalog.verbs) == 0 {
			t.Fatalf("%s declares no verbs", tool)
		}
		seen := map[string]bool{}
		for _, verb := range catalog.verbs {
			if seen[verb] {
				t.Fatalf("%s lists verb %q twice", tool, verb)
			}
			seen[verb] = true
		}
		for verb := range catalog.destructive {
			if !seen[verb] {
				t.Fatalf("%s marks %q destructive but does not list it", tool, verb)
			}
		}
	}
}
