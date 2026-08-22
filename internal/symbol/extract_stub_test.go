//go:build !treesitter

package symbol

import "testing"

func TestGrammarExtractionIsUnavailableWithoutBuildTag(t *testing.T) {
	if _, err := Extract("main.go", []byte("package main")); err == nil {
		t.Fatal("grammar-backed extraction unexpectedly available without treesitter tag")
	}
}
