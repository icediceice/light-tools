package terse

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"unicode"
	"unicode/utf8"
)

const minimumInputTokens = 100

// Format returns a shorter deterministic representation only after decoding it
// back and proving exact equality with the original UseNumber JSON value.
func Format(raw []byte) ([]byte, bool) {
	if EstimateTokens(raw) < minimumInputTokens {
		return nil, false
	}
	want, ok := decodeJSONDocument(raw)
	if !ok {
		return nil, false
	}

	var rendered bytes.Buffer
	rendered.WriteByte('~')
	if !renderValue(&rendered, want) {
		return nil, false
	}
	candidate := rendered.Bytes()

	got, err := Decode(candidate)
	if err != nil || !reflect.DeepEqual(want, got) {
		return nil, false
	}
	if len(candidate) >= len(raw) || EstimateTokens(candidate) >= EstimateTokens(raw) {
		return nil, false
	}
	return append([]byte(nil), candidate...), true
}

// EstimateTokens is a deterministic punctuation-aware approximation used only
// for the terse swap decision. It is intentionally separate from light_file's
// public read-window token estimate.
func EstimateTokens(value []byte) int {
	tokens := 0
	wordRunes := 0
	flushWord := func() {
		if wordRunes > 0 {
			tokens += (wordRunes + 3) / 4
			wordRunes = 0
		}
	}
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			flushWord()
			tokens++
			value = value[1:]
			continue
		}
		value = value[size:]
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' {
			wordRunes++
			continue
		}
		flushWord()
		if !unicode.IsSpace(r) {
			tokens++
		}
	}
	flushWord()
	return tokens
}

func decodeJSONDocument(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

func renderValue(output *bytes.Buffer, value any) bool {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
		return true
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
		return true
	case json.Number:
		output.WriteString(typed.String())
		return true
	case string:
		return renderString(output, typed)
	case map[string]any:
		return renderObject(output, typed)
	case []any:
		return renderArray(output, typed)
	default:
		return false
	}
}

func renderString(output *bytes.Buffer, value string) bool {
	if safeAtom(value) {
		output.WriteString(value)
		return true
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	output.Write(encoded)
	return true
}

func renderObject(output *bytes.Buffer, value map[string]any) bool {
	if len(value) == 0 {
		return false
	}
	keys, ok := sortedKeys(value)
	if !ok {
		return false
	}
	var body bytes.Buffer
	for index, key := range keys {
		if index > 0 {
			body.WriteByte(';')
		}
		body.WriteString(key)
		body.WriteByte(':')
		if !renderValue(&body, value[key]) {
			return false
		}
	}
	output.WriteByte('{')
	output.Write(body.Bytes())
	output.WriteByte('}')
	return true
}

func renderArray(output *bytes.Buffer, value []any) bool {
	if len(value) == 0 {
		return false
	}
	if _, ok := value[0].(map[string]any); ok {
		return renderTable(output, value)
	}

	var body bytes.Buffer
	for index, item := range value {
		switch item.(type) {
		case nil, bool, json.Number, string:
		default:
			return false
		}
		if index > 0 {
			body.WriteByte(',')
		}
		if !renderValue(&body, item) {
			return false
		}
	}
	output.WriteByte('[')
	output.Write(body.Bytes())
	output.WriteByte(']')
	return true
}

func renderTable(output *bytes.Buffer, value []any) bool {
	first, ok := value[0].(map[string]any)
	if !ok || len(first) == 0 {
		return false
	}
	keys, ok := sortedKeys(first)
	if !ok {
		return false
	}

	var body bytes.Buffer
	for index, key := range keys {
		if index > 0 {
			body.WriteByte(',')
		}
		body.WriteString(key)
	}
	for _, item := range value {
		row, ok := item.(map[string]any)
		if !ok || len(row) != len(keys) {
			return false
		}
		rowKeys, ok := sortedKeys(row)
		if !ok || !reflect.DeepEqual(rowKeys, keys) {
			return false
		}
		body.WriteByte('|')
		for column, key := range keys {
			if column > 0 {
				body.WriteByte(',')
			}
			if !renderValue(&body, row[key]) {
				return false
			}
		}
	}
	output.WriteByte('[')
	output.Write(body.Bytes())
	output.WriteByte(']')
	return true
}

func sortedKeys(value map[string]any) ([]string, bool) {
	keys := make([]string, 0, len(value))
	for key := range value {
		if !safeKey(key) {
			return nil, false
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, true
}

func safeKey(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		current := value[index]
		if index == 0 {
			if (current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') || current == '_' {
				continue
			}
			return false
		}
		if (current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') || current == '_' || current == '-' || current == '.' {
			continue
		}
		return false
	}
	return true
}

func safeAtom(value string) bool {
	if value == "" || value == "null" || value == "true" || value == "false" {
		return false
	}
	if _, number := parseNumber(value); number {
		return false
	}
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
		if r < utf8.RuneSelf && isValueDelimiter(byte(r)) {
			return false
		}
		if r == '"' || r == '\\' {
			return false
		}
		value = value[size:]
	}
	return true
}
