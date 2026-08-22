//go:build treesitter

package symbol

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestRegistryQueriesCompile(t *testing.T) {
	for id, descriptor := range grammarRegistry {
		if descriptor.query == "" {
			if descriptor.special == nil {
				t.Errorf("%s has neither query nor special extractor", id)
			}
			continue
		}
		query, err := tree_sitter.NewQuery(descriptor.language(), descriptor.query)
		if err != nil {
			t.Errorf("%s query: %v", id, err)
			continue
		}
		query.Close()
	}
}

func TestSemanticParityCorpus(t *testing.T) {
	tests := []struct {
		path   string
		source string
		want   map[string]string
	}{
		{"sample.go", "package p\ntype (\n Foo int\n Bar string\n)\nfunc Run() {}\n", map[string]string{"Foo": KindType, "Bar": KindType, "Run": KindFunction}},
		{"sample.js", "class Box {}\nconst f = () => 1;\ndescribe(\"renders badge\", () => {});\n", map[string]string{"Box": KindClass, "f": KindFunction, "renders badge": KindFunction}},
		{"sample.ts", "interface Props { label: string }\nenum Mode { On }\nconst make = (): Props => ({label: 'x'});\n", map[string]string{"Props": KindInterface, "Mode": KindEnum, "make": KindFunction}},
		{"sample.tsx", "interface Props { label: string }\nexport const Badge = (p: Props) => <span>{p.label}</span>;\n", map[string]string{"Props": KindInterface, "Badge": KindFunction}},
		{"sample.py", "class Box:\n    def run(self):\n        return 1\n", map[string]string{"Box": KindClass, "run": KindMethod}},
		{"sample.java", "class Box { void run() {} }\ninterface Item {}\nenum Mode { ON }\nrecord Pair(int x) {}\n", map[string]string{"Box": KindClass, "run": KindMethod, "Item": KindInterface, "Mode": KindEnum, "Pair": KindRecord}},
		{"sample.rs", "struct Item {}\ntrait Named {}\nfn run() {}\n", map[string]string{"Item": KindStruct, "Named": KindTrait, "run": KindFunction}},
		{"sample.c", "struct Item { int x; };\nint run(void) { return 1; }\n", map[string]string{"Item": KindStruct, "run": KindFunction}},
		{"sample.cpp", "class Box {};\nstruct Item {};\nint run() { return 1; }\n", map[string]string{"Box": KindClass, "Item": KindStruct, "run": KindFunction}},
		{"sample.cs", "class Box { void Run() {} }\ninterface Item {}\nenum Mode { On }\n", map[string]string{"Box": KindClass, "Run": KindMethod, "Item": KindInterface, "Mode": KindEnum}},
		{"sample.rb", "class C\n  def f\n  end\nend\n", map[string]string{"C": KindClass, "f": KindMethod}},
		{"sample.php", "<?php\nfunction run() {}\nclass Box {}\ninterface Item {}\n", map[string]string{"run": KindFunction, "Box": KindClass, "Item": KindInterface}},
		{"sample.sh", "f() { :; }\n", map[string]string{"f": KindFunction}},
		{"sample.lua", "local function f() end\n", map[string]string{"f": KindFunction}},
		{"sample.scala", "class Box\ntrait Item\nobject App\ndef run(): Unit = {}\n", map[string]string{"Box": KindClass, "Item": KindTrait, "App": KindObject, "run": KindFunction}},
		{"sample.kt", "class Box { fun run() {} }\nobject App\n", map[string]string{"Box": KindClass, "run": KindFunction, "App": KindObject}},
		{"sample.dart", "class Box { void f() {} }\nvoid g() {}\n", map[string]string{"Box": KindClass, "f": KindMethod, "g": KindFunction}},
		{"sample.html", "<button id=\"save\" name=\"action\" onclick=\"app.save()\">Save</button>\n", map[string]string{"save": KindHTMLID, "action": KindHTMLName, "app.save": KindHTMLHandler, "Save": KindHTMLText}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			source := []byte(test.source)
			first, err := Extract(test.path, source)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			second, err := Extract(test.path, source)
			if err != nil {
				t.Fatalf("repeat Extract: %v", err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("nondeterministic output:\n%#v\n%#v", first, second)
			}
			for _, symbol := range first {
				assertSymbolInvariant(t, source, symbol)
			}
			for name, kind := range test.want {
				if countSymbols(first, name, kind) != 1 {
					t.Errorf("want exactly one %s %q, got %#v", kind, name, first)
				}
			}
		})
	}
}

func TestExtensionMatrix(t *testing.T) {
	want := strings.Fields(".bash .c .cc .cpp .cs .css .cxx .dart .go .h .hpp .html .java .js .jsx .kt .kts .lua .markdown .md .mjs .cjs .php .py .rb .rs .scala .sh .toml .ts .tsx .yaml .yml")
	got := supportedExtensions()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extensions = %#v; want %#v", got, want)
	}
	if _, err := extensionFor("page.htm"); !errors.Is(err, ErrUnsupportedExtension) {
		t.Fatalf(".htm error = %v", err)
	}
}

func TestUTF8TruncationAndGroupedGoTypes(t *testing.T) {
	value := strings.Repeat("x", 238) + "🧊" + strings.Repeat("y", 20)
	truncated := truncateUTF8(value, 240)
	if !utf8.ValidString(truncated) || !strings.HasSuffix(truncated, "…") {
		t.Fatalf("invalid truncation %q", truncated)
	}
	source := []byte("package p\ntype (\n A int\n B string\n)\n")
	symbols, err := Extract("types.go", source)
	if err != nil {
		t.Fatal(err)
	}
	if countSymbols(symbols, "A", KindType) != 1 || countSymbols(symbols, "B", KindType) != 1 {
		t.Fatalf("grouped types = %#v", symbols)
	}
}

func TestWatchdogCircuit(t *testing.T) {
	release := make(chan struct{})
	_, err := runParserWork(20*time.Millisecond, func() ([]Symbol, error) {
		<-release
		return nil, nil
	})
	if !errors.Is(err, ErrParseTimeout) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := runParserWork(time.Second, func() ([]Symbol, error) { return nil, nil }); !errors.Is(err, ErrParseBusy) {
		t.Fatalf("second error = %v", err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		symbols, err := runParserWork(time.Second, func() ([]Symbol, error) {
			return []Symbol{{Name: "ok"}}, nil
		})
		if err == nil {
			if len(symbols) != 1 {
				t.Fatalf("recovered symbols = %#v", symbols)
			}
			break
		}
		if !errors.Is(err, ErrParseBusy) || time.Now().After(deadline) {
			t.Fatalf("circuit did not recover: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	hostile := []byte(strings.Repeat("x", parseMaxLineBytes+1))
	if _, err := Extract("hostile.ts", hostile); !errors.Is(err, ErrParseHostile) {
		t.Fatalf("hostile error = %v", err)
	}
}

func assertSymbolInvariant(t *testing.T, source []byte, symbol Symbol) {
	t.Helper()
	if symbol.Name == "" || !ValidKind(symbol.Kind) {
		t.Errorf("invalid identity %#v", symbol)
	}
	if symbol.StartByte >= symbol.EndByte || int(symbol.EndByte) > len(source) {
		t.Errorf("invalid byte range %#v for %d bytes", symbol, len(source))
	}
	if symbol.StartLine < 1 || symbol.StartLine > symbol.EndLine {
		t.Errorf("invalid line range %#v", symbol)
	}
	if !utf8.ValidString(symbol.Signature) || !utf8.ValidString(symbol.Comment) {
		t.Errorf("invalid UTF-8 %#v", symbol)
	}
}

func countSymbols(symbols []Symbol, name, kind string) int {
	count := 0
	for _, symbol := range symbols {
		if symbol.Name == name && symbol.Kind == kind {
			count++
		}
	}
	return count
}
