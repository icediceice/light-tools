package symbol

import "testing"

func TestPeerProbeYAMLBlockKeys(t *testing.T) {
	source := []byte("name: CI\non:\n  push:\njobs:\n  build:\n")
	symbols, err := Extract("ci.yml", source)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"name": false, "on": false, "jobs": false}
	for _, symbol := range symbols {
		if _, ok := want[symbol.Name]; ok {
			want[symbol.Name] = true
		}
		if symbol.Name == "push" || symbol.Name == "build" {
			t.Errorf("nested YAML key %q should remain outside the top-level lane", symbol.Name)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("top-level YAML key %q absent from %#v", key, symbols)
		}
	}
}

func TestPeerProbeCSSDeclarationsAreNotSymbols(t *testing.T) {
	source := []byte(".card {\n  color: #fff;\n  background: url(logo.svg);\n}\n@media (min-width: 10px) {\n  #main { color: #abc; }\n}\n")
	symbols, err := Extract("theme.css", source)
	if err != nil {
		t.Fatal(err)
	}
	if countTextSymbols(symbols, "card", KindCSSClass) != 1 || countTextSymbols(symbols, "main", KindCSSID) != 1 {
		t.Errorf("real selectors lost: %#v", symbols)
	}
	for _, symbol := range symbols {
		if (symbol.Kind == KindCSSID && (symbol.Name == "fff" || symbol.Name == "abc")) ||
			(symbol.Kind == KindCSSClass && symbol.Name == "svg") {
			t.Errorf("CSS declaration value emitted as a selector: %#v", symbol)
		}
	}
}

func countTextSymbols(symbols []Symbol, name, kind string) int {
	count := 0
	for _, symbol := range symbols {
		if symbol.Name == name && symbol.Kind == kind {
			count++
		}
	}
	return count
}
