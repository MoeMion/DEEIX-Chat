package config

import (
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/identity"
)

const (
	defaultAddr                = "127.0.0.1:8090"
	defaultSignedContextHeader = "X-MCP-CLIENT-SIGNED-CONTEXT"
	minimumBearerTokenBytes    = 32
	contextSecretBytes         = 32
	maximumHeaderNameBytes     = 128
)

var (
	reservedHeaderNames = []string{
		"Accept",
		"Accept-Encoding",
		"Authorization",
		"Baggage",
		"Connection",
		"Content-Length",
		"Content-Type",
		"Cookie",
		"Host",
		"Keep-Alive",
		"Last-Event-ID",
		"MCP-Protocol-Version",
		"MCP-Session-Id",
		"Origin",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Set-Cookie",
		"TE",
		"Traceparent",
		"Tracestate",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"User-Agent",
		"X-DEEIX-Context",
	}
	reservedHeaderPrefixes = []string{"MCP-", "Proxy-", "Sec-", "X-DEEIX-"}
)

type LookupEnv func(string) (string, bool)

type ContextJWT struct {
	Secret   string
	Issuer   string
	Audience string
	KeyID    string
}

type Config struct {
	Addr                string
	BearerToken         string
	SignedContextHeader string
	ContextJWT          *ContextJWT
}

func Load(lookup LookupEnv) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("environment lookup is required")
	}

	addr := defaultAddr
	if value, present := lookup("MCP_DEMO_ADDR"); present {
		addr = value
	}
	if !isLoopbackAddress(addr) {
		return Config{}, errors.New("address must be localhost or an explicit loopback ip with a valid port")
	}

	bearerToken, present := lookup("MCP_DEMO_BEARER_TOKEN")
	if !present || len(bearerToken) < minimumBearerTokenBytes {
		return Config{}, errors.New("bearer token must contain at least 32 bytes")
	}

	signedContextHeader := defaultSignedContextHeader
	if value, configured := lookup("MCP_DEMO_SIGNED_CONTEXT_HEADER"); configured {
		signedContextHeader = value
	}
	if !validSignedContextHeader(signedContextHeader) {
		return Config{}, errors.New("signed context header name is invalid or reserved")
	}

	contextJWT, err := loadContextJWT(lookup)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:                addr,
		BearerToken:         bearerToken,
		SignedContextHeader: signedContextHeader,
		ContextJWT:          contextJWT,
	}, nil
}

func loadContextJWT(lookup LookupEnv) (*ContextJWT, error) {
	secret, _ := lookup("MCP_DEMO_CONTEXT_SECRET")
	issuer, _ := lookup("MCP_DEMO_CONTEXT_ISSUER")
	audience, _ := lookup("MCP_DEMO_CONTEXT_AUDIENCE")
	keyID, _ := lookup("MCP_DEMO_CONTEXT_KEY_ID")

	configured := 0
	for _, value := range []string{secret, issuer, audience, keyID} {
		if value != "" {
			configured++
		}
	}
	if configured == 0 {
		return nil, nil
	}
	if configured != 4 {
		return nil, errors.New("context jwt configuration must be entirely set or empty")
	}

	decoded, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	if err != nil || len(decoded) != contextSecretBytes ||
		base64.RawURLEncoding.EncodeToString(decoded) != secret {
		return nil, errors.New("context jwt secret must be canonical raw base64url for exactly 32 bytes")
	}

	return &ContextJWT{
		Secret:   secret,
		Issuer:   issuer,
		Audience: audience,
		KeyID:    keyID,
	}, nil
}

func isLoopbackAddress(addr string) bool {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil || !decimalDigits(portText) {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func validSignedContextHeader(name string) bool {
	if name == "" || len(name) > maximumHeaderNameBytes || name != strings.TrimSpace(name) {
		return false
	}
	for index := range len(name) {
		if !isHeaderNameByte(name[index]) {
			return false
		}
	}
	for _, reserved := range reservedHeaderNames {
		if strings.EqualFold(name, reserved) {
			return false
		}
	}
	lowerName := strings.ToLower(name)
	for _, prefix := range reservedHeaderPrefixes {
		if strings.HasPrefix(lowerName, strings.ToLower(prefix)) {
			return false
		}
	}
	for _, plainName := range identity.PlainHeaderNames() {
		if strings.EqualFold(name, plainName) {
			return false
		}
	}
	return true
}

func isHeaderNameByte(value byte) bool {
	if value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", rune(value))
}
