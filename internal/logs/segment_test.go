package logs

import (
	"reflect"
	"testing"
)

// splitRawLines numbering must agree with read_block's, or every [lo-hi] the
// outline prints addresses the wrong line in the spill.
func TestSplitRawLinesNumbersLikeReadBlock(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"single trailing newline terminates, not adds", "a\nb\n", []string{"a", "b"}},
		{"no trailing newline", "a\nb", []string{"a", "b"}},
		{"a genuine trailing blank line is kept", "a\nb\n\n", []string{"a", "b", ""}},
		{"interior blanks are addressable", "a\n\nb\n", []string{"a", "", "b"}},
		{"lone newline is one empty line", "\n", []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := splitRawLines(c.raw); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("splitRawLines(%q) = %#v, want %#v", c.raw, got, c.want)
			}
		})
	}
}
