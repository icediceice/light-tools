//go:build treesitter

package symbol

import "testing"

func TestSymbolsAcrossThreeLanguages(t *testing.T) {
	cases := []struct {
		path   string
		source string
		name   string
	}{
		{"sample.go", "package p\nfunc Alpha() {}\n", "Alpha"},
		{"sample.py", "def beta():\n    return 1\n", "beta"},
		{"sample.js", "function gamma() { return 1; }\n", "gamma"},
	}
	for _, test := range cases {
		symbols, err := Extract(test.path, []byte(test.source))
		if err != nil {
			t.Fatalf("%s: %v", test.path, err)
		}
		found := false
		for _, symbol := range symbols {
			if symbol.Name == test.name {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: symbol %q absent from %#v", test.path, test.name, symbols)
		}
	}
}
