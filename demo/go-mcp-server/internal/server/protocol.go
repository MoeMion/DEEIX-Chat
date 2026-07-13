package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ProtocolVersion         = "2025-11-25"
	maximumSDKResponseBytes = 1 << 20
)

func requireProtocolVersion(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if method == "initialize" {
			params, ok := request.GetParams().(*mcp.InitializeParams)
			if !ok || params.ProtocolVersion != ProtocolVersion {
				return nil, &jsonrpc.Error{
					Code:    jsonrpc.CodeInvalidParams,
					Message: "mcp.unsupported_protocol",
				}
			}
		}
		return next(ctx, method, request)
	}
}

type safeResponseWriter struct {
	mu sync.Mutex

	underlying  http.ResponseWriter
	header      http.Header
	status      int
	body        []byte
	oversized   bool
	committed   bool
	passthrough bool
}

func newSafeResponseWriter(underlying http.ResponseWriter) *safeResponseWriter {
	return &safeResponseWriter{
		underlying: underlying,
		header:     make(http.Header),
	}
}

func (w *safeResponseWriter) Header() http.Header {
	return w.header
}

func (w *safeResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed || w.passthrough || w.status != 0 {
		return
	}
	w.status = status
}

func (w *safeResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return len(data), nil
	}
	if w.passthrough {
		return w.underlying.Write(data)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.oversized || len(data) > maximumSDKResponseBytes-len(w.body) {
		w.oversized = true
		w.body = nil
		return len(data), nil
	}
	w.body = append(w.body, data...)
	return len(data), nil
}

func (w *safeResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return
	}
	if w.passthrough {
		if flusher, ok := w.underlying.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	status := w.effectiveStatus()
	mediaType, _, err := mime.ParseMediaType(w.header.Get("Content-Type"))
	flusher, canFlush := w.underlying.(http.Flusher)
	if w.oversized || err != nil || status < http.StatusOK || status >= http.StatusMultipleChoices ||
		mediaType != "text/event-stream" || !canFlush {
		w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
		return
	}
	w.copyPrivateHeadersLocked()
	w.underlying.WriteHeader(status)
	if len(w.body) != 0 {
		_, _ = w.underlying.Write(w.body)
	}
	w.body = nil
	w.passthrough = true
	flusher.Flush()
}

func (w *safeResponseWriter) Unwrap() http.ResponseWriter {
	return w.underlying
}

func (w *safeResponseWriter) finalize() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed || w.passthrough {
		return
	}
	status := w.effectiveStatus()
	if status >= http.StatusBadRequest {
		w.commitPlainLocked(status, safeHTTPErrorCode(status))
		return
	}
	if w.oversized {
		w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
		return
	}
	if status == http.StatusAccepted || status == http.StatusNoContent {
		if len(w.body) != 0 {
			w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
			return
		}
		w.commitRawLocked(status, nil, true)
		return
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices || len(w.body) == 0 {
		w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
		return
	}

	mediaType, _, err := mime.ParseMediaType(w.header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
		return
	}
	sanitized, kind, ok := sanitizeJSONRPCResponse(w.body)
	if !ok {
		w.commitPlainLocked(http.StatusInternalServerError, "mcp.internal_error")
		return
	}
	if kind == jsonResponseSuccess {
		w.commitRawLocked(status, w.body, true)
		return
	}
	clear(w.underlying.Header())
	w.underlying.Header().Set("Content-Type", "application/json")
	w.underlying.Header().Set("X-Content-Type-Options", "nosniff")
	w.underlying.WriteHeader(status)
	_, _ = w.underlying.Write(sanitized)
	w.committed = true
}

func (w *safeResponseWriter) effectiveStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *safeResponseWriter) copyPrivateHeadersLocked() {
	clear(w.underlying.Header())
	for name, values := range w.header {
		w.underlying.Header()[name] = append([]string(nil), values...)
	}
}

func (w *safeResponseWriter) commitRawLocked(status int, body []byte, copyHeaders bool) {
	if copyHeaders {
		w.copyPrivateHeadersLocked()
	} else {
		clear(w.underlying.Header())
	}
	w.underlying.WriteHeader(status)
	if len(body) != 0 {
		_, _ = w.underlying.Write(body)
	}
	w.committed = true
}

func (w *safeResponseWriter) commitPlainLocked(status int, code string) {
	clear(w.underlying.Header())
	w.underlying.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.underlying.Header().Set("X-Content-Type-Options", "nosniff")
	w.underlying.WriteHeader(status)
	_, _ = io.WriteString(w.underlying, code+"\n")
	w.body = nil
	w.committed = true
}

func safeSDKErrorBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		buffered := newSafeResponseWriter(w)
		next.ServeHTTP(buffered, request)
		buffered.finalize()
	})
}

func safeHTTPErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "mcp.bad_request"
	case http.StatusForbidden:
		return "mcp.forbidden"
	case http.StatusNotFound:
		return "mcp.session_not_found"
	case http.StatusMethodNotAllowed:
		return "http.method_not_allowed"
	case http.StatusConflict:
		return "mcp.stream_conflict"
	case http.StatusUnsupportedMediaType:
		return "mcp.unsupported_media_type"
	default:
		if status >= http.StatusInternalServerError {
			return "mcp.internal_error"
		}
		return "mcp.request_rejected"
	}
}

func writePlainError(w http.ResponseWriter, status int, code string) {
	clear(w.Header())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, code+"\n")
}

type jsonResponseKind uint8

const (
	jsonResponseSuccess jsonResponseKind = iota + 1
	jsonResponseError
	jsonResponseToolError
)

func sanitizeJSONRPCResponse(body []byte) ([]byte, jsonResponseKind, bool) {
	object, ok := decodeSingleJSONObject(body)
	if !ok || !validJSONRPCVersion(object["jsonrpc"]) || !validJSONRPCID(object["id"]) {
		return nil, 0, false
	}
	errorRaw, hasError := object["error"]
	resultRaw, hasResult := object["result"]
	if hasError == hasResult {
		return nil, 0, false
	}
	if hasError {
		sanitized, ok := sanitizeJSONRPCError(object["id"], errorRaw)
		return sanitized, jsonResponseError, ok
	}

	isToolError, valid := resultIsToolError(resultRaw)
	if !valid {
		return nil, 0, false
	}
	if !isToolError {
		return body, jsonResponseSuccess, true
	}
	sanitized := make([]byte, 0, 160)
	sanitized = append(sanitized, "{\"jsonrpc\":\"2.0\",\"id\":"...)
	sanitized = append(sanitized, bytes.TrimSpace(object["id"])...)
	sanitized = append(sanitized, ",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"mcp.tool_error\"}],\"isError\":true}}"...)
	return sanitized, jsonResponseToolError, true
}

func decodeSingleJSONObject(body []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return object, true
}

func validJSONRPCVersion(raw json.RawMessage) bool {
	var version string
	return len(raw) != 0 && json.Unmarshal(raw, &version) == nil && version == "2.0"
}

func validJSONRPCID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

func sanitizeJSONRPCError(id, raw json.RawMessage) ([]byte, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	code, ok := jsonInteger(object["code"])
	if !ok {
		return nil, false
	}
	var originalMessage string
	if err := json.Unmarshal(object["message"], &originalMessage); err != nil {
		return nil, false
	}
	message := safeJSONRPCMessage(code)
	if code == -32602 && originalMessage == "mcp.unsupported_protocol" {
		message = originalMessage
	}
	messageJSON, err := json.Marshal(message)
	if err != nil {
		return nil, false
	}
	sanitized := make([]byte, 0, 128)
	sanitized = append(sanitized, "{\"jsonrpc\":\"2.0\",\"id\":"...)
	sanitized = append(sanitized, bytes.TrimSpace(id)...)
	sanitized = append(sanitized, ",\"error\":{\"code\":"...)
	sanitized = strconv.AppendInt(sanitized, code, 10)
	sanitized = append(sanitized, ",\"message\":"...)
	sanitized = append(sanitized, messageJSON...)
	sanitized = append(sanitized, "}}"...)
	return sanitized, true
}

func jsonInteger(raw json.RawMessage) (int64, bool) {
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, ".eE") {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

func safeJSONRPCMessage(code int64) string {
	switch code {
	case -32700:
		return "mcp.parse_error"
	case -32600:
		return "mcp.invalid_request"
	case -32601:
		return "mcp.method_not_found"
	case -32602:
		return "mcp.invalid_params"
	case -32603:
		return "mcp.internal_error"
	default:
		return "mcp.request_failed"
	}
}

func resultIsToolError(raw json.RawMessage) (bool, bool) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, true
	}
	isErrorRaw, present := result["isError"]
	if !present {
		return false, true
	}
	var isError bool
	if err := json.Unmarshal(isErrorRaw, &isError); err != nil {
		return false, false
	}
	return isError, true
}
