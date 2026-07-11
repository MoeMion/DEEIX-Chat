package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	platformtracing "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/observability/tracing"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
)

const (
	protocolVersion         = "2025-06-18"
	defaultRequestTimeoutMS = 10000
	defaultConnectTimeout   = 10 * time.Second
)

var ErrUnsupportedProtocolVersion = errors.New("unsupported mcp protocol version")

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

type session struct {
	ID              string
	ProtocolVersion string
}

// Client 封装 MCP Streamable HTTP JSON-RPC 客户端。
type Client struct {
	httpClient            *http.Client
	nextID                atomic.Int64
	env                   string
	ssrfProtectionEnabled bool
}

// CallConfig 定义 MCP 调用配置。
type CallConfig struct {
	BaseURL       string
	AuthToken     string
	TimeoutMS     int
	CustomHeaders map[string]string
	Context       TemplateContext
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
	transport := security.NewOutboundHTTPTransport(env, ssrfProtectionEnabled, defaultConnectTimeout)
	return &Client{
		httpClient: &http.Client{
			Transport: platformtracing.NewHTTPTransport(transport),
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		env:                   env,
		ssrfProtectionEnabled: ssrfProtectionEnabled,
	}
}

// ListTools 读取 MCP 服务暴露的工具列表。
func (c *Client) ListTools(ctx context.Context, cfg CallConfig) ([]Tool, error) {
	current, err := c.initialize(ctx, cfg)
	if current.ID != "" {
		defer func() { _ = c.terminateSession(ctx, cfg, current) }()
	}
	if err != nil {
		return nil, err
	}
	result, next, err := c.rpcWithSession(ctx, cfg, current, "tools/list", map[string]interface{}{}, false)
	current = next
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []Tool `json:"tools"`
	}
	if err = json.Unmarshal(result, &payload); err != nil {
		return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	return payload.Tools, nil
}

// CallTool 执行远端 MCP 工具。
func (c *Client) CallTool(ctx context.Context, cfg CallConfig, input CallInput) (string, error) {
	params, err := buildCallToolParams(cfg, input)
	if err != nil {
		return "", err
	}
	current, err := c.initialize(ctx, cfg)
	if current.ID != "" {
		defer func() { _ = c.terminateSession(ctx, cfg, current) }()
	}
	if err != nil {
		return "", err
	}
	result, next, err := c.rpcWithSession(ctx, cfg, current, "tools/call", params, false)
	current = next
	if err != nil {
		return "", err
	}
	return normalizeToolCallResult(result)
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

func (c *Client) initialize(ctx context.Context, cfg CallConfig) (session, error) {
	params := map[string]interface{}{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "deeix-chat",
			"version": "0.1.0",
		},
	}
	current := session{ProtocolVersion: protocolVersion}
	result, next, err := c.rpcWithSession(ctx, cfg, current, "initialize", params, false)
	current = next
	if err != nil {
		return current, err
	}
	var initialized struct {
		ProtocolVersion json.RawMessage `json:"protocolVersion"`
	}
	if err = json.Unmarshal(result, &initialized); err != nil {
		return current, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if len(initialized.ProtocolVersion) != 0 {
		var negotiated string
		if err = json.Unmarshal(initialized.ProtocolVersion, &negotiated); err != nil {
			return current, newClientError(ClientErrorProtocol, 0, 0, nil)
		}
		if negotiated != protocolVersion {
			return current, newClientError(ClientErrorProtocol, 0, 0, ErrUnsupportedProtocolVersion)
		}
	}
	_, next, err = c.rpcWithSession(ctx, cfg, current, "notifications/initialized", nil, true)
	current = next
	if err != nil {
		return current, err
	}
	return current, nil
}

func (c *Client) rpcWithSession(
	ctx context.Context,
	cfg CallConfig,
	current session,
	method string,
	params interface{},
	notification bool,
) (json.RawMessage, session, error) {
	endpoint, err := c.buildEndpointURL(cfg)
	if err != nil {
		return nil, current, err
	}
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	if !notification {
		payload["id"] = c.nextRequestID()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, current, newClientError(ClientErrorProtocol, 0, 0, nil)
	}

	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(resolveRequestTimeoutMS(cfg.TimeoutMS))*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, current, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if err = applyRequestHeaders(req, cfg, current); err != nil {
		return nil, current, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, current, newClientError(ClientErrorNetwork, 0, 0, safeContextCause(requestCtx, err))
	}
	defer resp.Body.Close() //nolint:errcheck

	if nextSessionID := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id")); nextSessionID != "" {
		current.ID = nextSessionID
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, current, newClientError(ClientErrorHTTP, resp.StatusCode, 0, nil)
	}
	if notification {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, current, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, current, newClientError(ClientErrorNetwork, 0, 0, safeContextCause(requestCtx, err))
	}
	result, err := parseRPCResponse(resp.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, current, err
	}
	return result, current, nil
}

func applyRequestHeaders(req *http.Request, cfg CallConfig, current session) error {
	if err := ValidateRenderedCustomHeaders(cfg.CustomHeaders); err != nil {
		return err
	}
	for name, value := range cfg.CustomHeaders {
		req.Header.Set(name, value)
	}
	if token := strings.TrimSpace(cfg.AuthToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if current.ID != "" {
		req.Header.Set("MCP-Session-Id", current.ID)
		req.Header.Set("MCP-Protocol-Version", current.ProtocolVersion)
	}
	return nil
}

func (c *Client) terminateSession(ctx context.Context, cfg CallConfig, current session) error {
	if current.ID == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	endpoint, err := c.buildEndpointURL(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return newClientError(ClientErrorProtocol, 0, 0, err)
	}
	if err = applyRequestHeaders(req, cfg, current); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return newClientError(ClientErrorNetwork, 0, 0, safeContextCause(cleanupCtx, err))
	}
	defer resp.Body.Close() //nolint:errcheck
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
		resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	return newClientError(ClientErrorHTTP, resp.StatusCode, 0, nil)
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
	return nil
}

func SafeErrorSummary(err error) string {
	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		return clientErr.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "mcp client error: kind=network canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "mcp client error: kind=network deadline_exceeded"
	}
	return "mcp client error: kind=internal"
}

func resolveRequestTimeoutMS(timeoutMS int) int {
	if timeoutMS <= 0 {
		return defaultRequestTimeoutMS
	}
	return timeoutMS
}

func (c *Client) nextRequestID() int64 {
	return c.nextID.Add(1)
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
	if err != nil || parsed == nil || parsed.Opaque != "" || parsed.Scheme == "" || parsed.Host == "" {
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

func parseRPCResponse(contentType string, body []byte) (json.RawMessage, error) {
	payload := strings.TrimSpace(string(body))
	if payload == "" {
		return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.EqualFold(mediaType, "text/event-stream") {
		payload = extractSSEDataPayload(payload)
		if payload == "" {
			return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
		}
	}
	response := rpcResponse{}
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return nil, newClientError(ClientErrorProtocol, 0, 0, nil)
	}
	if response.Error != nil {
		return nil, newClientError(ClientErrorJSONRPC, 0, response.Error.Code, nil)
	}
	if len(response.Result) == 0 {
		return json.RawMessage("{}"), nil
	}
	return response.Result, nil
}

func extractSSEDataPayload(payload string) string {
	reader := bufio.NewReader(strings.NewReader(payload))
	dataLines := make([]string, 0, 4)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ")
		dataLines = append(dataLines, data)
	}
	return strings.TrimSpace(strings.Join(dataLines, "\n"))
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code int `json:"code"`
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
