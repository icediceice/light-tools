//go:build treesitter

package symbol

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

const (
	parseWatchdog = 2 * time.Second
	signatureCap  = 240
)

func Extract(path string, source []byte) ([]Symbol, error) {
	language, err := languageFor(path)
	if err != nil {
		return nil, err
	}
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(language); err != nil {
		return nil, err
	}
	started := time.Now()
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter returned no tree")
	}
	defer tree.Close()
	if time.Since(started) > parseWatchdog {
		return nil, fmt.Errorf("tree-sitter parse exceeded %s watchdog", parseWatchdog)
	}

	seen := make(map[uint]bool)
	var symbols []Symbol
	walk(tree.RootNode(), source, "", seen, &symbols)
	sort.SliceStable(symbols, func(i, j int) bool { return symbols[i].StartByte < symbols[j].StartByte })
	return symbols, nil
}

func languageFor(path string) (*tree_sitter.Language, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return tree_sitter.NewLanguage(tree_sitter_go.Language()), nil
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return tree_sitter.NewLanguage(tree_sitter_javascript.Language()), nil
	case ".py":
		return tree_sitter.NewLanguage(tree_sitter_python.Language()), nil
	default:
		return nil, fmt.Errorf("no tree-sitter grammar for %s", filepath.Ext(path))
	}
}

func walk(node *tree_sitter.Node, source []byte, parent string, seen map[uint]bool, output *[]Symbol) {
	if node == nil {
		return
	}
	kind := classify(node.Kind())
	nextParent := parent
	if kind != "" {
		nameNode := node.ChildByFieldName("name")
		if nameNode == nil {
			nameNode = recursiveName(node)
		}
		if nameNode != nil {
			name := strings.TrimSpace(nameNode.Utf8Text(source))
			if usefulName(name) && !seen[node.StartByte()] {
				seen[node.StartByte()] = true
				derivedParent := parent
				if node.Kind() == "method_declaration" {
					if receiver := node.ChildByFieldName("receiver"); receiver != nil {
						derivedParent = strings.TrimSpace(receiver.Utf8Text(source))
					}
				}
				signature := firstLine(node.Utf8Text(source))
				*output = append(*output, Symbol{
					Name: name, Kind: kind, Signature: signature, Comment: leadingComment(node, source),
					Parent: derivedParent, StartLine: int(node.StartPosition().Row) + 1,
					EndLine: int(node.EndPosition().Row) + 1, StartByte: node.StartByte(), EndByte: node.EndByte(),
				})
				if kind == "class" || kind == "type" {
					nextParent = name
				}
			}
		}
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		walk(node.NamedChild(index), source, nextParent, seen, output)
	}
}

func recursiveName(node *tree_sitter.Node) *tree_sitter.Node {
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		switch child.Kind() {
		case "identifier", "type_identifier", "property_identifier":
			return child
		}
		if found := recursiveName(child); found != nil {
			return found
		}
	}
	return nil
}

func classify(kind string) string {
	switch kind {
	case "function_declaration", "function_definition":
		return "function"
	case "method_declaration", "method_definition":
		return "method"
	case "class_declaration", "class_definition":
		return "class"
	case "type_declaration", "type_spec":
		return "type"
	}
	return ""
}

func usefulName(name string) bool {
	if name == "" || name == "_" || len(name) > 256 {
		return false
	}
	switch name {
	case "init", "__init__", "main":
		return true
	}
	return !strings.HasPrefix(name, "<")
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	if len(line) > signatureCap {
		return line[:signatureCap] + "…"
	}
	return line
}

func leadingComment(node *tree_sitter.Node, source []byte) string {
	previous := node.PrevNamedSibling()
	if previous == nil || previous.Kind() != "comment" {
		return ""
	}
	if previous.EndPosition().Row+1 < node.StartPosition().Row {
		return ""
	}
	value := strings.TrimSpace(previous.Utf8Text(source))
	if len(value) > signatureCap {
		value = value[:signatureCap] + "…"
	}
	return value
}
