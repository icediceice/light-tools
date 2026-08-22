//go:build treesitter

package symbol

import (
	"fmt"
	"testing"
)

// Peer verify-ship acceptance probes, grammar lane.
// Assertions are RED today and encode expected post-fix behaviour;
// log-only probes document non-blocking observations.

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

// G1: a .cpp translation unit is mostly out-of-line member definitions.
func TestPeerProbeCppOutOfLineMembers(t *testing.T) {
	source := "class Box {\npublic:\n  void run();\n  Box();\n};\n\nvoid Box::run() {}\nBox::Box() {}\nint main() { return 0; }\n"
	symbols := peerDump(t, "box.cpp", source)
	if countSymbols(symbols, "run", KindMethod)+countSymbols(symbols, "run", KindFunction) == 0 {
		t.Errorf("out-of-line C++ member definition Box::run produced no symbol")
	}
}

// G2: Lua modules declare almost everything as function M.x / function M:x.
func TestPeerProbeLuaTableFunctions(t *testing.T) {
	source := "local M = {}\nfunction M.helper() end\nfunction M:method() end\nfunction plain() end\nreturn M\n"
	symbols := peerDump(t, "mod.lua", source)
	if countSymbols(symbols, "plain", KindFunction) != 1 {
		t.Errorf("plain Lua function regressed: %#v", symbols)
	}
	for _, name := range []string{"helper", "M.helper"} {
		if countSymbols(symbols, name, KindFunction)+countSymbols(symbols, name, KindMethod) > 0 {
			return
		}
	}
	t.Errorf("Lua table/method function declarations produced no symbol")
}

// G5: Rust inherent/trait impl methods must carry kind=method and the impl type.
func TestPeerProbeRustImplMethods(t *testing.T) {
	source := "struct Item;\nimpl Item {\n    pub fn run(&self) -> u32 { 1 }\n}\n"
	symbols := peerDump(t, "item.rs", source)
	for _, s := range symbols {
		if s.Name == "run" && s.Parent == "Item" && s.Kind == KindMethod {
			return
		}
	}
	t.Errorf("Rust impl method lost method kind and Item parent attribution: %#v", symbols)
}

// FOLLOW-UP probes — log only.

func TestPeerProbeGoGroupedTypeDocComment(t *testing.T) {
	peerDump(t, "g.go", "package p\n\ntype (\n\t// Alpha does a thing.\n\tAlpha int\n\t// Beta does another.\n\tBeta string\n)\n")
}

func TestPeerProbeExportedTSAndKotlinKt(t *testing.T) {
	peerDump(t, "m.ts", "export function run(): void {}\nexport default class Widget {}\nexport abstract class Base {}\n")
	peerDump(t, "m.kt", "class Box {\n    fun run() {}\n}\nobject App\nfun top() {}\n")
	peerDump(t, "m.scala", "package p\nclass Box {\n  def run(): Unit = {}\n}\ncase class Pair(a: Int)\n")
}

// G7 (FOLLOW-UP): a '>' inside an attribute value splits the start tag early.
func TestPeerProbeHTMLAttributeWithAngleBracket(t *testing.T) {
	peerDump(t, "p.html", "<button title=\"a > b\" id=\"save\">Save</button>\n")
}
