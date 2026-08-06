package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RedactedHeaderValue 是控制面响应中敏感请求头值的固定占位符。
const RedactedHeaderValue = "********"

// ErrInvalidRedactedHeaders 表示脱敏请求头无法安全合并。
var ErrInvalidRedactedHeaders = errors.New("invalid redacted headers")

// ParseHeaderStringMapJSON 严格解析仅含字符串值的单个 JSON 对象。
func ParseHeaderStringMapJSON(raw string) (map[string]string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return map[string]string{}, nil
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	opening, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("decode header object: %w", err)
	}
	openingDelimiter, ok := opening.(json.Delim)
	if !ok || openingDelimiter != '{' {
		return nil, errors.New("header json must be an object")
	}

	result := make(map[string]string)
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return nil, fmt.Errorf("decode header name: %w", keyErr)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("header name must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("duplicate header name")
		}
		seen[key] = struct{}{}

		valueToken, valueErr := decoder.Token()
		if valueErr != nil {
			return nil, fmt.Errorf("decode header value: %w", valueErr)
		}
		headerValue, ok := valueToken.(string)
		if !ok {
			return nil, errors.New("header value must be a string")
		}
		result[key] = headerValue
	}

	closing, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("close header object: %w", err)
	}
	closingDelimiter, ok := closing.(json.Delim)
	if !ok || closingDelimiter != '}' {
		return nil, errors.New("header json must end with an object")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing header json: %w", err)
		}
		return nil, errors.New("header json contains a trailing value")
	}
	return result, nil
}

// MergeRedactedHeadersJSON 将请求中的脱敏占位符还原为已有敏感值，并以请求中的键集合为准。
func MergeRedactedHeadersJSON(existingRaw string, incomingRaw string) (string, error) {
	existing, err := ParseHeaderStringMapJSON(existingRaw)
	if err != nil {
		return "", fmt.Errorf("%w: existing headers", ErrInvalidRedactedHeaders)
	}
	incoming, err := ParseHeaderStringMapJSON(incomingRaw)
	if err != nil {
		return "", fmt.Errorf("%w: incoming headers", ErrInvalidRedactedHeaders)
	}
	existingByName := make(map[string]string, len(existing))
	for key, value := range existing {
		existingByName[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(key)))] = value
	}
	for key, value := range incoming {
		if value != RedactedHeaderValue {
			continue
		}
		if !IsSensitiveHeaderName(key) {
			return "", fmt.Errorf("%w: sentinel on non-sensitive header", ErrInvalidRedactedHeaders)
		}
		previous, ok := existingByName[strings.ToLower(http.CanonicalHeaderKey(strings.TrimSpace(key)))]
		if !ok {
			return "", fmt.Errorf("%w: sentinel has no previous value", ErrInvalidRedactedHeaders)
		}
		incoming[key] = previous
	}
	normalized, err := json.Marshal(incoming)
	if err != nil {
		return "", fmt.Errorf("%w: encode headers", ErrInvalidRedactedHeaders)
	}
	return string(normalized), nil
}

// ContainsRedactedHeaderValue 判断 JSON 对象中是否包含脱敏占位符。
func ContainsRedactedHeaderValue(raw string) bool {
	values, err := ParseHeaderStringMapJSON(raw)
	if err != nil {
		return false
	}
	for _, value := range values {
		if value == RedactedHeaderValue {
			return true
		}
	}
	return false
}

// RedactHeadersJSON 对自定义请求头 JSON 中的敏感头做脱敏，避免 API 响应扩大密钥泄漏面。
func RedactHeadersJSON(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return value
	}
	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil || payload == nil {
		return "{}"
	}
	result := make(map[string]interface{}, len(payload))
	for key, item := range payload {
		if IsSensitiveHeaderName(key) {
			result[key] = RedactedHeaderValue
			continue
		}
		result[key] = item
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		return "{}"
	}
	return string(normalized)
}

// IsSensitiveHeaderName 判断 header 名称是否可能承载密钥、Token 或 Cookie。
func IsSensitiveHeaderName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if normalized == "" {
		return false
	}
	if normalized == "cookie" || normalized == "set-cookie" {
		return true
	}
	for _, marker := range []string{"authorization", "api-key", "apikey", "token", "secret"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
