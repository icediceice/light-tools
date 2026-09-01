package bench

// The code-reading track.
//
// The question here is narrower than the log track's and worth stating
// exactly: given that an agent ALREADY KNOWS which symbol it wants, what does
// it cost to get that symbol into context? This suite does not measure finding
// the symbol. light-tools is explicitly not a code-intelligence layer (see the
// README), so crediting it for search would be measuring something it does not
// claim to do.
//
// BUILD TAG: these scenarios require -tags treesitter. Without it
// internal/symbol/extract_stub.go returns ErrUnavailable for Go source and the
// symbol verb returns no matches, which would measure the degraded path rather
// than the shipped one. bench_test.go fails loudly rather than skipping.
//
// PROVENANCE: the source fixtures are SYNTHETIC Go, generated below. The
// property being measured is structural — file size against symbol size — and
// the parser doing the work is the real tree-sitter one either way. But
// synthetic input is a real limitation and the report says so.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/icediceice/light-tools/internal/filetool"
	"github.com/icediceice/light-tools/internal/security"
)

// codeWindow is how many lines the skilled baseline reads around a grep hit —
// enough to contain a medium function with its doc comment.
const codeWindow = 60

// newFileHandler builds a real filetool handler over a fixture root.
//
// DefaultEpoch is set because read-dedup is scoped by it: with an empty epoch
// the ledger is inert (handler.go:resolveEpoch and readcache ShouldElide), so
// the dedup scenario would measure dedup being switched off.
func newFileHandler(root string) (*filetool.Handler, error) {
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		return nil, err
	}
	return filetool.New(filetool.Options{
		Confiner:     confiner,
		SnapshotRoot: filepath.Join(root, ".snapshots"),
		DefaultEpoch: "bench-epoch",
	})
}

// callFileTool drives the handler through Portable — the exact entry point the
// MCP server registers, taking the same JSON a model sends and returning the
// same result. Measuring an internal helper instead would report a saving that
// never reaches a model.
func callFileTool(handler *filetool.Handler, request map[string]any) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	result, err := handler.Portable()(context.Background(), raw)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, content := range envelope.Content {
		b.WriteString(content.Text)
	}
	return b.String(), nil
}

// normalise strips the fixture root from delivered text.
//
// filetool returns absolute paths, so without this the byte counts would move
// with the temp directory and the report would differ per machine. It is
// applied to EVERY arm identically, so it cannot tilt a comparison.
//
// A single literal ReplaceAll of the raw root is NOT enough, and both gaps it
// left showed up as cross-platform drift in docs/BENCHMARK.md — the report
// regenerated green on Linux and then failed as "stale" on macOS and Windows:
//
//   - macOS: t.TempDir() hands back /var/folders/..., but /var is a symlink to
//     /private/var, so the path filetool resolves and echoes back is
//     /private/var/folders/.... Replacing only the unresolved root strands a
//     stray "/private" in the delivered text.
//   - Windows: both the JSON envelope and render.go's plainHeader put the path
//     through Go/JSON quoting, so separators arrive escaped (C:\\Users\\...)
//     and never match the raw root at all.
//
// Longest-first ordering is load-bearing: the resolved root CONTAINS the raw
// root as a substring on macOS, and replacing the shorter one first would
// strand exactly the prefix this exists to remove.
func normalise(text, root string) string {
	variants := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		variants = append(variants, resolved)
	}
	for _, variant := range append([]string(nil), variants...) {
		// The same path also reaches us Go-quoted (plainHeader) and
		// JSON-escaped (the envelope); both escape a separator identically,
		// so the quoted body covers each shape.
		if quoted := strings.Trim(strconv.Quote(variant), `"`); quoted != variant {
			variants = append(variants, quoted)
		}
	}
	sort.SliceStable(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, variant := range variants {
		text = strings.ReplaceAll(text, variant, "<root>")
	}
	return text
}

// grepWindow models the skilled baseline: locate the pattern, then read a
// bounded window around the first hit.
func grepWindow(source, pattern string) (string, error) {
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	lines := strings.Split(source, "\n")
	for index, line := range lines {
		if !expression.MatchString(line) {
			continue
		}
		start := index
		if start > 4 {
			start -= 4 // a few lines of lead-in, as a reader would take
		}
		end := start + codeWindow
		if end > len(lines) {
			end = len(lines)
		}
		return strings.Join(lines[start:end], "\n") + "\n", nil
	}
	return "", nil
}

// CodeArms measures extracting ONE known symbol from one file.
func CodeArms(handler *filetool.Handler, root, path, symbolName string) ([]Observation, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Naive — the whole file, which is what a native read-a-file tool returns.
	naiveText := normalise(string(source), root)
	naive := Observation{Arm: ArmNaive, Delivered: len(naiveText), Calls: 1, Text: naiveText}

	// Skilled — grep for the declaration, then read a window around it. The
	// pattern is derived from the symbol name the agent already has, which is
	// the premise of this whole track, so it is fair.
	windowText, err := grepWindow(string(source), `func .*\b`+regexp.QuoteMeta(symbolName)+`\b`)
	if err != nil {
		return nil, err
	}
	windowText = normalise(windowText, root)
	skilled := Observation{Arm: ArmSkilled, Delivered: len(windowText), Calls: 2, Text: windowText}

	// Light — the symbol verb, one call, whole declaration and nothing else.
	lightText, err := callFileTool(handler, map[string]any{
		"verb": "symbol", "path": path, "name": symbolName,
	})
	if err != nil {
		return nil, err
	}
	lightText = normalise(lightText, root)
	light := Observation{Arm: ArmLight, Delivered: len(lightText), Calls: 1, Text: lightText}

	return []Observation{naive, skilled, light}, nil
}

// CodeBatchArms measures collecting several symbols spread across several
// files — the case where the per-call round trip, not the byte count, is the
// dominant cost of the native path.
func CodeBatchArms(handler *filetool.Handler, root string, paths, names []string) ([]Observation, error) {
	if len(paths) != len(names) {
		return nil, fmt.Errorf("paths and names must pair up")
	}

	var naiveBuilder, skilledBuilder strings.Builder
	for index, path := range paths {
		source, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		naiveBuilder.Write(source)
		window, err := grepWindow(string(source), `func .*\b`+regexp.QuoteMeta(names[index])+`\b`)
		if err != nil {
			return nil, err
		}
		skilledBuilder.WriteString(window)
	}

	naiveText := normalise(naiveBuilder.String(), root)
	naive := Observation{Arm: ArmNaive, Delivered: len(naiveText), Calls: len(paths), Text: naiveText}

	skilledText := normalise(skilledBuilder.String(), root)
	// Two calls per file: locate, then read the window.
	skilled := Observation{Arm: ArmSkilled, Delivered: len(skilledText), Calls: len(paths) * 2, Text: skilledText}

	items := make([]map[string]any, 0, len(paths))
	for index, path := range paths {
		items = append(items, map[string]any{"path": path, "name": names[index]})
	}
	lightText, err := callFileTool(handler, map[string]any{"verb": "read", "items": items})
	if err != nil {
		return nil, err
	}
	lightText = normalise(lightText, root)
	light := Observation{Arm: ArmLight, Delivered: len(lightText), Calls: 1, Text: lightText}

	return []Observation{naive, skilled, light}, nil
}

// CodeRereadArms measures reading the SAME unchanged window twice.
//
// Only the second observation is reported. The first is the cost of learning
// the content, which every arm pays alike; the question this row asks is what
// the REPEAT costs, and that is where the arms diverge.
func CodeRereadArms(handler *filetool.Handler, root, path string) ([]Observation, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// A native re-read has no memory of the first one: it returns the file
	// again, in full, at full cost.
	naiveText := normalise(string(source), root)
	naive := Observation{Arm: ArmNaive, Delivered: len(naiveText), Calls: 1, Text: naiveText}

	// This row's question is "have these bytes changed since I read them", and
	// no grep answers that — a native agent has to re-read the file in full.
	// So the full re-read IS the correct skilled arm HERE; it is deliberately
	// NOT the grep-then-window shape the other code rows use, and the report
	// marks the row ‡, states the substitution, and holds it out of the
	// aggregate tally so a different baseline algorithm cannot inflate the
	// headline (report.go:writeTally).
	skilled := Observation{Arm: ArmSkilled, Delivered: len(naiveText), Calls: 1, Text: naiveText}

	// The window is wide enough to contain the target symbol, so the FIRST
	// read genuinely carries the answer. A window that excluded it would make
	// the dedup row assert nothing.
	request := map[string]any{"verb": "read", "path": path, "offset": 0, "limit": 900}
	if _, err := callFileTool(handler, request); err != nil {
		return nil, err
	}
	repeat, err := callFileTool(handler, request)
	if err != nil {
		return nil, err
	}
	repeat = normalise(repeat, root)
	light := Observation{Arm: ArmLight, Delivered: len(repeat), Calls: 1, Text: repeat}

	return []Observation{naive, skilled, light}, nil
}

// CodeScenarios returns the code track. root is a caller-owned directory; the
// fixtures are written into it and nothing is written anywhere else.
func CodeScenarios(root string) ([]Scenario, error) {
	paths, err := writeFixtures(root)
	if err != nil {
		return nil, err
	}
	handler, err := newFileHandler(root)
	if err != nil {
		return nil, err
	}

	return []Scenario{
		{
			Name:     "symbol-in-large-file",
			Track:    TrackCode,
			Question: "I need to change Coordinator.ReconcileExpiredSessions. Show me its current body.",
			Corpus:   "SYNTHETIC — a ~700-line Go file holding 61 declarations",
			Answers:  []*regexp.Regexp{regexp.MustCompile(`RECONCILE_SENTINEL`)},
			Note: "The everyday case for an editing agent: the symbol is known, the file is not small, " +
				"and only the declaration is wanted.",
			Run: func() ([]Observation, error) {
				return CodeArms(handler, root, paths["service.go"], "ReconcileExpiredSessions")
			},
		},
		{
			Name:     "symbol-in-medium-file",
			Track:    TrackCode,
			Question: "Show me ResolveTransport so I can change its error path.",
			Corpus:   "SYNTHETIC — a ~70-line Go file holding 8 declarations",
			Answers:  []*regexp.Regexp{regexp.MustCompile(`TRANSPORT_SENTINEL`)},
			Note: "A moderate file. Included so the track is not built only from the extreme case — " +
				"a suite of nothing but 700-line files would overstate the everyday result.",
			Run: func() ([]Observation, error) {
				return CodeArms(handler, root, paths["transport.go"], "ResolveTransport")
			},
		},
		{
			Name:     "batch-across-files",
			Track:    TrackCode,
			Question: "Show me ResolveLedger, ResolveTransport and ResolveRegistry — I need all three before I can change any of them.",
			Corpus:   "SYNTHETIC — three Go files, one wanted declaration in each",
			Answers: []*regexp.Regexp{
				// The question asks for all THREE declarations, so all three
				// must survive. Asserting only the last one would let an arm
				// deliver a third of what was asked and still pass.
				regexp.MustCompile(`LEDGER_SENTINEL`),
				regexp.MustCompile(`TRANSPORT_SENTINEL`),
				regexp.MustCompile(`REGISTRY_SENTINEL`),
			},
			Note: "The row where ROUND TRIPS, not bytes, are the native path's real cost: three files " +
				"means three reads, and a grep-first agent pays six. The light arm asks once.",
			Run: func() ([]Observation, error) {
				return CodeBatchArms(handler, root,
					[]string{paths["ledger.go"], paths["transport.go"], paths["registry.go"]},
					[]string{"ResolveLedger", "ResolveTransport", "ResolveRegistry"})
			},
		},
		{
			Name:           "repeat-read-unchanged",
			Track:          TrackCode,
			Question:       "I read this file earlier and need to confirm it has not changed.",
			Corpus:         "SYNTHETIC — the ~700-line file, read twice with no edit between",
			Answers:        []*regexp.Regexp{regexp.MustCompile(`RECONCILE_SENTINEL`)},
			ContextCarried: true,
			Note: "The light arm returns a dedup stub rather than the content, which is correct ONLY " +
				"because the reader already holds it from the first read. Scored accordingly: the first " +
				"read must have carried the answer and the stub must identify the exact bytes it stands for. " +
				"This row measures a repeat, so it does not generalise to first contact. " +
				"READ THE RATIO WITH CARE: no grep answers 'have these bytes changed', so the skilled arm " +
				"here is a FULL re-read rather than the grep-then-window shape used by every other row. " +
				"That is the honest native cost for THIS question, but it is a different baseline algorithm, " +
				"so this row is held out of the aggregate tally and its ratio should not be quoted as though " +
				"it were comparable to the rows above.",
			Run: func() ([]Observation, error) {
				return CodeRereadArms(handler, root, paths["service.go"])
			},
		},
		{
			Name:        "tiny-file",
			Track:       TrackCode,
			Question:    "What does UserAgent return?",
			Corpus:      "SYNTHETIC — an 11-line Go file",
			Answers:     []*regexp.Regexp{regexp.MustCompile(`VERSION_SENTINEL`)},
			Adversarial: true,
			Note: "ADVERSARIAL. Reading the whole file was already the right call, so extraction has almost " +
				"nothing to remove. Reads ship the smaller of the JSON envelope and a plain render, which " +
				"removed the envelope penalty — but the structured symbol section still costs more than a " +
				"grep window over an 11-line file, so this ratio is expected to stay under 1×. A negative " +
				"row here is the suite working.",
			Run: func() ([]Observation, error) {
				return CodeArms(handler, root, paths["version.go"], "UserAgent")
			},
		},
	}, nil
}
