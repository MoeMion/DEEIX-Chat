package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

func TestNormalizeRequestID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       string
		wantExact   string
		wantNewUUID bool
	}{
		{name: "trim valid", input: "  req_1:a-b.c  ", wantExact: "req_1:a-b.c"},
		{name: "blank", input: "   ", wantNewUUID: true},
		{name: "oversized", input: strings.Repeat("a", 129), wantNewUUID: true},
		{name: "unicode", input: "请求", wantNewUUID: true},
		{name: "control", input: "req\nother", wantNewUUID: true},
		{name: "punctuation", input: "req/value", wantNewUUID: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRequestID(tt.input)
			if tt.wantNewUUID {
				if _, err := uuid.Parse(got); err != nil || got == strings.TrimSpace(tt.input) {
					t.Fatalf("generated request ID = %q, parse error = %v", got, err)
				}
				return
			}
			if got != tt.wantExact {
				t.Fatalf("request ID = %q, want %q", got, tt.wantExact)
			}
		})
	}
}

func TestRequestIDPrefersOpenTelemetryTraceID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	otelTraceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{TraceID: otelTraceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})

	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-ID", "11111111111111111111111111111111")
	req = req.WithContext(trace.ContextWithSpanContext(req.Context(), spanCtx))
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("X-Trace-ID"); got != otelTraceID.String() {
		t.Fatalf("expected OTel trace id, got %q", got)
	}
}

func TestRequestIDRejectsInvalidIncomingTraceID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.Use(RequestID())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-ID", "not-a-trace-id")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, req)

	if got := recorder.Header().Get("X-Trace-ID"); got == "" || got == "not-a-trace-id" {
		t.Fatalf("expected generated trace id, got %q", got)
	}
}
