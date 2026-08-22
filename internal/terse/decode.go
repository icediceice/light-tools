// Package terse implements a deterministic, self-verifying representation for
// JSON tool results. Decode is intentionally production code: Format uses it to
// prove semantic equality before any transformed bytes leave the server.
package terse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

type parser struct {
	data  []byte
	index int
}

// Decode parses a value emitted by Format into the same Go shapes produced by
// encoding/json with UseNumber enabled.
func Decode(data []byte) (any, error) {
	p := parser{data: data}
	if !p.take('~') {
		return nil, fmt.Errorf("terse document must start with ~")
	}
	value, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if p.index != len(p.data) {
		return nil, fmt.Errorf("unexpected trailing data at byte %d", p.index)
	}
	return value, nil
}

func (p *parser) parseValue() (any, error) {
	if p.index >= len(p.data) {
		return nil, io.ErrUnexpectedEOF
	}
	switch p.data[p.index] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseQuotedString()
	default:
		return p.parseAtom()
	}
}

func (p *parser) parseObject() (any, error) {
	p.index++
	if p.take('}') {
		return nil, fmt.Errorf("empty objects are not valid terse values")
	}
	result := make(map[string]any)
	for {
		key, err := p.parseKey(':')
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate object key %q", key)
		}
		if !p.take(':') {
			return nil, fmt.Errorf("expected : after object key %q", key)
		}
		value, err := p.parseValue()
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		result[key] = value
		if p.take('}') {
			return result, nil
		}
		if !p.take(';') {
			return nil, fmt.Errorf("expected ; or } at byte %d", p.index)
		}
	}
}

func (p *parser) parseArray() (any, error) {
	p.index++
	if p.take(']') {
		return nil, fmt.Errorf("empty arrays are not valid terse values")
	}
	if p.hasTopLevelPipe() {
		return p.parseTable()
	}
	var result []any
	for {
		value, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		if p.take(']') {
			return result, nil
		}
		if !p.take(',') {
			return nil, fmt.Errorf("expected , or ] at byte %d", p.index)
		}
	}
}

func (p *parser) parseTable() (any, error) {
	var keys []string
	seen := make(map[string]struct{})
	for {
		key, err := p.parseKey(',', '|')
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate table key %q", key)
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
		if p.take('|') {
			break
		}
		if !p.take(',') {
			return nil, fmt.Errorf("expected , or | at byte %d", p.index)
		}
	}

	var rows []any
	for {
		row := make(map[string]any, len(keys))
		for column, key := range keys {
			value, err := p.parseValue()
			if err != nil {
				return nil, fmt.Errorf("table row %d column %q: %w", len(rows), key, err)
			}
			row[key] = value
			if column+1 < len(keys) {
				if !p.take(',') {
					return nil, fmt.Errorf("table row %d has too few columns", len(rows))
				}
			}
		}
		rows = append(rows, row)
		if p.take(']') {
			return rows, nil
		}
		if !p.take('|') {
			return nil, fmt.Errorf("expected | or ] after table row %d", len(rows)-1)
		}
	}
}

func (p *parser) parseKey(terminators ...byte) (string, error) {
	start := p.index
	for p.index < len(p.data) && !containsByte(terminators, p.data[p.index]) {
		p.index++
	}
	if p.index == len(p.data) {
		return "", io.ErrUnexpectedEOF
	}
	key := string(p.data[start:p.index])
	if !safeKey(key) {
		return "", fmt.Errorf("unsafe terse key %q", key)
	}
	return key, nil
}

func (p *parser) parseQuotedString() (any, error) {
	start := p.index
	p.index++
	escaped := false
	for p.index < len(p.data) {
		current := p.data[p.index]
		p.index++
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if current == '"' {
			var value string
			if err := json.Unmarshal(p.data[start:p.index], &value); err != nil {
				return nil, err
			}
			return value, nil
		}
	}
	return nil, io.ErrUnexpectedEOF
}

func (p *parser) parseAtom() (any, error) {
	start := p.index
	for p.index < len(p.data) && !isValueDelimiter(p.data[p.index]) {
		r, size := utf8.DecodeRune(p.data[p.index:])
		if r == utf8.RuneError && size == 1 {
			return nil, fmt.Errorf("invalid UTF-8 at byte %d", p.index)
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return nil, fmt.Errorf("unquoted whitespace at byte %d", p.index)
		}
		p.index += size
	}
	if start == p.index {
		return nil, fmt.Errorf("empty value at byte %d", p.index)
	}
	atom := string(p.data[start:p.index])
	switch atom {
	case "null":
		return nil, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if number, ok := parseNumber(atom); ok {
		return number, nil
	}
	return atom, nil
}

func (p *parser) hasTopLevelPipe() bool {
	depth := 0
	inString := false
	escaped := false
	for index := p.index; index < len(p.data); index++ {
		current := p.data[index]
		if inString {
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ']':
			if depth == 0 {
				return false
			}
			depth--
		case '|':
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

func (p *parser) take(want byte) bool {
	if p.index >= len(p.data) || p.data[p.index] != want {
		return false
	}
	p.index++
	return true
}

func parseNumber(value string) (json.Number, bool) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", false
	}
	number, ok := decoded.(json.Number)
	return number, ok
}

func containsByte(values []byte, candidate byte) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func isValueDelimiter(value byte) bool {
	return bytes.IndexByte([]byte(",;:{}[]|"), value) >= 0
}
