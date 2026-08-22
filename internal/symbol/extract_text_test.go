package symbol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTextExtractorsArePlatformIndependent(t *testing.T) {
	tests := []struct {
		path   string
		source string
		name   string
		kind   string
	}{
		{"styles.css", ".card { color: red; }\n", "card", KindCSSClass},
		{"README.markdown", "intro\n## Install\n", "Install", KindMDHeading},
		{"config.yml", "service: light\n", "service", KindYAMLKey},
		{"config.toml", "[server]\nport = 8080\n", "server", KindTOMLSection},
	}
	for _, test := range tests {
		symbols, err := Extract(test.path, []byte(test.source))
		if err != nil {
			t.Fatalf("%s: %v", test.path, err)
		}
		if len(symbols) == 0 || symbols[0].Name != test.name || symbols[0].Kind != test.kind {
			t.Fatalf("%s: got %#v", test.path, symbols)
		}
		for _, symbol := range symbols {
			if symbol.StartByte >= symbol.EndByte || !ValidKind(symbol.Kind) {
				t.Fatalf("%s: invalid symbol %#v", test.path, symbol)
			}
		}
	}
}

func TestLargeMarkdownExtractionIsLinearEnough(t *testing.T) {
	var source strings.Builder
	for index := 0; index < 5000; index++ {
		source.WriteString("## Heading\n")
	}
	started := time.Now()
	symbols, err := Extract("CHANGELOG.md", []byte(source.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 5000 {
		t.Fatalf("headings = %d", len(symbols))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("markdown extraction took %s", elapsed)
	}
}

func TestUnsupportedAndGrammarOnlyExtensions(t *testing.T) {
	if _, err := Extract("notes.txt", []byte("hello")); !errors.Is(err, ErrUnsupportedExtension) {
		t.Fatalf("unsupported extension error = %v", err)
	}
}
