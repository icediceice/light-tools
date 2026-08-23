package portable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Argument repair.
//
// Every tool schema sets additionalProperties:false, so before this pass a key
// the model spelled slightly wrong was a hard refusal (schema.go: "is not
// allowed"). That is a whole turn spent to learn one character.
//
// The repair is deliberately NOT silent. A fixed call that says nothing teaches
// the model nothing, so it makes the same malformed call next time and the cost
// simply moves from a refusal to a permanent tax. Every repair emits a warning
// that rides back on the result, which is the half that makes the tool
// reinforce correct use instead of just tolerating incorrect use.
//
// One shape is deliberately NOT repaired: a verb whose nearest match is
// destructive. Coercing a typo onto write/sed/rename/vault_restore would let a
// misspelling mutate the filesystem, so that case refuses and names the
// candidate instead.

// WarningSink lets a result carry repair warnings back to the caller. A result
// type that does not implement it simply loses them, which is why the shapes
// this server actually returns all do.
type WarningSink interface {
	WithWarnings(warnings []string) any
}

// attachWarnings delivers repair warnings on whatever shape the handler
// returned. A WarningSink decides its own placement; a plain map carries them
// under "warnings". Anything else has nowhere to put them, so the repair still
// happens but goes unreported — which is why the shapes this server returns are
// exactly these two.
func attachWarnings(result any, warnings []string) any {
	if len(warnings) == 0 {
		return result
	}
	if sink, ok := result.(WarningSink); ok {
		return sink.WithWarnings(warnings)
	}
	if object, ok := result.(map[string]any); ok {
		if _, taken := object["warnings"]; !taken {
			object["warnings"] = warnings
		}
		return object
	}
	return result
}

// verbAliases are the dispatch keys models reach for instead of "verb". They
// are folded only when the tool actually declares a verb and none was given.
var verbAliases = []string{"cmd", "action", "op", "mode", "subcommand", "operation"}

type verbCatalog struct {
	verbs       []string
	destructive map[string]bool
}

// toolVerbs mirrors each handler's own switch. A verb added there and not here
// simply gets no coercion; a verb here that the handler does not accept would
// be a lie, which is what TestVerbCatalogsMatchTheHandlers guards.
var toolVerbs = map[string]verbCatalog{
	"light_file": {
		verbs: []string{
			"read", "list", "symbol", "outline", "locate", "diff", "identity", "vault_list",
			"write", "edit", "sed", "rename", "rewrite", "vault_restore",
		},
		destructive: map[string]bool{
			"write": true, "edit": true, "sed": true, "rename": true, "rewrite": true, "vault_restore": true,
		},
	},
	// light_bash and light_ops both dispatch their async lifecycle verbs ahead
	// of their main switch, so those verbs are easy to leave out of the catalog
	// here — and leaving them out makes the "Valid verbs" list the tool prints
	// actively wrong, not merely incomplete.
	"light_bash": {verbs: []string{"status", "collect", "cancel"}},
	"light_ops": {verbs: []string{
		"list_services", "probe_service", "probe_port", "probe_process", "probe_file",
		"log_grep", "log_correlate", "log_investigate",
		"log_window", "log_trace", "log_search", "log_errors", "log_since",
		"status", "collect", "cancel",
	}},
}

// Repair rewrites a nearly-right argument object into the right one and reports
// what it changed. It never invents a value and never touches a call that is
// already valid: an unchanged object is returned byte for byte.
func Repair(toolName string, schema map[string]any, raw json.RawMessage) (json.RawMessage, []string, error) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return raw, nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		// Malformed JSON is Normalize's diagnostic to report, not this pass's.
		return raw, nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return raw, nil, nil
	}

	declared := make([]string, 0, len(properties))
	for name := range properties {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	isDeclared := func(name string) bool {
		_, exists := properties[name]
		return exists
	}

	var warnings []string
	changed := false

	// Alias folding first: it can supply the verb the later coercion inspects.
	if isDeclared("verb") {
		if _, present := object["verb"]; !present {
			for _, alias := range verbAliases {
				candidate, exists := object[alias]
				if !exists || isDeclared(alias) {
					continue
				}
				if text, ok := candidate.(string); ok && text != "" {
					object["verb"] = text
					delete(object, alias)
					warnings = append(warnings, fmt.Sprintf("%s: folded %q onto \"verb\" — this tool dispatches on \"verb\"", toolName, alias))
					changed = true
					break
				}
			}
		}
	}

	// Near-miss key repair, then unknown-key removal with a suggestion.
	for _, key := range sortedKeys(object) {
		if isDeclared(key) {
			continue
		}
		match, distance := nearest(key, declared)
		if match != "" && distance <= repairThreshold(key) {
			if _, taken := object[match]; !taken {
				object[match] = object[key]
				delete(object, key)
				warnings = append(warnings, fmt.Sprintf("%s: renamed %q to %q", toolName, key, match))
				changed = true
				continue
			}
		}
		delete(object, key)
		changed = true
		if match != "" {
			warnings = append(warnings, fmt.Sprintf("%s: dropped unknown field %q — closest declared field is %q", toolName, key, match))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s: dropped unknown field %q", toolName, key))
	}

	// Verb coercion last, so it sees a folded alias and a repaired key.
	catalog, hasCatalog := toolVerbs[toolName]
	if hasCatalog {
		if given, ok := object["verb"].(string); ok && given != "" && !contains(catalog.verbs, given) {
			match, distance := nearest(given, catalog.verbs)
			switch {
			case match == "" || distance > repairThreshold(given):
				// Leave it: the handler's own unknown-verb diagnostic names the
				// whole vocabulary, which is more useful than a bad guess.
			case catalog.destructive[match]:
				return nil, nil, &DiagnosticError{
					Code: "E_VERB",
					Message: fmt.Sprintf(
						"%s: unknown verb %q. The closest match is %q, which MUTATES the filesystem — refusing to guess. Send %q explicitly if that is what you meant. Valid verbs: %s",
						toolName, given, match, match, strings.Join(catalog.verbs, ", ")),
				}
			default:
				object["verb"] = match
				warnings = append(warnings, fmt.Sprintf("%s: corrected verb %q to %q", toolName, given, match))
				changed = true
			}
		}
	}

	if !changed {
		return raw, nil, nil
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return raw, nil, nil
	}
	return encoded, warnings, nil
}

// UnknownVerbMessage renders the diagnostic a handler should return for a verb
// no repair could rescue: the closest candidate plus the whole vocabulary,
// sorted, so the model can correct itself in one step.
func UnknownVerbMessage(toolName, given string) string {
	catalog, ok := toolVerbs[toolName]
	if !ok || len(catalog.verbs) == 0 {
		return fmt.Sprintf("%s: unsupported verb %q", toolName, given)
	}
	vocabulary := append([]string(nil), catalog.verbs...)
	sort.Strings(vocabulary)
	// The closest match is reported even when it is too far to COERCE. The two
	// decisions answer different questions: coercion has to be conservative
	// because it acts on the model's behalf, but a diagnostic that withholds the
	// nearest verb just to stay quiet costs the correction turn this whole layer
	// exists to save. A tie still names nothing — nearest() returns no match — so
	// the message never invents a winner between equidistant candidates.
	if match, _ := nearest(given, catalog.verbs); match != "" {
		return fmt.Sprintf("%s: unsupported verb %q — did you mean %q? Valid verbs: %s",
			toolName, given, match, strings.Join(vocabulary, ", "))
	}
	return fmt.Sprintf("%s: unsupported verb %q. Valid verbs: %s", toolName, given, strings.Join(vocabulary, ", "))
}

// repairThreshold keeps short names strict: one edit on a three-letter key is
// most of the key, and a confident wrong rename is worse than a clean refusal.
func repairThreshold(name string) int {
	switch {
	case len(name) <= 4:
		return 1
	case len(name) <= 8:
		return 2
	default:
		return 3
	}
}

// nearest returns the single closest candidate. An ambiguous best — two
// candidates tied at the same distance — returns nothing, because picking one
// would be a coin flip presented to the model as a correction.
func nearest(given string, candidates []string) (string, int) {
	best, bestDistance, tied := "", -1, false
	lowered := strings.ToLower(given)
	for _, candidate := range candidates {
		distance := editDistance(lowered, strings.ToLower(candidate))
		switch {
		case bestDistance < 0 || distance < bestDistance:
			best, bestDistance, tied = candidate, distance, false
		case distance == bestDistance:
			tied = true
		}
	}
	if tied {
		return "", -1
	}
	return best, bestDistance
}

// editDistance is optimal string alignment, not plain Levenshtein: an adjacent
// transposition costs 1, not 2. That single difference is what makes "raed" ->
// "read" repairable, and a transposition is the most common typo there is.
func editDistance(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// rows[0] is i-2, rows[1] is i-1, rows[2] is the row being filled.
	beforePrevious := make([]int, len(b)+1)
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			best := min3(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if swapped := beforePrevious[j-2] + 1; swapped < best {
					best = swapped
				}
			}
			current[j] = best
		}
		copy(beforePrevious, previous)
		copy(previous, current)
	}
	return previous[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
