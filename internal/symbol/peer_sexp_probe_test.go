//go:build treesitter

package symbol

import (
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestPeerSexpDump(t *testing.T) {
	cases := []struct {
		lang   languageID
		source string
	}{
		{langCPP, "void Box::run() {}\nBox::Box() {}\nint* Box::data() { return 0; }\nvoid Ns::Box::z() {}\n"},
		{langLua, "function M.helper() end\nfunction M:method() end\nlocal f = function() end\n"},
		{langRust, "impl Item {\n  pub fn run(&self) {}\n}\nimpl Trait for Item { fn go(&self) {} }\n"},
	}
	for _, test := range cases {
		descriptor, ok := grammarFor(test.lang)
		if !ok {
			t.Fatalf("no grammar for %s", test.lang)
		}
		parser := tree_sitter.NewParser()
		if err := parser.SetLanguage(descriptor.language()); err != nil {
			t.Fatal(err)
		}
		tree := parser.Parse([]byte(test.source), nil)
		t.Logf("%s => %s", test.lang, tree.RootNode().ToSexp())
		tree.Close()
		parser.Close()
	}
}
