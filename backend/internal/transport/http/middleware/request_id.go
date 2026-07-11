package middleware

import (
	"regexp"
	"strings"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/pkg/traceid"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func normalizeRequestID(raw string) string {
	value := strings.TrimSpace(raw)
	if !requestIDPattern.MatchString(value) {
		return uuid.NewString()
	}
	return value
}

// RequestID 为每个请求注入可追踪 ID。
// 日志 trace_id 优先使用当前 OpenTelemetry span，未启用 OTel 时才回退到入口透传值或本地生成值。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := normalizeRequestID(c.GetHeader("X-Request-ID"))

		traceID := traceid.FromContext(c.Request.Context())
		if traceID == "" {
			if incomingTraceID := strings.TrimSpace(c.GetHeader("X-Trace-ID")); traceid.Valid(incomingTraceID) {
				traceID = strings.ToLower(incomingTraceID)
			}
		}
		if traceID == "" {
			traceID = traceid.Generate()
		}

		c.Set(ContextKeyRequestID, requestID)
		c.Set(ContextKeyTraceID, traceID)
		c.Writer.Header().Set("X-Request-ID", requestID)
		c.Writer.Header().Set("X-Trace-ID", traceID)

		// 将 trace_id 注入 request context，供下游 service 层使用。
		ctx := traceid.WithTraceID(c.Request.Context(), traceID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}
