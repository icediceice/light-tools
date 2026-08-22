//go:build treesitter

package symbol

import (
	"fmt"
	"sort"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func Extract(path string, source []byte) ([]Symbol, error) {
	if symbols, ok := extractText(path, source); ok {
		return symbols, nil
	}
	info, err := extensionFor(path)
	if err != nil {
		return nil, err
	}
	descriptor, ok := grammarFor(info.language)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedExtension, info.language)
	}
	if longest := maxLineBytes(source); longest > parseMaxLineBytes {
		return nil, fmt.Errorf("%w: longest line is %d bytes (limit %d)", ErrParseHostile, longest, parseMaxLineBytes)
	}
	return runParserWork(parseDeadline, func() ([]Symbol, error) {
		return extractGrammar(descriptor, source)
	})
}

func extractGrammar(descriptor grammarDescriptor, source []byte) ([]Symbol, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	language := descriptor.language()
	if err := parser.SetLanguage(language); err != nil {
		return nil, fmt.Errorf("set %s grammar: %w", descriptor.id, err)
	}
	parser.SetTimeoutMicros(parseTimeoutMicros)
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("%w: %s", ErrParseTimeout, descriptor.id)
	}
	defer tree.Close()
	if descriptor.special != nil {
		symbols := descriptor.special(tree.RootNode(), source)
		sortSymbols(symbols)
		return symbols, nil
	}
	query, err := tree_sitter.NewQuery(language, descriptor.query)
	if err != nil {
		return nil, fmt.Errorf("compile %s symbol query: %w", descriptor.id, err)
	}
	defer query.Close()

	nameIndex, nameOK := query.CaptureIndexForName("name")
	bodyIndex, bodyOK := query.CaptureIndexForName("body")
	if !nameOK || !bodyOK {
		return nil, fmt.Errorf("compile %s symbol query: required captures absent", descriptor.id)
	}
	cursor := tree_sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(query, tree.RootNode(), source)
	seen := make(map[string]bool)
	var symbols []Symbol
	for match := matches.Next(); match != nil; match = matches.Next() {
		var nameNode, bodyNode *tree_sitter.Node
		for index := range match.Captures {
			capture := &match.Captures[index]
			switch uint(capture.Index) {
			case nameIndex:
				nameNode = &capture.Node
			case bodyIndex:
				bodyNode = &capture.Node
			}
		}
		if nameNode == nil || bodyNode == nil {
			continue
		}
		name := normalizeCapturedName(nameNode, source)
		kind := descriptor.kinds[bodyNode.Kind()]
		if name == "" || !ValidKind(kind) {
			continue
		}
		if descriptor.noise[bodyNode.Kind()] && len([]rune(name)) <= 1 {
			continue
		}
		identity := fmt.Sprintf("%s:%d:%d:%s", kind, nameNode.StartByte(), nameNode.EndByte(), name)
		if seen[identity] {
			continue
		}
		seen[identity] = true

		emitNode := bodyNode
		if descriptor.id == langGo && bodyNode.Kind() == "type_spec" {
			if parent := bodyNode.Parent(); parent != nil && parent.Kind() == "type_declaration" && parent.NamedChildCount() == 1 {
				emitNode = parent
			}
		}
		parent := parentName(bodyNode, source)
		if descriptor.id == langGo && bodyNode.Kind() == "method_declaration" {
			parent = extractGoReceiver(firstLine(emitNode.Utf8Text(source)))
		}
		if parent != "" && kind == KindFunction {
			kind = KindMethod
		}
		symbol := symbolFromNode(name, kind, parent, emitNode, source)
		if symbol.StartByte < symbol.EndByte {
			symbols = append(symbols, symbol)
		}
	}
	sortSymbols(symbols)
	return symbols, nil
}

func normalizeCapturedName(node *tree_sitter.Node, source []byte) string {
	name := strings.TrimSpace(node.Utf8Text(source))
	switch node.Kind() {
	case "string", "template_string":
		if len(name) >= 2 {
			first, last := name[0], name[len(name)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
				name = name[1 : len(name)-1]
			}
		}
	}
	if name == "_" || len(name) > 256 || strings.HasPrefix(name, "<") {
		return ""
	}
	return name
}

func symbolFromNode(name, kind, parent string, node *tree_sitter.Node, source []byte) Symbol {
	return Symbol{
		Name: name, Kind: kind, Signature: firstLine(node.Utf8Text(source)),
		Comment: leadingComment(node, source), Parent: parent,
		StartLine: int(node.StartPosition().Row) + 1, EndLine: int(node.EndPosition().Row) + 1,
		StartByte: node.StartByte(), EndByte: node.EndByte(),
	}
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	return truncateUTF8(line, 240)
}

func leadingComment(node *tree_sitter.Node, source []byte) string {
	previous := node.PrevNamedSibling()
	if previous == nil || previous.Kind() != "comment" {
		return ""
	}
	if previous.EndPosition().Row+1 < node.StartPosition().Row {
		return ""
	}
	return truncateUTF8(strings.TrimSpace(previous.Utf8Text(source)), 500)
}

func parentName(node *tree_sitter.Node, source []byte) string {
	for ancestor := node.Parent(); ancestor != nil; ancestor = ancestor.Parent() {
		switch ancestor.Kind() {
		case "class_declaration", "class_definition", "class", "module", "object_definition",
			"object_declaration", "trait_definition", "interface_declaration", "struct_item",
			"class_specifier", "struct_specifier":
			if name := directName(ancestor, source); name != "" {
				return name
			}
		}
	}
	return ""
}

func directName(node *tree_sitter.Node, source []byte) string {
	if name := node.ChildByFieldName("name"); name != nil {
		return strings.TrimSpace(name.Utf8Text(source))
	}
	for index := uint(0); index < node.NamedChildCount(); index++ {
		child := node.NamedChild(index)
		switch child.Kind() {
		case "identifier", "type_identifier", "constant":
			return strings.TrimSpace(child.Utf8Text(source))
		}
	}
	return ""
}

func extractGoReceiver(signature string) string {
	if !strings.HasPrefix(signature, "func (") {
		return ""
	}
	rest := signature[6:]
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return ""
	}
	parts := strings.Fields(strings.TrimSpace(rest[:end]))
	if len(parts) == 0 {
		return ""
	}
	receiver := strings.TrimPrefix(parts[len(parts)-1], "*")
	if bracket := strings.IndexByte(receiver, '['); bracket >= 0 {
		receiver = receiver[:bracket]
	}
	return receiver
}

func sortSymbols(symbols []Symbol) {
	sort.SliceStable(symbols, func(left, right int) bool {
		if symbols[left].StartByte != symbols[right].StartByte {
			return symbols[left].StartByte < symbols[right].StartByte
		}
		if symbols[left].EndByte != symbols[right].EndByte {
			return symbols[left].EndByte < symbols[right].EndByte
		}
		if symbols[left].Kind != symbols[right].Kind {
			return symbols[left].Kind < symbols[right].Kind
		}
		return symbols[left].Name < symbols[right].Name
	})
}
