//go:build treesitter

package symbol

import (
	"fmt"
	"testing"
)

func peerDump(t *testing.T, path, source string) []Symbol {
	t.Helper()
	symbols, err := Extract(path, []byte(source))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	got := make([]string, 0, len(symbols))
	for _, s := range symbols {
		got = append(got, fmt.Sprintf("%s/%s(parent=%q)@L%d-%d", s.Kind, s.Name, s.Parent, s.StartLine, s.EndLine))
	}
	t.Logf("%s -> %v", path, got)
	return symbols
}

func TestPeerProbeCppOutOfLineMembers(t *testing.T) {
	source := "class Box {\npublic:\n  void run();\n  Box();\n};\n\nvoid Box::run() {}\nBox::Box() {}\nint main() { return 0; }\n"
	symbols := peerDump(t, "box.cpp", source)
	if countSymbols(symbols, "run", KindMethod)+countSymbols(symbols, "run", KindFunction) == 0 {
		t.Errorf("out-of-line C++ member definition Box::run produced no symbol")
	}
}

func TestPeerProbeLuaTableFunctions(t *testing.T) {
	source := "local M = {}\nfunction M.helper() end\nfunction M:method() end\nfunction plain() end\nreturn M\n"
	symbols := peerDump(t, "mod.lua", source)
	for _, name := range []string{"helper", "M.helper", "method", "M:method"} {
		if countSymbols(symbols, name, KindFunction)+countSymbols(symbols, name, KindMethod) > 0 {
			return
		}
	}
	t.Errorf("Lua table/method function declarations produced no symbol")
}

func TestPeerProbeRustImplMethods(t *testing.T) {
	source := "struct Item;\nimpl Item {\n    pub fn run(&self) -> u32 { 1 }\n}\n"
	symbols := peerDump(t, "item.rs", source)
	for _, s := range symbols {
		if s.Name == "run" && s.Parent == "Item" {
			return
		}
	}
	t.Errorf("Rust impl method lost its Item parent attribution")
}

func TestPeerProbeGoGroupedTypeDocComment(t *testing.T) {
	source := "package p\n\ntype (\n\t// Alpha does a thing.\n\tAlpha int\n\t// Beta does another.\n\tBeta string\n)\n"
	symbols := peerDump(t, "g.go", source)
	for _, s := range symbols {
		if s.Name == "Alpha" && s.Comment == "" {
			t.Errorf("grouped Go type Alpha lost its doc comment: %#v", s)
		}
	}
}

func TestPeerProbeExportedTSAndKotlinKt(t *testing.T) {
	peerDump(t, "m.ts", "export function run(): void {}\nexport default class Widget {}\nexport abstract class Base {}\n")
	peerDump(t, "m.kt", "class Box {\n    fun run() {}\n}\nobject App\nfun top() {}\n")
	peerDump(t, "m.scala", "package p\nclass Box {\n  def run(): Unit = {}\n}\ncase class Pair(a: Int)\n")
}

func TestPeerProbeHTMLAttributeWithAngleBracket(t *testing.T) {
	source := "<button title=\"a > b\" id=\"save\">Save</button>\n"
	symbols := peerDump(t, "p.html", source)
	if countSymbols(symbols, "save", KindHTMLID) != 1 {
		t.Errorf("html id lost when an attribute value contains '>': %#v", symbols)
	}
}
