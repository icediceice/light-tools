//go:build treesitter

package symbol

import (
	"regexp"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

var (
	htmlAttributeRE = regexp.MustCompile(`(?i)\b(id|name|onclick)\s*=\s*["']([^"']+)["']`)
	htmlTagRE       = regexp.MustCompile(`(?i)^\s*<\s*([a-z][a-z0-9-]*)`)
	htmlHandlerRE   = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$.]*`)
)

var htmlTextTags = map[string]bool{
	"button": true, "label": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "a": true, "title": true,
}

func extractHTMLSymbols(root *tree_sitter.Node, source []byte) []Symbol {
	seen := make(map[string]bool)
	var symbols []Symbol
	var walk func(*tree_sitter.Node)
	walk = func(node *tree_sitter.Node) {
		if node == nil {
			return
		}
		if node.Kind() == "element" || node.Kind() == "script_element" || node.Kind() == "style_element" {
			raw := node.Utf8Text(source)
			tagEnd := strings.IndexByte(raw, '>')
			if tagEnd >= 0 {
				startTag := raw[:tagEnd+1]
				tag := ""
				if match := htmlTagRE.FindStringSubmatch(startTag); match != nil {
					tag = strings.ToLower(match[1])
				}
				emit := func(kind, name string) {
					name = strings.TrimSpace(name)
					key := kind + "\x00" + name + "\x00" + string(rune(node.StartByte()))
					if name == "" || seen[key] {
						return
					}
					seen[key] = true
					symbol := symbolFromNode(name, kind, "", node, source)
					symbol.Signature = truncateUTF8(strings.TrimSpace(startTag), 240)
					symbols = append(symbols, symbol)
				}
				for _, match := range htmlAttributeRE.FindAllStringSubmatch(startTag, -1) {
					switch strings.ToLower(match[1]) {
					case "id":
						emit(KindHTMLID, match[2])
					case "name":
						emit(KindHTMLName, match[2])
					case "onclick":
						for _, handler := range htmlHandlerRE.FindAllString(match[2], -1) {
							emit(KindHTMLHandler, handler)
						}
					}
				}
				if htmlTextTags[tag] {
					rest := raw[tagEnd+1:]
					if textEnd := strings.IndexByte(rest, '<'); textEnd >= 0 {
						text := strings.TrimSpace(rest[:textEnd])
						if text != "" && len([]rune(text)) <= 120 {
							emit(KindHTMLText, text)
						}
					}
				}
			}
		}
		for index := uint(0); index < node.NamedChildCount(); index++ {
			walk(node.NamedChild(index))
		}
	}
	walk(root)
	return symbols
}

func extractDartSymbols(root *tree_sitter.Node, source []byte) []Symbol {
	seen := make(map[string]bool)
	var symbols []Symbol
	var walk func(*tree_sitter.Node, string)
	walk = func(node *tree_sitter.Node, enclosing string) {
		if node == nil {
			return
		}
		kind := ""
		name := ""
		nextEnclosing := enclosing
		switch node.Kind() {
		case "class_definition":
			kind = KindClass
		case "mixin_declaration":
			kind = KindTrait
		case "extension_declaration":
			kind = KindObject
		case "enum_declaration":
			kind = KindEnum
		case "type_alias":
			kind = KindType
		case "function_signature":
			if enclosing == "" {
				kind = KindFunction
			} else {
				kind = KindMethod
			}
		case "getter_signature", "setter_signature", "constructor_signature",
			"factory_constructor_signature", "redirecting_factory_constructor_signature":
			kind = KindMethod
		case "operator_signature":
			kind, name = KindMethod, "operator"
		case "initialized_variable_definition":
			if enclosing == "" {
				kind = KindConst
			}
		}
		if kind != "" {
			if name == "" {
				name = directName(node, source)
			}
			if name != "" {
				key := kind + "\x00" + name + "\x00" + string(rune(node.StartByte()))
				if !seen[key] {
					seen[key] = true
					symbol := symbolFromNode(name, kind, enclosing, node, source)
					if symbol.StartByte < symbol.EndByte {
						symbols = append(symbols, symbol)
					}
				}
				switch kind {
				case KindClass, KindTrait, KindObject, KindEnum:
					nextEnclosing = name
				}
			}
		}
		for index := uint(0); index < node.NamedChildCount(); index++ {
			walk(node.NamedChild(index), nextEnclosing)
		}
	}
	walk(root, "")
	return symbols
}
