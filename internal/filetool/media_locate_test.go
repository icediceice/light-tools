package filetool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/mcp"
)

func TestImageReadBlockAndLargeDegrade(t *testing.T) {
	handler, root := newTestHandler(t)
	small := filepath.Join(root, "small.png")
	if err := os.WriteFile(small, []byte("\x89PNG\r\n\x1a\nbytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := handler.read(context.Background(), Request{Verb: "read", Path: small})
	if err != nil {
		t.Fatal(err)
	}
	result := value.(mcp.Result)
	if len(result.Content) != 1 || result.Content[0].Type != "image" || result.Content[0].MIMEType != "image/png" {
		t.Fatalf("small image did not emit image content: %#v", result)
	}

	large := filepath.Join(root, "large.webp")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(imageLimit + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	value, err = handler.read(context.Background(), Request{Verb: "read", Path: large})
	if err != nil {
		t.Fatal(err)
	}
	result = value.(mcp.Result)
	if result.Content[0].Type != "text" || !strings.Contains(result.Content[0].Text, "exceeds 9 MiB") {
		t.Fatalf("large image did not degrade: %#v", result)
	}
}

func TestDirectoryLocateAndInvalidRegexFallback(t *testing.T) {
	handler, root := newTestHandler(t)
	first := filepath.Join(root, "a.txt")
	second := filepath.Join(root, "nested", "b.txt")
	if err := os.MkdirAll(filepath.Dir(second), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(first, []byte("before\nneedle here\nafter\n"), 0o600)
	os.WriteFile(second, []byte("another needle\n"), 0o600)
	result := invokeRequest(t, handler, map[string]any{
		"verb": "locate", "path": root, "pattern": "needle", "context": 1,
	})
	if strings.Count(result, `"line":`) != 2 || !strings.Contains(result, "directory locate uses bounded fixed-string") {
		t.Fatalf("directory locate failed: %s", result)
	}

	os.WriteFile(first, []byte("literal [ token\n"), 0o600)
	result = invokeRequest(t, handler, map[string]any{
		"verb": "locate", "path": first, "pattern": "[",
	})
	if !strings.Contains(result, "invalid regex retried once as fixed string") || !strings.Contains(result, "literal [ token") {
		t.Fatalf("invalid regex fallback failed: %s", result)
	}
}
