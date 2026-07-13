package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	platformtracing "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/observability/tracing"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
)

const (
	defaultRequestTimeoutMS = 10000
	defaultConnectTimeout   = 10 * time.Second
)

type ClientErrorKind string

const (
	ClientErrorNetwork    ClientErrorKind = "network"
	ClientErrorHTTP       ClientErrorKind = "http"
	ClientErrorProtocol   ClientErrorKind = "protocol"
	ClientErrorJSONRPC    ClientErrorKind = "json_rpc"
	ClientErrorToolResult ClientErrorKind = "tool_result"
)

type ClientError struct {
	Kind       ClientErrorKind
	StatusCode int
	RPCCode    int
	cause      error
}

// Client 封装 MCP Streamable HTTP JSON-RPC 客户端。
type Client struct {
	httpClient            *http.Client
	contextSigner         ContextSigner
	transport             Transport
	transportOnce         sync.Once
	env                   string
	ssrfProtectionEnabled bool
}

// CallConfig 定义 MCP 调用配置。
type CallConfig struct {
	BaseURL             string
	AuthToken           string
	TimeoutMS           int
	HeadersEnabled      bool
	CustomHeaders       map[string]string
	Context             TemplateContext
	SignedContextHeader string
	SignedContext       *SignedContextConfig
}

// CallInput 定义 MCP 工具调用入参。
type CallInput struct {
	ToolName      string
	ArgumentsJSON string
}

// Tool 定义 MCP 工具元数据。
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// NewClient 创建 MCP 客户端。
func NewClient() *Client {
	return NewClientWithEnv("", false)
}

// NewClientWithEnv 创建带运行环境的 MCP 客户端。
func NewClientWithEnv(env string, ssrfProtectionEnabled bool) *Client {
	outbound := security.NewOutboundHTTPTransport(env, ssrfProtectionEnabled, defaultConnectTimeout)
	client := &Client{
		httpClient: &http.Client{
			Transport: platformtracing.NewHTTPTransport(outbound),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		contextSigner:         NewJWTContextSigner(),
		env:                   env,
		ssrfProtectionEnabled: ssrfProtectionEnabled,
	}
	client.transport = newClientHTTPTransport(client)
	return client
}

// ListTools 读取 MCP 服务暴露的工具列表。
func (c *Client) ListTools(ctx context.Context, cfg CallConfig) ([]Tool, error) {
	endpoint, err := c.buildEndpointURL(cfg)
	if err != nil {
		return nil, err
	}
	cfg.BaseURL = endpoint
	op, err := newOperation(c.transportBoundary(), cfg, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = op.terminate(context.WithoutCancel(ctx)) }()
	return op.ListTools(ctx)
}

// CallTool 执行远端 MCP 工具。
func (c *Client) CallTool(ctx context.Context, cfg CallConfig, input CallInput) (string, error) {
	endpoint, err := c.buildEndpointURL(cfg)
	if err != nil {
		return "", err
	}
	cfg.BaseURL = endpoint
	op, err := newOperation(c.transportBoundary(), cfg, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = op.terminate(context.WithoutCancel(ctx)) }()
	return op.CallTool(ctx, input)
}

func snapshotCallConfig(cfg CallConfig) (CallConfig, error) {
	if err := validateCustomHeadersWithSignedContext(
		cfg.CustomHeaders,
		cfg.SignedContextHeader,
		"",
		false,
	); err != nil {
		return CallConfig{}, err
	}
	snapshot := cfg
	snapshot.CustomHeaders = cloneCustomHeaders(cfg.CustomHeaders)
	if cfg.SignedContext != nil {
		signed := *cfg.SignedContext
		snapshot.SignedContext = &signed
	}
	return snapshot, nil
}

func cloneCustomHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(headers))
	for name, value := range headers {
		cloned[name] = value
	}
	return cloned
}

func buildCallToolParams(cfg CallConfig, input CallInput) (map[string]interface{}, error) {
	toolName := strings.TrimSpace(input.ToolName)
	if toolName == "" {
		return nil, fmt.Errorf("mcp tool name is empty")
	}
	arguments, err := decodeArguments(input.ArgumentsJSON)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"name":      toolName,
		"arguments": arguments,
		"_meta": map[string]interface{}{
			"deeix_user_public_id":              cfg.Context.UserPublicID,
			"deeix_conversation_public_id":      cfg.Context.ConversationPublicID,
			"deeix_assistant_message_public_id": cfg.Context.AssistantMessagePublicID,
			"deeix_user_message_public_id":      cfg.Context.UserMessagePublicID,
			"deeix_request_id":                  cfg.Context.RequestID,
			"deeix_run_id":                      cfg.Context.RunID,
			"deeix_trace_id":                    cfg.Context.TraceID,
		},
	}, nil
}

func (c *Client) transportBoundary() Transport {
	c.transportOnce.Do(func() {
		if c.transport == nil {
			c.transport = newClientHTTPTransport(c)
		}
	})
	return c.transport
}

func newClientError(kind ClientErrorKind, statusCode int, rpcCode int, cause error) *ClientError {
	return &ClientError{Kind: kind, StatusCode: statusCode, RPCCode: rpcCode, cause: cause}
}

func (e *ClientError) Error() string {
	if e == nil {
		return "mcp client error"
	}
	return fmt.Sprintf("mcp client error: kind=%s status=%d rpc_code=%d", e.Kind, e.StatusCode, e.RPCCode)
}

func (e *ClientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeContextCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if isTLSPolicyFailure(err) {
		return errTLSPolicyFailure
	}
	return nil
}

func isTLSPolicyFailure(err error) bool {
	var verificationErr *tls.CertificateVerificationError
	if errors.As(err, &verificationErr) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return true
	}
	var certificateErr x509.CertificateInvalidError
	if errors.As(err, &certificateErr) {
		return true
	}
	var rootsErr x509.SystemRootsError
	return errors.As(err, &rootsErr)
}

func SafeErrorSummary(err error) string {
	if errors.Is(err, context.Canceled) {
		return "mcp client error: kind=network canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "mcp client error: kind=network deadline_exceeded"
	}
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr.Error()
	}
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Error()
	}
	return "mcp client error: kind=internal"
}

func resolveRequestTimeoutMS(timeoutMS int) int {
	if timeoutMS <= 0 {
		return defaultRequestTimeoutMS
	}
	return timeoutMS
}

func (c *Client) buildEndpointURL(cfg CallConfig) (string, error) {
	endpoint, err := buildEndpointURL(cfg)
	if err != nil {
		return "", err
	}
	if err = security.ValidateOutboundHTTPURL(endpoint, c.env, c.ssrfProtectionEnabled); err != nil {
		return "", newClientError(ClientErrorProtocol, 0, 0, err)
	}
	return endpoint, nil
}

func buildEndpointURL(cfg CallConfig) (string, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return "", newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Scheme == "" ||
		parsed.Host == "" || parsed.Hostname() == "" {
		return "", newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if parsed.User != nil {
		return "", newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawFragment != "" || strings.Contains(baseURL, "#") {
		return "", newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	return baseURL, nil
}

type toolCallResult struct {
	Content []toolContent `json:"content,omitempty"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

func normalizeToolCallResult(raw json.RawMessage) (string, error) {
	output := strings.TrimSpace(string(raw))
	if output == "" {
		return "{}", nil
	}

	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return output, nil
	}
	if result.IsError {
		return "", newClientError(ClientErrorToolResult, 0, 0, nil)
	}
	if result.hasProtocolError() {
		return "", newClientError(ClientErrorToolResult, 0, 0, nil)
	}
	return output, nil
}

func (r toolCallResult) textContent() string {
	parts := make([]string, 0, len(r.Content))
	for _, item := range r.Content {
		if text := strings.TrimSpace(item.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func (r toolCallResult) hasProtocolError() bool {
	text := strings.TrimSpace(r.textContent())
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "MCP error ") || strings.HasPrefix(text, "MCP error:") {
		return true
	}
	return false
}

func decodeArguments(raw string) (map[string]interface{}, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return map[string]interface{}{}, nil
	}
	var parsed interface{}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("mcp tool arguments must be a valid JSON object")
	}
	object, ok := normalizeJSONNumber(parsed).(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("mcp tool arguments must be a JSON object")
	}
	return object, nil
}

func normalizeJSONNumber(value interface{}) interface{} {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		if parsed, err := strconv.ParseFloat(typed.String(), 64); err == nil {
			return parsed
		}
		return typed.String()
	case map[string]interface{}:
		for key, item := range typed {
			typed[key] = normalizeJSONNumber(item)
		}
		return typed
	case []interface{}:
		for index, item := range typed {
			typed[index] = normalizeJSONNumber(item)
		}
		return typed
	default:
		return value
	}
}
