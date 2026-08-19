package portable

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// Normalize validates a JSON value against the supported MCP schema subset and
// performs conservative scalar coercions. Schema-valid input is returned byte
// for byte; a new RawMessage is emitted only when a coercion was required.
func Normalize(schema map[string]any, raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "arguments must be valid JSON: " + err.Error()}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "arguments must contain one JSON value"}
		}
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "arguments must be valid JSON: " + err.Error()}
	}
	normalized, changed, err := normalizeValue(schema, value, "$")
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "arguments could not be normalized: " + err.Error()}
	}
	return encoded, nil
}

func normalizeValue(schema map[string]any, value any, path string) (any, bool, error) {
	schemaType, _ := schema["type"].(string)
	if value == nil {
		return nil, false, schemaError(path, "must not be null")
	}

	var normalized any
	var changed bool
	switch schemaType {
	case "", "any":
		normalized = value
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false, schemaError(path, "must be an object")
		}
		properties, _ := schema["properties"].(map[string]any)
		requiredProperties := make(map[string]struct{})
		for _, required := range stringSlice(schema["required"]) {
			requiredProperties[required] = struct{}{}
			if _, exists := object[required]; !exists {
				return nil, false, schemaError(childPath(path, required), "is required")
			}
		}
		for key, child := range object {
			propertySchema, declared := schemaMap(properties[key])
			if !declared {
				switch additional := schema["additionalProperties"].(type) {
				case bool:
					if !additional {
						return nil, false, schemaError(childPath(path, key), "is not allowed")
					}
					continue
				case map[string]any:
					propertySchema = additional
				default:
					continue
				}
			}
			if child == nil && declared {
				if _, required := requiredProperties[key]; required {
					return nil, false, schemaError(childPath(path, key), "must not be null")
				}
				delete(object, key)
				changed = true
				continue
			}
			next, childChanged, err := normalizeValue(propertySchema, child, childPath(path, key))
			if err != nil {
				return nil, false, err
			}
			if childChanged {
				object[key] = next
				changed = true
			}
		}
		normalized = object
	case "array":
		array, ok := value.([]any)
		if !ok {
			return nil, false, schemaError(path, "must be an array")
		}
		itemSchema, hasItems := schemaMap(schema["items"])
		if hasItems {
			for index, child := range array {
				next, childChanged, err := normalizeValue(itemSchema, child, fmt.Sprintf("%s[%d]", path, index))
				if err != nil {
					return nil, false, err
				}
				if childChanged {
					array[index] = next
					changed = true
				}
			}
		}
		normalized = array
	case "string":
		switch typed := value.(type) {
		case string:
			normalized = typed
		case json.Number:
			normalized, changed = typed.String(), true
		case bool:
			normalized, changed = strconv.FormatBool(typed), true
		default:
			return nil, false, schemaError(path, "must be a string")
		}
	case "integer":
		switch typed := value.(type) {
		case json.Number:
			if _, err := typed.Int64(); err != nil {
				return nil, false, schemaError(path, "must be an integer")
			}
			normalized = typed
		case string:
			integer, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
			if err != nil {
				return nil, false, schemaError(path, "must be an integer")
			}
			normalized, changed = json.Number(strconv.FormatInt(integer, 10)), true
		default:
			return nil, false, schemaError(path, "must be an integer")
		}
	case "number":
		switch typed := value.(type) {
		case json.Number:
			if _, ok := new(big.Rat).SetString(typed.String()); !ok {
				return nil, false, schemaError(path, "must be a number")
			}
			normalized = typed
		case string:
			number := strings.TrimSpace(typed)
			if _, ok := new(big.Rat).SetString(number); !ok {
				return nil, false, schemaError(path, "must be a number")
			}
			normalized, changed = json.Number(number), true
		default:
			return nil, false, schemaError(path, "must be a number")
		}
	case "boolean":
		switch typed := value.(type) {
		case bool:
			normalized = typed
		case string:
			boolean, err := strconv.ParseBool(strings.TrimSpace(typed))
			if err != nil {
				return nil, false, schemaError(path, "must be a boolean")
			}
			normalized, changed = boolean, true
		default:
			return nil, false, schemaError(path, "must be a boolean")
		}
	default:
		return nil, false, schemaError(path, "uses unsupported schema type "+schemaType)
	}

	if err := validateEnum(schema, normalized, path); err != nil {
		return nil, false, err
	}
	if err := validateRange(schema, normalized, path); err != nil {
		return nil, false, err
	}
	return normalized, changed, nil
}

func validateEnum(schema map[string]any, value any, path string) error {
	choices, ok := schema["enum"].([]any)
	if !ok {
		return nil
	}
	actual, _ := json.Marshal(value)
	for _, choice := range choices {
		expected, _ := json.Marshal(choice)
		if bytes.Equal(actual, expected) {
			return nil
		}
	}
	return schemaError(path, "is not one of the allowed values")
}

func validateRange(schema map[string]any, value any, path string) error {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	actual, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return schemaError(path, "must be numeric")
	}
	for keyword, direction := range map[string]int{"minimum": -1, "maximum": 1} {
		raw, exists := schema[keyword]
		if !exists {
			continue
		}
		limit, ok := new(big.Rat).SetString(fmt.Sprint(raw))
		if !ok {
			continue
		}
		comparison := actual.Cmp(limit)
		if (direction < 0 && comparison < 0) || (direction > 0 && comparison > 0) {
			return schemaError(path, fmt.Sprintf("must satisfy %s %s", keyword, limit.RatString()))
		}
	}
	return nil
}

func schemaMap(value any) (map[string]any, bool) {
	schema, ok := value.(map[string]any)
	return schema, ok
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func childPath(parent, child string) string {
	if parent == "$" {
		return "$." + child
	}
	return parent + "." + child
}

func schemaError(path, message string) *DiagnosticError {
	return &DiagnosticError{Code: "E_SCHEMA", Message: path + " " + message}
}
