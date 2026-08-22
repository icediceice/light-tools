package symbol

import (
	"fmt"
	"testing"
)

// Peer verify-ship acceptance probes, untagged (pure-text) lane.
// The assertions below are RED today and encode the expected post-fix
// behaviour; log-only probes document non-blocking observations.

func TestPeerProbeTextLanes(t *testing.T) {
	cases := []struct {
		path   string
		source string
	}{
		{"ci.yml", "name: CI\non:\n  push:\njobs:\n  build:\n    runs-on: ubuntu-24.04\n"},
		{"theme.css", "/* c */\n.card { color: #fff; background: url(logo.svg); }\n#main { margin: 1.5em; }\n"},
		{"doc.md", "# Title\n\n```sh\n# not a heading\n```\n\n## Real\n"},
		{"conf.toml", "[server]\nport = 8080\n"},
	}
	for _, test := range cases {
		symbols, err := Extract(test.path, []byte(test.source))
		if err != nil {
			t.Fatalf("%s: %v", test.path, err)
		}
		got := make([]string, 0, len(symbols))
		for _, s := range symbols {
			got = append(got, fmt.Sprintf("%s/%s@L%d", s.Kind, s.Name, s.StartLine))
		}
		t.Logf("%s -> %v", test.path, got)
	}
}

// G3: top-level YAML keys that open a block (the structural keys of every
// real config file) must be addressable, not only "key: value" leaves.
func TestPeerProbeYAMLBlockKeys(t *testing.T) {
	source := []byte("name: CI\non:\n  push:\njobs:\n  build:\n")
	symbols, err := Extract("ci.yml", source)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"name": false, "on": false, "jobs": false}
	for _, s := range symbols {
		if _, ok := want[s.Name]; ok {
			want[s.Name] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("top-level YAML key %q absent from %#v", key, symbols)
		}
	}
}

// G4: hex colours and url() file extensions are not CSS selectors.
func TestPeerProbeCSSHexColorIsNotASymbol(t *testing.T) {
	source := []byte(".card { color: #fff; background: url(logo.svg); }\n")
	symbols, err := Extract("theme.css", source)
	if err != nil {
		t.Fatal(err)
	}
	if countTextSymbols(symbols, "card", KindCSSClass) != 1 {
		t.Errorf("real selector .card lost: %#v", symbols)
	}
	for _, s := range symbols {
		if s.Kind == KindCSSID && s.Name == "fff" {
			t.Errorf("hex colour #fff emitted as css_id: %#v", s)
		}
		if s.Kind == KindCSSClass && s.Name == "svg" {
			t.Errorf("url extension .svg emitted as css_class: %#v", s)
		}
	}
}

// G9 (FOLLOW-UP, log only): a value carrying invalid UTF-8 before the cut
// collapses to a bare ellipsis instead of a truncated prefix.
func TestPeerProbeTruncateInvalidUTF8Input(t *testing.T) {
	value := string([]byte{0xff, 0xfe}) + "abcdefghij"
	t.Logf("truncateUTF8(invalid-utf8, 4) = %q", truncateUTF8(value, 4))
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
