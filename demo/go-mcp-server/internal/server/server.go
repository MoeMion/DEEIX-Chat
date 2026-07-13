package server

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/config"
	"github.com/DEEIX-AI/DEEIX-Chat/demo/go-mcp-server/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maximumRequestBodyBytes = 1 << 20
	maximumHeaderBytes      = 32 << 10
	defaultShutdownTimeout  = 5 * time.Second
)

type Server struct {
	httpServer      *http.Server
	handler         http.Handler
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger, now func() time.Time) (*Server, error) {
	if now == nil {
		return nil, errors.New("server clock is required")
	}
	if !isLoopbackListenAddress(cfg.Addr) {
		return nil, errors.New("server address must be an explicit loopback address")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	signedConfig := &identity.SignedConfig{Header: cfg.SignedContextHeader}
	if cfg.ContextJWT != nil {
		signedConfig.Secret = cfg.ContextJWT.Secret
		signedConfig.Issuer = cfg.ContextJWT.Issuer
		signedConfig.Audience = cfg.ContextJWT.Audience
		signedConfig.KeyID = cfg.ContextJWT.KeyID
	}
	resolver, err := identity.NewResolver(signedConfig, now)
	if err != nil {
		return nil, err
	}
	authenticator, err := identity.NewAuthenticator(cfg.BearerToken, resolver)
	if err != nil {
		return nil, err
	}

	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "deeix-identity-demo", Version: "1.0.0"},
		nil,
	)
	mcpServer.AddReceivingMiddleware(requireProtocolVersion)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "identity_check",
		Description: "Return the authenticated and verified DEEIX identity snapshot.",
	}, identityCheck(now))

	origin := http.NewCrossOriginProtection()
	origin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlainError(w, http.StatusForbidden, "http.origin_denied")
	}))
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{
			Stateless:                  false,
			JSONResponse:               true,
			SessionTimeout:             5 * time.Minute,
			DisableLocalhostProtection: false,
			CrossOriginProtection:      origin,
		},
	)

	sdkEndpoint := safeSDKErrorBoundary(
		scrubSensitiveHeaders(cfg.SignedContextHeader, streamable),
	)
	authenticated := auth.RequireBearerToken(
		auth.TokenVerifier(authenticator.Verify),
		nil,
	)(boundedBodyGuard(maximumRequestBodyBytes, sdkEndpoint))
	endpoint := origin.Handler(
		methodGuard(
			protocolHeaderGuard(
				bearerShapeGuard(authenticated),
			),
		),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.Handle("/mcp", endpoint)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maximumHeaderBytes,
	}
	return &Server{
		httpServer:      httpServer,
		handler:         mux,
		shutdownTimeout: defaultShutdownTimeout,
		logger:          logger,
	}, nil
}

func (s *Server) Handler() http.Handler {
	if s == nil {
		return nil
	}
	return s.handler
}

func methodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodPost, http.MethodGet, http.MethodDelete:
			next.ServeHTTP(w, request)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writePlainError(w, http.StatusMethodNotAllowed, "http.method_not_allowed")
		}
	})
}

func protocolHeaderGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		versions := matchingHeaderValues(request.Header, "Mcp-Protocol-Version")
		if len(versions) > 1 || len(versions) == 1 && versions[0] != ProtocolVersion {
			writePlainError(w, http.StatusBadRequest, "mcp.unsupported_protocol")
			return
		}
		if len(matchingHeaderValues(request.Header, "Mcp-Session-Id")) > 0 &&
			(len(versions) != 1 || versions[0] != ProtocolVersion) {
			writePlainError(w, http.StatusBadRequest, "mcp.unsupported_protocol")
			return
		}
		next.ServeHTTP(w, request)
	})
}

func bearerShapeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		values := matchingHeaderValues(request.Header, "Authorization")
		if len(values) != 1 {
			writePlainError(w, http.StatusUnauthorized, "auth.invalid_bearer")
			return
		}
		fields := strings.Fields(values[0])
		if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || fields[1] == "" {
			writePlainError(w, http.StatusUnauthorized, "auth.invalid_bearer")
			return
		}
		next.ServeHTTP(w, request)
	})
}

func boundedBodyGuard(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Body == nil {
			next.ServeHTTP(w, request)
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
		closeErr := request.Body.Close()
		if err != nil || closeErr != nil {
			writePlainError(w, http.StatusBadRequest, "mcp.bad_request")
			return
		}
		if int64(len(body)) > limit {
			writePlainError(w, http.StatusRequestEntityTooLarge, "http.request_too_large")
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		next.ServeHTTP(w, request)
	})
}

func scrubSensitiveHeaders(signedHeader string, next http.Handler) http.Handler {
	sensitive := append([]string{"Authorization"}, identity.PlainHeaderNames()...)
	if signedHeader != "" {
		sensitive = append(sensitive, signedHeader)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		cloned := request.Clone(request.Context())
		cloned.Header = request.Header.Clone()
		for candidate := range cloned.Header {
			for _, name := range sensitive {
				if strings.EqualFold(candidate, name) {
					delete(cloned.Header, candidate)
					break
				}
			}
		}
		next.ServeHTTP(w, cloned)
	})
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

func isLoopbackListenAddress(address string) bool {
	host, portText, err := net.SplitHostPort(address)
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
