package bash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Backup-and-revert for unquoted path globs.
//
// The rule this file implements: a glob mutator whose ENTIRE expanded surface
// can be durably snapshotted runs on FIRST contact, backed by a capture the
// caller can revert. Only a surface that cannot be fully protected degrades to
// a preview the caller must confirm — and such a run is labelled as having no
// revert, because "ran" and "ran revertibly" are different facts.
//
// The protection matrix is deliberately faithful to the Light stack's
// decideGlobProtection. Its non-obvious calls are load-bearing:
//   - a directory can never be quarantined for rm, because the coreutils call
//     would FAIL on it and substituting success would change the command's
//     own outcome;
//   - an edit only ever protects regular files;
//   - only mv's DESTINATION may be absent, since mv-to-a-new-name is the one
//     modeled shape where a missing path is expected rather than a surprise.

const (
	effectDelete = "delete"
	effectRename = "rename"
	effectEdit   = "edit"
)

type globToken struct {
	Raw     string
	Quoted  bool
	HasGlob bool
}

// globPlan is the modeled shape of one simple mutator invocation.
type globPlan struct {
	Command  string
	Tokens   []globToken
	Operands []int
	Effect   string
}

type surfaceEntry struct {
	Path    string
	Hazards []string
}

type protectionDecision struct {
	Effect      string
	Protectable bool
	Reason      string
}

// tokenizeCommand splits a command into shell words. It reports ok=false the
// moment it sees an unquoted control operator: a pipeline, a subshell or a
// redirect has a surface this guard cannot honestly enumerate, and claiming
// otherwise would promise a revert over paths it never saw.
func tokenizeCommand(command string) ([]globToken, bool) {
	var tokens []globToken
	var current strings.Builder
	var singleQuoted, doubleQuoted, escaped, quoted, glob, started bool

	flush := func() {
		if !started {
			return
		}
		tokens = append(tokens, globToken{Raw: current.String(), Quoted: quoted, HasGlob: glob})
		current.Reset()
		quoted, glob, started = false, false, false
	}

	for _, character := range command {
		if escaped {
			current.WriteRune(character)
			escaped, started = false, true
			continue
		}
		if character == '\\' && !singleQuoted {
			escaped, started = true, true
			continue
		}
		if character == '\'' && !doubleQuoted {
			singleQuoted = !singleQuoted
			quoted, started = true, true
			continue
		}
		if character == '"' && !singleQuoted {
			doubleQuoted = !doubleQuoted
			quoted, started = true, true
			continue
		}
		if !singleQuoted && !doubleQuoted {
			if strings.ContainsRune("|&;()<>\n", character) {
				return nil, false
			}
			if character == ' ' || character == '\t' {
				flush()
				continue
			}
			if character == '*' || character == '?' || character == '[' {
				glob = true
			}
		}
		current.WriteRune(character)
		started = true
	}
	if singleQuoted || doubleQuoted || escaped {
		return nil, false
	}
	flush()
	return tokens, true
}

// planGlobMutation recognises the shapes whose effect can be reverted. An
// unrecognised command is not an error: it simply never enters this lane.
func planGlobMutation(command string) (globPlan, bool) {
	tokens, ok := tokenizeCommand(command)
	if !ok || len(tokens) == 0 {
		return globPlan{}, false
	}
	plan := globPlan{Command: tokens[0].Raw, Tokens: tokens}

	nonFlag := func(from int) []int {
		var indexes []int
		terminated := false
		for index := from; index < len(tokens); index++ {
			if !terminated && tokens[index].Raw == "--" {
				terminated = true
				continue
			}
			if !terminated && strings.HasPrefix(tokens[index].Raw, "-") && tokens[index].Raw != "-" {
				continue
			}
			indexes = append(indexes, index)
		}
		return indexes
	}

	switch plan.Command {
	case "rm", "unlink":
		plan.Effect = effectDelete
		plan.Operands = nonFlag(1)
	case "mv":
		plan.Effect = effectRename
		plan.Operands = nonFlag(1)
	case "sed":
		// sed's first non-flag word is the script, not a path.
		plan.Effect = effectEdit
		operands := nonFlag(1)
		if len(operands) < 2 {
			return globPlan{}, false
		}
		plan.Operands = operands[1:]
	case "gofmt":
		plan.Effect = effectEdit
		plan.Operands = nonFlag(1)
	case "go":
		if len(tokens) < 2 || tokens[1].Raw != "fmt" {
			return globPlan{}, false
		}
		plan.Effect = effectEdit
		plan.Operands = nonFlag(2)
	default:
		return globPlan{}, false
	}
	if len(plan.Operands) == 0 {
		return globPlan{}, false
	}
	return plan, true
}

// unmodeledGlobFlags lists non-operand words the capture lane cannot honestly
// model. An option that changes the semantics the capture substitutes (rm -i
// prompts), writes side files outside the declared surface (sed -i.bak, mv -b)
// or redirects operands (mv -t) must never claim to be capture-backed.
func unmodeledGlobFlags(plan globPlan) []string {
	selected := make(map[int]bool, len(plan.Operands))
	for _, index := range plan.Operands {
		selected[index] = true
	}
	var bad []string
	sawTerminator := false
	for index, token := range plan.Tokens {
		if index == 0 || selected[index] || !strings.HasPrefix(token.Raw, "-") {
			continue
		}
		if token.Raw == "--" && !sawTerminator {
			sawTerminator = true
			continue
		}
		switch {
		case plan.Command == "sed" && token.Raw == "-i":
			continue
		case plan.Command == "gofmt" && token.Raw == "-w":
			continue
		case plan.Command == "rm" && (token.Raw == "-f" || token.Raw == "-r" || token.Raw == "-rf" || token.Raw == "-fr"):
			// -r still cannot protect a directory; that is caught by hazards,
			// not here, so the reason names the actual blocking path.
			continue
		}
		bad = append(bad, token.Raw)
	}
	return bad
}

// expandSurface resolves each operand against cwd and lstats every match. The
// per-operand grouping is preserved because mv's matrix is positional.
func expandSurface(plan globPlan, cwd string) ([][]surfaceEntry, error) {
	groups := make([][]surfaceEntry, 0, len(plan.Operands))
	for _, index := range plan.Operands {
		token := plan.Tokens[index]
		operand := token.Raw
		if !filepath.IsAbs(operand) {
			operand = filepath.Join(cwd, operand)
		}
		var matches []string
		if token.HasGlob && !token.Quoted {
			expanded, err := filepath.Glob(operand)
			if err != nil {
				return nil, fmt.Errorf("operand %q is not a valid pattern: %w", token.Raw, err)
			}
			matches = expanded
		} else {
			matches = []string{operand}
		}
		sort.Strings(matches)
		group := make([]surfaceEntry, 0, len(matches))
		for _, match := range matches {
			group = append(group, surfaceEntry{Path: match, Hazards: pathHazards(match)})
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func pathHazards(path string) []string {
	var hazards []string
	if !utf8.ValidString(path) {
		hazards = append(hazards, "invalid_utf8")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return append(hazards, "missing")
	}
	if err != nil {
		return append(hazards, "unreadable")
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		hazards = append(hazards, "symlink")
	case info.IsDir():
		hazards = append(hazards, "directory")
	case !info.Mode().IsRegular():
		hazards = append(hazards, "irregular")
	}
	return hazards
}

func hazardsAllow(hazards []string, allowed ...string) bool {
	for _, hazard := range hazards {
		ok := false
		for _, candidate := range allowed {
			if hazard == candidate {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func firstHazard(hazards []string, fallback string) string {
	if len(hazards) == 0 {
		return fallback
	}
	return strings.Join(hazards, ",")
}

// decideGlobProtection is the admission matrix. A surface that is not fully
// protectable is not an error — it degrades to the confirm fence, named with
// the reason so the caller knows what it is being asked to accept.
func decideGlobProtection(plan globPlan, groups [][]surfaceEntry) protectionDecision {
	decision := protectionDecision{Effect: plan.Effect}
	if decision.Effect == "" {
		decision.Reason = fmt.Sprintf("command %q has no modeled capture effect", plan.Command)
		return decision
	}
	if bad := unmodeledGlobFlags(plan); len(bad) > 0 {
		decision.Reason = fmt.Sprintf(
			"unmodeled option %s — capture cannot enumerate every path it would mutate, so protection is unavailable",
			strings.Join(bad, " "))
		return decision
	}
	for _, group := range groups {
		if len(group) == 0 {
			decision.Reason = "an operand matched nothing — the shell would pass it through literally"
			return decision
		}
	}
	blocker := func(path, hazard string) protectionDecision {
		decision.Reason = fmt.Sprintf("%s (%s)", path, hazard)
		return decision
	}
	switch decision.Effect {
	case effectDelete:
		for _, group := range groups {
			for _, entry := range group {
				if !hazardsAllow(entry.Hazards, "symlink") {
					return blocker(entry.Path, firstHazard(entry.Hazards, "directory"))
				}
			}
		}
		decision.Protectable = true
	case effectRename:
		if len(groups) != 2 {
			decision.Reason = "mv protection requires exactly one source and one destination operand"
			return decision
		}
		if len(groups[0]) != 1 {
			decision.Reason = fmt.Sprintf("multi-source mv expanded to %d source paths — outside the protected lane", len(groups[0]))
			return decision
		}
		if len(groups[1]) != 1 {
			decision.Reason = fmt.Sprintf("mv destination expanded to %d paths — outside the protected lane", len(groups[1]))
			return decision
		}
		if !hazardsAllow(groups[0][0].Hazards, "symlink") {
			return blocker(groups[0][0].Path, firstHazard(groups[0][0].Hazards, "unsupported source"))
		}
		if !hazardsAllow(groups[1][0].Hazards, "symlink", "missing") {
			return blocker(groups[1][0].Path, firstHazard(groups[1][0].Hazards, "unsupported destination"))
		}
		decision.Protectable = true
	case effectEdit:
		for _, group := range groups {
			for _, entry := range group {
				if len(entry.Hazards) > 0 {
					return blocker(entry.Path, firstHazard(entry.Hazards, "non-regular target"))
				}
			}
		}
		decision.Protectable = true
	}
	return decision
}

func flattenSurface(groups [][]surfaceEntry) []surfaceEntry {
	var flat []surfaceEntry
	for _, group := range groups {
		flat = append(flat, group...)
	}
	return flat
}

// surfaceDigest binds a confirmation to exactly the surface that was printed:
// the first 8 hex of sha256 over the sorted, deduped "command\x00path" pairs.
func surfaceDigest(command string, entries []surfaceEntry) string {
	seen := make(map[string]bool, len(entries))
	pairs := make([]string, 0, len(entries))
	for _, entry := range entries {
		pair := command + "\x00" + entry.Path
		if seen[pair] {
			continue
		}
		seen[pair] = true
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))
	return hex.EncodeToString(sum[:])[:8]
}

func confirmMatches(confirm, digest string) bool {
	return strings.EqualFold(strings.TrimSpace(confirm), digest)
}

const surfaceListCap = 20

func renderSurface(entries []surfaceEntry) []string {
	rendered := make([]string, 0, len(entries))
	for index, entry := range entries {
		if index == surfaceListCap {
			rendered = append(rendered, fmt.Sprintf("… +%d more", len(entries)-index))
			break
		}
		if len(entry.Hazards) > 0 {
			rendered = append(rendered, fmt.Sprintf("%s (%s)", entry.Path, strings.Join(entry.Hazards, ",")))
			continue
		}
		rendered = append(rendered, entry.Path)
	}
	return rendered
}
