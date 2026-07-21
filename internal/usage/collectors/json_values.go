package collectors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func decodeJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func normalizedKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lookupValue(values map[string]any, names ...string) (any, bool) {
	for _, name := range names {
		wanted := normalizedKey(name)
		for key, value := range values {
			if normalizedKey(key) == wanted {
				return value, true
			}
		}
	}
	return nil, false
}

func lookupNumber(values map[string]any, names ...string) (*float64, bool) {
	value, ok := lookupValue(values, names...)
	if !ok {
		return nil, false
	}
	result, ok := numericValue(value)
	return result, ok
}

func numericValue(value any) (*float64, bool) {
	var parsed float64
	switch typed := value.(type) {
	case json.Number:
		value, err := typed.Float64()
		if err != nil {
			return nil, false
		}
		parsed = value
	case float64:
		parsed = typed
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return nil, false
		}
		parsed = value
	default:
		return nil, false
	}
	return numberPtr(parsed), true
}

func lookupString(values map[string]any, names ...string) (string, bool) {
	value, ok := lookupValue(values, names...)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok && strings.TrimSpace(text) != ""
}

func findArrayByKey(value any, wanted string, depth int) ([]any, bool) {
	if depth > 8 {
		return nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		if found, ok := lookupValue(typed, wanted); ok {
			if array, ok := found.([]any); ok {
				return array, true
			}
		}
		for _, child := range typed {
			if result, ok := findArrayByKey(child, wanted, depth+1); ok {
				return result, true
			}
		}
	case []any:
		for _, child := range typed {
			if result, ok := findArrayByKey(child, wanted, depth+1); ok {
				return result, true
			}
		}
	}
	return nil, false
}

func timestampValue(value any) *time.Time {
	if text, ok := value.(string); ok {
		parsed, err := time.Parse(time.RFC3339, text)
		if err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	number, ok := numericValue(value)
	if !ok || *number <= 0 {
		return nil
	}
	seconds := int64(*number)
	if seconds > 100_000_000_000 {
		seconds /= 1000
	}
	parsed := time.Unix(seconds, 0).UTC()
	return &parsed
}

func lookupTimestamp(values map[string]any, names ...string) *time.Time {
	value, ok := lookupValue(values, names...)
	if !ok {
		return nil
	}
	return timestampValue(value)
}

func identifier(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := normalizedKey(part); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	if len(cleaned) == 0 {
		return "usage"
	}
	return strings.Join(cleaned, "_")
}

func mustObject(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected JSON object")
	}
	return object, nil
}
