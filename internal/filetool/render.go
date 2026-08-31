package filetool

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/symbol"
)

// This file is the read lanes' delivery seam. A read result is built once as
// its canonical map (or symbol matches) and can then be delivered in either
// of two shapes: the canonical JSON envelope, or a plain render. Which of the
// two ships is decided by chooseDelivery on delivered bytes alone — the same
// currency docs/BENCHMARK.md measures.

// chooseDelivery returns whichever representation of a read result is
// strictly fewer BYTES: the canonical JSON envelope, or the plain render. A
// tie keeps the JSON. There is deliberately no size floor: terse/encode.go
// (Format declining below 100 tokens) and logs/analyze.go (delivering the raw
// stream under MinLines/MinBytes) both mean "below the floor keep the
// CANONICAL form", not "force the alternate" — a floor that forced the plain
// render could deliver MORE bytes than the envelope it replaced, which is the
// exact outcome this comparison exists to prevent. The byte comparison is the
// whole rule, and it compares bytes rather than token estimates because
// docs/BENCHMARK.md measures delivered bytes.
func chooseDelivery(value any, plain string) (mcp.Result, error) {
	canonical, err := json.Marshal(value)
	if err != nil {
		return mcp.Result{}, err
	}
	if len(plain) < len(canonical) {
		return mcp.Result{Content: []mcp.Content{mcp.Text(plain)}}, nil
	}
	return mcp.Result{Content: []mcp.Content{mcp.Text(string(canonical))}}, nil
	return mcp.Result{Content: []mcp.Content{mcp.Text(string(canonical))}}, nil
}

// plainHeader emits the first line of a plain render. The path is Go-quoted
// so every legal path — one containing a newline, a quote, or any other
// control byte — stays on exactly one physical line and round-trips through
// strconv.Unquote; a raw path would let a filename byte terminate the header
// and masquerade as the content that follows. The '=' first byte the dual
// decoder discriminates on is unchanged.
func plainHeader(path string) string {
	return "=== " + strconv.Quote(path) + " ===\n"
}

// renderWindowText renders the canonical window result as plain text: the
// header line, the numbered window content verbatim, and one bracketed meta
// line carrying every scalar field the JSON envelope would — total_lines,
// bytes, tokens, sha256, next_offset and continued, plus truncated, spill_id
// and note when present. The plain form MUST begin with plainHeader —
// '===" ' + a quoted path + " ===" — and the canonical JSON begins "{".
func renderWindowText(result map[string]any) string {
	path := textValue(result, "path")
	content := textValue(result, "content")
	var b strings.Builder
	b.WriteString(plainHeader(path))
	b.WriteString(content)
	// The meta line must start at a line boundary even when the content was
	// truncated mid-line (the oversized-line case), so pad only when the
	// content does not already end in a newline. Decoders strip exactly this
	// one separator, which keeps a truncated page byte-exact.
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "[meta total_lines=%d bytes=%d tokens=%d sha256=%s next_offset=%d continued=%t",
		intValue(result, "total_lines"), intValue(result, "bytes"), intValue(result, "tokens"),
		textValue(result, "sha256"), intValue(result, "next_offset"), boolValue(result, "continued"))
	if boolValue(result, "truncated") {
		b.WriteString(" truncated=true")
	}
	if id := textValue(result, "spill_id"); id != "" {
		fmt.Fprintf(&b, " spill_id=%s", id)
	}
	// note is free text and always last: everything after "note=" is the note.
	if note := textValue(result, "note"); note != "" {
		fmt.Fprintf(&b, " note=%s", note)
	}
	b.WriteString("]\n")
	return b.String()
}

// symbolMatch is one named-symbol hit: the extracted symbol with every
// contracted field, plus the source lines it spans. It is the payload both
// symbol deliveries render — the JSON envelope marshals it with the same
// "symbol"/"content" keys the handler has always shipped.
type symbolMatch struct {
	Symbol  symbol.Symbol `json:"symbol"`
	Content string        `json:"content"`
}

// renderSymbolText renders a named-symbol read as plain text: the header
// line, then one section per match carrying EVERY symbol.Symbol field (name,
// kind, signature, comment, parent, start_line, end_line, start_byte,
// end_byte) plus the source content. "[symbols unavailable] <err>" and "[no
// symbol matches]" stay distinct states, mirroring the envelope's note branch
// and empty-matches branch. Each variable-length field is delimited by its
// exact byte length — quoted string fields, and the content body as
// "content N bytes" followed by exactly N raw bytes — so source bytes cannot
// forge a section delimiter: a decoder walks the lengths arithmetically and
// never scans for delimiters inside a body.
func renderSymbolText(path string, extractErr error, matches []symbolMatch) string {
	var b strings.Builder
	b.WriteString(plainHeader(path))
	if extractErr != nil {
		fmt.Fprintf(&b, "[symbols unavailable] %s\n", extractErr.Error())
		return b.String()
	}
	if len(matches) == 0 {
		b.WriteString("[no symbol matches]\n")
		return b.String()
	}
	for _, match := range matches {
		extracted := match.Symbol
		fmt.Fprintf(&b, "--- symbol lines %d-%d bytes %d-%d\n",
			extracted.StartLine, extracted.EndLine,
			extracted.StartByte, extracted.EndByte)
		// Name and kind are quoted field lines, never raw header tokens:
		// extractor-legal names carry whitespace (a Markdown heading), and a
		// whitespace-separated header cannot encode them unambiguously.
		fmt.Fprintf(&b, "name %s\n", strconv.Quote(extracted.Name))
		fmt.Fprintf(&b, "kind %s\n", strconv.Quote(extracted.Kind))
		if extracted.Signature != "" {
			fmt.Fprintf(&b, "signature %s\n", strconv.Quote(extracted.Signature))
		}
		if extracted.Comment != "" {
			fmt.Fprintf(&b, "comment %s\n", strconv.Quote(extracted.Comment))
		}
		if extracted.Parent != "" {
			fmt.Fprintf(&b, "parent %s\n", strconv.Quote(extracted.Parent))
		}
		fmt.Fprintf(&b, "content %d bytes\n", len(match.Content))
		b.WriteString(match.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func textValue(result map[string]any, key string) string {
	value, _ := result[key].(string)
	return value
}

func intValue(result map[string]any, key string) int {
	value, _ := result[key].(int)
	return value
}

func boolValue(result map[string]any, key string) bool {
	value, _ := result[key].(bool)
	return value
}
