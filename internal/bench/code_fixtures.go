package bench

// Synthetic Go source fixtures for the code track.
//
// These are generated rather than copied from the repository for the reason
// step 1 of the plan gives: pointing the benchmark at live repo files would
// make every published number churn whenever unrelated code changed, and a
// report nobody can reproduce two commits later is not a measurement.
//
// They are deliberately ordinary: doc comments, imports, a type with methods,
// functions of varied length. The parser doing the extraction is the real
// tree-sitter one, so what is being exercised is the shipped path.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// writeFixtures materialises the code corpus under root and returns the paths.
func writeFixtures(root string) (map[string]string, error) {
	files := map[string]string{
		"service.go":   largeServiceFile(),
		"ledger.go":    mediumFile("ledger", 8),
		"transport.go": mediumFile("transport", 7),
		"registry.go":  mediumFile("registry", 6),
		"version.go":   tinyFile(),
	}
	paths := make(map[string]string, len(files))
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

// largeServiceFile is the "one symbol in a big file" case: ~700 lines, of
// which the wanted declaration is a few dozen.
func largeServiceFile() string {
	var b strings.Builder
	b.WriteString("// Package service coordinates request handling for the edge daemon.\n")
	b.WriteString("package service\n\n")
	b.WriteString("import (\n\t\"context\"\n\t\"errors\"\n\t\"fmt\"\n\t\"sync\"\n\t\"time\"\n)\n\n")
	b.WriteString("// Coordinator owns the lifecycle of every in-flight request.\ntype Coordinator struct {\n")
	b.WriteString("\tmu       sync.Mutex\n\tsessions map[string]*Session\n\ttimeout  time.Duration\n}\n\n")
	b.WriteString("// Session is one client's state.\ntype Session struct {\n\tID      string\n\tOpened  time.Time\n\tPending int\n}\n\n")

	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "// handleStage%02d advances a request through stage %d of the pipeline.\n", i, i)
		fmt.Fprintf(&b, "func (c *Coordinator) handleStage%02d(ctx context.Context, session *Session) error {\n", i)
		b.WriteString("\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n")
		fmt.Fprintf(&b, "\tif session == nil {\n\t\treturn errors.New(\"stage%02d: nil session\")\n\t}\n", i)
		b.WriteString("\tselect {\n\tcase <-ctx.Done():\n\t\treturn ctx.Err()\n\tdefault:\n\t}\n")
		b.WriteString("\tsession.Pending++\n\treturn nil\n}\n\n")
	}

	// The target. Distinctive enough that the answer regexp cannot match by
	// accident anywhere else in the file.
	b.WriteString("// ReconcileExpiredSessions drops sessions whose deadline has passed and\n")
	b.WriteString("// reports how many were removed. It is the symbol the benchmark extracts.\n")
	b.WriteString("func (c *Coordinator) ReconcileExpiredSessions(now time.Time) int {\n")
	b.WriteString("\tc.mu.Lock()\n\tdefer c.mu.Unlock()\n\tremoved := 0\n")
	b.WriteString("\tfor id, session := range c.sessions {\n")
	b.WriteString("\t\tif now.Sub(session.Opened) <= c.timeout {\n\t\t\tcontinue\n\t\t}\n")
	b.WriteString("\t\tdelete(c.sessions, id)\n\t\tremoved++\n\t}\n")
	b.WriteString("\treturn removed // RECONCILE_SENTINEL\n}\n\n")

	for i := 40; i < 60; i++ {
		fmt.Fprintf(&b, "// handleStage%02d advances a request through stage %d of the pipeline.\n", i, i)
		fmt.Fprintf(&b, "func (c *Coordinator) handleStage%02d(ctx context.Context, session *Session) error {\n", i)
		b.WriteString("\tif session == nil {\n\t\treturn fmt.Errorf(\"nil session\")\n\t}\n")
		b.WriteString("\tsession.Pending--\n\treturn nil\n}\n\n")
	}
	return b.String()
}

// mediumFile is a supporting file for the multi-file batch scenario. Each
// carries one distinctively named exported function the batch asks for.
func mediumFile(pkg string, functions int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Package %s is a supporting component.\npackage %s\n\n", pkg, pkg)
	b.WriteString("import (\n\t\"fmt\"\n\t\"strings\"\n)\n\n")
	for i := 0; i < functions; i++ {
		fmt.Fprintf(&b, "// helper%02d performs step %d of the %s routine.\n", i, i, pkg)
		fmt.Fprintf(&b, "func helper%02d(input string) string {\n", i)
		b.WriteString("\ttrimmed := strings.TrimSpace(input)\n")
		fmt.Fprintf(&b, "\tif trimmed == \"\" {\n\t\treturn fmt.Sprintf(\"%s:empty\")\n\t}\n", pkg)
		b.WriteString("\treturn trimmed\n}\n\n")
	}
	title := strings.ToUpper(pkg[:1]) + pkg[1:]
	fmt.Fprintf(&b, "// Resolve%s is the entry point the batch scenario asks for.\n", title)
	fmt.Fprintf(&b, "func Resolve%s(input string) (string, error) {\n", title)
	b.WriteString("\tif input == \"\" {\n\t\treturn \"\", fmt.Errorf(\"empty input\")\n\t}\n")
	fmt.Fprintf(&b, "\treturn helper00(input) + \"/%s\", nil // %s_SENTINEL\n}\n", pkg, strings.ToUpper(pkg))
	return b.String()
}

// tinyFile is ADVERSARIAL: small enough that reading all of it was already the
// right call, so the light arm cannot meaningfully beat a full read and the
// row should report parity or worse.
func tinyFile() string {
	return `// Package version reports the build identity.
package version

// Current is the released version string.
const Current = "0.4.0"

// UserAgent renders the HTTP user agent for this build.
func UserAgent() string {
	return "light-tools/" + Current // VERSION_SENTINEL
}
`
}
