package symbol

import (
	"errors"
	"testing"
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

func TestUnsupportedAndGrammarOnlyExtensions(t *testing.T) {
	if _, err := Extract("notes.txt", []byte("hello")); !errors.Is(err, ErrUnsupportedExtension) {
		t.Fatalf("unsupported extension error = %v", err)
	}
	if _, err := Extract("main.go", []byte("package main")); err == nil {
		t.Fatal("grammar-backed extraction unexpectedly available without treesitter tag")
	}
}
