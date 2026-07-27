package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const plainIdentityDigestTag = "DEEIX-MCP-DEMO:plain-identity:v1"

type plainHeaderSpec struct {
	name   string
	limit  int
	assign func(*HeaderIdentity, string)
}

var plainHeaderSpecs = []plainHeaderSpec{
	{name: "X-MCP-CLIENT-USER-PUBLIC-ID", limit: 128, assign: func(identity *HeaderIdentity, value string) { identity.UserPublicID = value }},
	{name: "X-MCP-CLIENT-USER-DISPLAY-NAME", limit: 256, assign: func(identity *HeaderIdentity, value string) { identity.UserDisplayName = value }},
	{name: "X-MCP-CLIENT-USER-EMAIL", limit: 320, assign: func(identity *HeaderIdentity, value string) { identity.UserEmail = value }},
	{name: "X-MCP-CLIENT-USER-ROLE", limit: 64, assign: func(identity *HeaderIdentity, value string) { identity.UserRole = value }},
	{name: "X-MCP-CLIENT-CONVERSATION-PUBLIC-ID", limit: 128, assign: func(identity *HeaderIdentity, value string) { identity.ConversationPublicID = value }},
	{name: "X-MCP-CLIENT-ASSISTANT-MESSAGE-PUBLIC-ID", limit: 128, assign: func(identity *HeaderIdentity, value string) { identity.AssistantMessagePublicID = value }},
	{name: "X-MCP-CLIENT-USER-MESSAGE-PUBLIC-ID", limit: 128, assign: func(identity *HeaderIdentity, value string) { identity.UserMessagePublicID = value }},
	{name: "X-MCP-CLIENT-REQUEST-ID", limit: 128, assign: func(identity *HeaderIdentity, value string) { identity.RequestID = value }},
	{name: "X-MCP-CLIENT-RUN-ID", limit: 64, assign: func(identity *HeaderIdentity, value string) { identity.RunID = value }},
	{name: "X-MCP-CLIENT-TRACE-ID", limit: 64, assign: func(identity *HeaderIdentity, value string) { identity.TraceID = value }},
}

func PlainHeaderNames() []string {
	names := make([]string, len(plainHeaderSpecs))
	for index, spec := range plainHeaderSpecs {
		names[index] = spec.name
	}
	return names
}

func ParseHeaders(headers http.Header) (HeaderIdentity, error) {
	var identity HeaderIdentity
	for _, spec := range plainHeaderSpecs {
		values := matchingHeaderValues(headers, spec.name)
		if len(values) == 0 {
			continue
		}
		if len(values) > 1 {
			return HeaderIdentity{}, errors.New("plain identity header has multiple values")
		}
		value := values[0]
		if !utf8.ValidString(value) || containsUnicodeControl(value) {
			return HeaderIdentity{}, errors.New("plain identity header value is invalid")
		}
		if len(value) > spec.limit {
			return HeaderIdentity{}, errors.New("plain identity header value exceeds byte limit")
		}
		spec.assign(&identity, value)
	}
	return identity, nil
}

func matchingHeaderValues(headers http.Header, name string) []string {
	var values []string
	for candidate, candidateValues := range headers {
		if strings.EqualFold(candidate, name) {
			values = append(values, candidateValues...)
		}
	}
	return values
}

func containsUnicodeControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func (identity HeaderIdentity) CanonicalDigest() [32]byte {
	values := [...]string{
		plainIdentityDigestTag,
		identity.UserPublicID,
		identity.UserDisplayName,
		identity.UserEmail,
		identity.UserRole,
		identity.ConversationPublicID,
		identity.AssistantMessagePublicID,
		identity.UserMessagePublicID,
		identity.RequestID,
		identity.RunID,
		identity.TraceID,
	}

	encodedSize := len(values) * 4
	for _, value := range values {
		encodedSize += len(value)
	}
	encoded := make([]byte, 0, encodedSize)
	var length [4]byte
	for _, value := range values {
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		encoded = append(encoded, length[:]...)
		encoded = append(encoded, value...)
	}
	return sha256.Sum256(encoded)
}
