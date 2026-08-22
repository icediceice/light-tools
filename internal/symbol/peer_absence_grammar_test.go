//go:build treesitter

package symbol

import "testing"

func TestPeerProbeCppOutOfLineMembers(t *testing.T) {
	source := `class Box {
public:
  void run();
  Box();
  ~Box();
  Box operator+(const Box&) const;
  int* data();
  int& ref();
};
void Box::run() {}
Box::Box() {}
Box::~Box() {}
Box Box::operator+(const Box&) const { return *this; }
int* Box::data() { return nullptr; }
int& Box::ref() { static int value; return value; }
namespace Ns { class Box { public: void z(); }; }
void Ns::Box::z() {}
int main() { return 0; }
`
	symbols, err := Extract("box.cpp", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name   string
		parent string
	}{
		{"run", "Box"},
		{"Box", "Box"},
		{"~Box", "Box"},
		{"operator+", "Box"},
		{"data", "Box"},
		{"ref", "Box"},
		{"z", "Box"},
	} {
		if !hasSymbol(symbols, want.name, KindMethod, want.parent) {
			t.Errorf("missing C++ method %q with parent %q: %#v", want.name, want.parent, symbols)
		}
	}
	if countSymbols(symbols, "main", KindFunction) != 1 {
		t.Errorf("free C++ function regressed: %#v", symbols)
	}
}

func TestPeerProbeLuaTableFunctions(t *testing.T) {
	source := "local M = {}\nfunction M.helper() end\nfunction M:method() end\nlocal assigned = function() end\nfunction plain() end\nreturn M\n"
	symbols, err := Extract("mod.lua", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"helper", "method"} {
		if !hasSymbol(symbols, name, KindMethod, "M") {
			t.Errorf("missing Lua table method %q: %#v", name, symbols)
		}
	}
	for _, name := range []string{"assigned", "plain"} {
		if !hasSymbol(symbols, name, KindFunction, "") {
			t.Errorf("missing Lua function %q: %#v", name, symbols)
		}
	}
}

func TestPeerProbeRustImplMethods(t *testing.T) {
	source := "trait Named {}\nstruct Item;\nimpl Item { pub fn inherent(&self) -> u32 { 1 } }\nimpl Named for Item { fn trait_method(&self) {} }\nfn free() {}\n"
	symbols, err := Extract("item.rs", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inherent", "trait_method"} {
		if !hasSymbol(symbols, name, KindMethod, "Item") {
			t.Errorf("Rust impl method %q lost Item parent attribution: %#v", name, symbols)
		}
	}
	if !hasSymbol(symbols, "free", KindFunction, "") {
		t.Errorf("free Rust function regressed: %#v", symbols)
	}
}

func hasSymbol(symbols []Symbol, name, kind, parent string) bool {
	for _, symbol := range symbols {
		if symbol.Name == name && symbol.Kind == kind && symbol.Parent == parent {
			return true
		}
	}
	return false
}
