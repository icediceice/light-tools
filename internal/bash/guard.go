package bash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	hasFlag := func(from int, want string) bool {
		for index := from; index < len(tokens); index++ {
			if tokens[index].Raw == "--" {
				return false
			}
			if tokens[index].Raw == want {
				return true
			}
		}
		return false
	}
	sedOperands := func(from int) ([]int, bool) {
		var indexes []int
		terminated := false
		sawScript := false
		inPlace := false
		for index := from; index < len(tokens); index++ {
			raw := tokens[index].Raw
			if !terminated && raw == "--" {
				terminated = true
				continue
			}
			if !terminated {
				switch {
				case raw == "-i" || strings.HasPrefix(raw, "-i"):
					inPlace = true
					continue
				case raw == "-e" || raw == "-f":
					if index+1 >= len(tokens) {
						return nil, false
					}
					index++
					sawScript = true
					continue
				case strings.HasPrefix(raw, "-e") || strings.HasPrefix(raw, "-f"):
					sawScript = true
					continue
				case strings.HasPrefix(raw, "-") && raw != "-":
					continue
				}
			}
			if !sawScript {
				sawScript = true
				continue
			}
			indexes = append(indexes, index)
		}
		return indexes, inPlace
	}

	switch plan.Command {
	case "rm", "unlink":
		plan.Effect = effectDelete
		plan.Operands = nonFlag(1)
	case "mv":
		plan.Effect = effectRename
		plan.Operands = nonFlag(1)
	case "sed":
		var inPlace bool
		plan.Operands, inPlace = sedOperands(1)
		if !inPlace {
			return globPlan{}, false
		}
		plan.Effect = effectEdit
	case "gofmt":
		if !hasFlag(1, "-w") {
			return globPlan{}, false
		}
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
		case plan.Command == "sed" && (token.Raw == "-i" || token.Raw == "-e" || token.Raw == "-f" ||
			(len(token.Raw) > 2 && (strings.HasPrefix(token.Raw, "-e") || strings.HasPrefix(token.Raw, "-f")))):
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
		switch {
		case plan.Command == "sed" && (token.Raw == "-i" || token.Raw == "-e" || token.Raw == "-f" ||
			(len(token.Raw) > 2 && (strings.HasPrefix(token.Raw, "-e") || strings.HasPrefix(token.Raw, "-f")))):
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
	// An operand the shell still gets to rewrite cannot be pinned to what was
	// captured. mv is the sharp case: its matrix admits a MISSING destination,
	// so "mv src* $HOME/dest" would otherwise be pinned to a literal "$HOME"
	// path that does not exist while the real move landed somewhere else.
	for _, index := range plan.Operands {
		if raw := plan.Tokens[index].Raw; shellRewrites(raw) {
			decision.Reason = fmt.Sprintf(
				"operand %q is still expanded by the shell, so the captured surface is not what would run", raw)
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

// capturePaths returns the unique, cleaned paths whose preimages need storage.
// flattenSurface deliberately remains positional because mv protection and glob
// pinning depend on operand grouping.
func capturePaths(entries []surfaceEntry) []string {
	seen := make(map[string]bool, len(entries))
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Clean(entry.Path)
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

// surfaceDigest binds a confirmation to exactly the mutation that was printed:
// the first 8 hex of sha256 over the WHOLE command, the resolved cwd, and the
// sorted, deduped surface.
//
// The command must go in verbatim. Digesting only the executable name let a
// confirmation issued for "rm *" authorize "rm -rf *" — same operand, same
// surface, same digest, but -r is precisely what turns a refusal over a
// directory into a recursive delete of it. Flags are not decoration here; they
// are the difference between the effect that was previewed and a different one.
func surfaceDigest(command, cwd string, entries []surfaceEntry) string {
	seen := make(map[string]bool, len(entries))
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if seen[entry.Path] {
			continue
		}
		seen[entry.Path] = true
		paths = append(paths, entry.Path)
	}
	sort.Strings(paths)
	payload := command + "\x00" + cwd + "\x00" + strings.Join(paths, "\n")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:8]
}

func confirmMatches(confirm, digest string) bool {
	return strings.EqualFold(strings.TrimSpace(confirm), digest)
}

// renderSurface returns EVERY expanded entry. It used to stop at 20 and append
// an ellipsis, which quietly hid paths from the one audit that matters: this
// list is what the caller reads before confirming a run that will have NO
// revert. A surface too long to read comfortably is a reason to narrow the
// glob, not a reason to be shown less of it.
func renderSurface(entries []surfaceEntry) []string {
	rendered := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Hazards) > 0 {
			rendered = append(rendered, fmt.Sprintf("%s (%s)", entry.Path, strings.Join(entry.Hazards, ",")))
			continue
		}
		rendered = append(rendered, entry.Path)
	}
	return rendered
}

// shellRewrites reports an operand the shell would rewrite at execution time.
//
// The protected lane substitutes the paths it captured, so an operand whose
// meaning is decided later cannot honestly be backed. Quoting does not settle
// it: tokenizeCommand collapses '...' and "..." into one Quoted flag, so a
// token that survived double quotes can still carry a live expansion. Treating
// every one of these as unprotectable costs a confirm fence on an exotic call
// and buys the guarantee that what was captured is what runs.
func shellRewrites(raw string) bool {
	return strings.ContainsAny(raw, "$`~{")
}

// pinCommand rebuilds the mutator so it operates on exactly the paths that were
// captured. Without it the shell expands the glob a SECOND time when the
// command actually runs, and anything created in the gap between capture and
// execution is mutated with no snapshot behind it.
//
// Every word is re-quoted, which is safe precisely because shellRewrites has
// already excluded the operands whose expansion carried meaning: after word
// splitting there is nothing left for the shell to do but pass them through.
// pinCommand rewrites the command to name the captured paths literally, so the
// shell cannot re-expand the pattern onto a different set between the snapshot
// and the effect.
//
// It emits POSIX shell syntax: every word is single-quoted, the command name
// included. PowerShell — the shell runSync uses on Windows — parses a leading
// quoted string as a string literal rather than a command, so a pinned command
// there never executes at all. The mutation silently does nothing while the
// caller is told the surface was captured, which is strictly worse than not
// pinning.
//
// Rewriting into PowerShell instead would mean modelling cmdlet parameter
// binding — Remove-Item takes -Filter positionally at index 1, so a naive
// `& 'rm' 'a' 'b'` deletes nothing and a wrong guess deletes the wrong files.
// That is not a trade worth taking inside a safety feature. On Windows we keep
// the capture, which is the substantial protection because it is what makes the
// mutation revertible, and let the shell expand the original pattern itself,
// accepting the narrower re-expansion window.
func pinCommand(plan globPlan, groups [][]surfaceEntry) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	replacement := make(map[int][]string, len(plan.Operands))
	for position, index := range plan.Operands {
		paths := make([]string, 0, len(groups[position]))
		for _, entry := range groups[position] {
			paths = append(paths, shellQuote(entry.Path))
		}
		replacement[index] = paths
	}
	words := make([]string, 0, len(plan.Tokens))
	for index, token := range plan.Tokens {
		if paths, ok := replacement[index]; ok {
			words = append(words, paths...)
			continue
		}
		words = append(words, shellQuote(token.Raw))
	}
	return strings.Join(words, " ")
}

func shellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
