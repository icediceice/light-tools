package symbol

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type languageID string

const (
	langGo         languageID = "go"
	langJavaScript languageID = "javascript"
	langTypeScript languageID = "typescript"
	langTSX        languageID = "tsx"
	langPython     languageID = "python"
	langJava       languageID = "java"
	langRust       languageID = "rust"
	langC          languageID = "c"
	langCPP        languageID = "cpp"
	langCSharp     languageID = "csharp"
	langRuby       languageID = "ruby"
	langPHP        languageID = "php"
	langBash       languageID = "bash"
	langLua        languageID = "lua"
	langScala      languageID = "scala"
	langKotlin     languageID = "kotlin"
	langDart       languageID = "dart"
	langHTML       languageID = "html"
	langCSS        languageID = "css"
	langMarkdown   languageID = "markdown"
	langYAML       languageID = "yaml"
	langTOML       languageID = "toml"
)

type extensionInfo struct {
	language languageID
	text     bool
}

var extensionRegistry = map[string]extensionInfo{
	".go": {langGo, false},
	".js": {langJavaScript, false}, ".jsx": {langJavaScript, false},
	".mjs": {langJavaScript, false}, ".cjs": {langJavaScript, false},
	".ts": {langTypeScript, false}, ".tsx": {langTSX, false},
	".py": {langPython, false}, ".java": {langJava, false}, ".rs": {langRust, false},
	".c": {langC, false}, ".h": {langC, false},
	".cpp": {langCPP, false}, ".cc": {langCPP, false}, ".cxx": {langCPP, false}, ".hpp": {langCPP, false},
	".cs": {langCSharp, false}, ".rb": {langRuby, false}, ".php": {langPHP, false},
	".sh": {langBash, false}, ".bash": {langBash, false}, ".lua": {langLua, false},
	".scala": {langScala, false}, ".kt": {langKotlin, false}, ".kts": {langKotlin, false},
	".dart": {langDart, false}, ".html": {langHTML, false},
	".css": {langCSS, true}, ".md": {langMarkdown, true}, ".markdown": {langMarkdown, true},
	".yaml": {langYAML, true}, ".yml": {langYAML, true}, ".toml": {langTOML, true},
}

func extensionFor(path string) (extensionInfo, error) {
	ext := strings.ToLower(filepath.Ext(path))
	info, ok := extensionRegistry[ext]
	if !ok {
		return extensionInfo{}, fmt.Errorf("%w: %s", ErrUnsupportedExtension, ext)
	}
	return info, nil
}

func supportedExtensions() []string {
	extensions := make([]string, 0, len(extensionRegistry))
	for ext := range extensionRegistry {
		extensions = append(extensions, ext)
	}
	sort.Strings(extensions)
	return extensions
}

func truncateUTF8(value string, capBytes int) string {
	if len(value) <= capBytes {
		return value
	}
	end := capBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "…"
}
