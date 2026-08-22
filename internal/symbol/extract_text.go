package symbol

import (
	"bytes"
	"regexp"
	"strings"
)

var (
	cssClassRE    = regexp.MustCompile(`\.([a-zA-Z][a-zA-Z0-9_-]*)`)
	cssIDRE       = regexp.MustCompile(`#([a-zA-Z][a-zA-Z0-9_-]*)`)
	cssKeyframeRE = regexp.MustCompile(`@keyframes\s+([a-zA-Z][a-zA-Z0-9_-]*)`)
	cssElementRE  = regexp.MustCompile(`^\s*(html|body|\*|:root)\s*[{,]`)
	markdownRE    = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	yamlRE        = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_-]*):(\s|$)`)
	tomlSectionRE = regexp.MustCompile(`^\[([^\]]+)\]`)
	tomlKeyRE     = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_.-]*)\s*=`)
)

type sourceLine struct {
	number int
	start  int
	end    int
	text   string
}

func splitSourceLines(source []byte) []sourceLine {
	raw := bytes.Split(source, []byte{'\n'})
	lines := make([]sourceLine, 0, len(raw))
	offset := 0
	for index, part := range raw {
		end := offset + len(part)
		textEnd := end
		if len(part) > 0 && part[len(part)-1] == '\r' {
			textEnd--
		}
		lines = append(lines, sourceLine{
			number: index + 1,
			start:  offset,
			end:    textEnd,
			text:   string(source[offset:textEnd]),
		})
		offset = end + 1
	}
	return lines
}

func textSymbol(kind, name string, line sourceLine) Symbol {
	return Symbol{
		Name: name, Kind: kind, Signature: truncateUTF8(strings.TrimSpace(line.text), 240),
		StartLine: line.number, EndLine: line.number,
		StartByte: uint(line.start), EndByte: uint(line.end),
	}
}

func extractText(path string, source []byte) ([]Symbol, bool) {
	info, err := extensionFor(path)
	if err != nil || !info.text {
		return nil, false
	}
	lines := splitSourceLines(source)
	var symbols []Symbol
	seen := make(map[string]bool)
	add := func(kind, name string, line sourceLine) {
		name = strings.TrimSpace(name)
		if name == "" || line.end <= line.start || !ValidKind(kind) {
			return
		}
		key := kind + "\x00" + name
		if (info.language == langCSS) && seen[key] {
			return
		}
		seen[key] = true
		symbols = append(symbols, textSymbol(kind, name, line))
	}
	cssDepth := 0
	for _, line := range lines {
		switch info.language {
		case langCSS:
			selector := ""
			if opening := strings.IndexByte(line.text, '{'); opening >= 0 {
				selector = line.text[:opening]
			} else if cssDepth == 0 {
				selector = line.text
			}
			cssDepth += strings.Count(line.text, "{") - strings.Count(line.text, "}")
			if cssDepth < 0 {
				cssDepth = 0
			}
			if selector == "" {
				continue
			}
			for _, match := range cssKeyframeRE.FindAllStringSubmatch(selector, -1) {
				add(KindCSSKeyframes, match[1], line)
			}
			for _, match := range cssClassRE.FindAllStringSubmatch(selector, -1) {
				add(KindCSSClass, match[1], line)
			}
			for _, match := range cssIDRE.FindAllStringSubmatch(selector, -1) {
				add(KindCSSID, match[1], line)
			}
			if match := cssElementRE.FindStringSubmatch(line.text); match != nil {
				add(KindCSSElement, match[1], line)
			}
		case langMarkdown:
			if match := markdownRE.FindStringSubmatch(line.text); match != nil {
				add(KindMDHeading, match[1], line)
			}
		case langYAML:
			if match := yamlRE.FindStringSubmatch(line.text); match != nil {
				add(KindYAMLKey, match[1], line)
			}
		case langTOML:
			if match := tomlSectionRE.FindStringSubmatch(line.text); match != nil {
				add(KindTOMLSection, match[1], line)
			} else if match := tomlKeyRE.FindStringSubmatch(line.text); match != nil {
				add(KindTOMLKey, match[1], line)
			}
		}
	}
	return symbols, true
}
