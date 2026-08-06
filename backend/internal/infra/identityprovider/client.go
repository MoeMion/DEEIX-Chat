package identityprovider

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	platformtracing "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/observability/tracing"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/outboundhttp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
)

const (
	requestTimeout                     = 10 * time.Second
	maxResponseSize                    = 1 << 20
	tlsInsecureSkipVerifyClientVariant = "tls-insecure-skip-verify"
)

// RequestOptions 控制单个管理员身份源请求的隔离传输层选项。
type RequestOptions struct {
	TLSInsecureSkipVerify bool
}

// Response 是身份源 HTTP 调用经过边界化处理后的响应。
type Response struct {
	StatusCode int
	Status     string
	Body       []byte
}

// Successful 判断身份源是否返回 2xx 状态码。
func (r Response) Successful() bool {
	return r.StatusCode >= http.StatusOK && r.StatusCode < http.StatusMultipleChoices
}

// Client 统一承载身份源与人机验证服务的 HTTP、SSRF、重定向和响应大小边界。
// basePolicy 必须是不含全局私网白名单的严格策略；管理员配置的端点仅按精确 origin 局部授权。
type Client struct {
	httpClients *outboundhttp.Pool
}

// New 创建身份源基础设施适配器。
func New(basePolicy security.OutboundPolicy) *Client {
	return &Client{
		httpClients: outboundhttp.NewPool(basePolicy, outboundhttp.DefaultCacheLimit, newIdentityProviderHTTPClient),
	}
}

// Get 向身份源发送 GET 请求。
func (c *Client) Get(ctx context.Context, targetURL string, trustedEndpoints []string, headers map[string]string) (Response, error) {
	return c.GetWithOptions(ctx, targetURL, trustedEndpoints, headers, RequestOptions{})
}

// GetWithOptions 向身份源发送带隔离传输层选项的 GET 请求。
func (c *Client) GetWithOptions(
	ctx context.Context,
	targetURL string,
	trustedEndpoints []string,
	headers map[string]string,
	options RequestOptions,
) (Response, error) {
	return c.do(ctx, http.MethodGet, targetURL, trustedEndpoints, headers, nil, options)
}

// PostForm 向身份源发送 application/x-www-form-urlencoded 请求。
func (c *Client) PostForm(ctx context.Context, targetURL string, trustedEndpoints []string, form url.Values, headers map[string]string) (Response, error) {
	return c.PostFormWithOptions(ctx, targetURL, trustedEndpoints, form, headers, RequestOptions{})
}

// PostFormWithOptions 向身份源发送带隔离传输层选项的表单请求。
func (c *Client) PostFormWithOptions(
	ctx context.Context,
	targetURL string,
	trustedEndpoints []string,
	form url.Values,
	headers map[string]string,
	options RequestOptions,
) (Response, error) {
	requestHeaders := make(map[string]string, len(headers)+1)
	for key, value := range headers {
		requestHeaders[key] = value
	}
	requestHeaders["Content-Type"] = "application/x-www-form-urlencoded"
	return c.do(ctx, http.MethodPost, targetURL, trustedEndpoints, requestHeaders, strings.NewReader(form.Encode()), options)
}

func (c *Client) do(
	ctx context.Context,
	method string,
	targetURL string,
	trustedEndpoints []string,
	headers map[string]string,
	body io.Reader,
	options RequestOptions,
) (Response, error) {
	if c == nil || c.httpClients == nil {
		return Response{}, fmt.Errorf("identity provider client is not configured")
	}
	trustedEndpoint, err := trustedEndpointFor(targetURL, trustedEndpoints)
	if err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimSpace(targetURL), body)
	if err != nil {
		return Response{}, fmt.Errorf("build identity provider request: %w", err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}

	variant := ""
	if options.TLSInsecureSkipVerify {
		variant = tlsInsecureSkipVerifyClientVariant
	}
	response, err := c.httpClients.Do(request, trustedEndpoint, variant)
	if err != nil {
		return Response{}, fmt.Errorf("request identity provider: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponseSize+1))
	if err != nil {
		return Response{}, fmt.Errorf("read identity provider response: %w", err)
	}
	if len(responseBody) > maxResponseSize {
		return Response{}, fmt.Errorf("identity provider response exceeds %d bytes", maxResponseSize)
	}
	return Response{
		StatusCode: response.StatusCode,
		Status:     response.Status,
		Body:       responseBody,
	}, nil
}

func trustedEndpointFor(targetURL string, trustedEndpoints []string) (string, error) {
	targetOrigin, err := security.HTTPOrigin(targetURL)
	if err != nil {
		return "", err
	}
	trustedEndpoint := ""
	for _, endpoint := range trustedEndpoints {
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		endpointOrigin, originErr := security.HTTPOrigin(endpoint)
		if originErr != nil {
			return "", fmt.Errorf("invalid configured identity provider endpoint: %w", originErr)
		}
		if endpointOrigin == targetOrigin {
			trustedEndpoint = endpoint
			break
		}
	}
	return trustedEndpoint, nil
}

// CloseIdleConnections 关闭严格客户端和所有可信 origin 客户端的空闲连接。
func (c *Client) CloseIdleConnections() {
	if c == nil {
		return
	}
	c.httpClients.CloseIdleConnections()
}

func newIdentityProviderHTTPClient(policy security.OutboundPolicy, trustedOrigin string, variant string) (outboundhttp.ManagedClient, error) {
	if variant != "" && variant != tlsInsecureSkipVerifyClientVariant {
		return outboundhttp.ManagedClient{}, fmt.Errorf("unsupported identity provider HTTP client variant")
	}
	client := security.NewOutboundHTTPClient(policy, requestTimeout)
	baseTransport, ok := client.Transport.(*http.Transport)
	if !ok {
		return outboundhttp.ManagedClient{}, fmt.Errorf("identity provider transport is unavailable")
	}
	if variant == tlsInsecureSkipVerifyClientVariant {
		baseTransport = baseTransport.Clone()
		tlsConfig := baseTransport.TLSClientConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tlsConfig = tlsConfig.Clone()
			if tlsConfig.MinVersion < tls.VersionTLS12 {
				tlsConfig.MinVersion = tls.VersionTLS12
			}
		}
		// Security: this isolated pool variant is selected only by an administrator-enabled provider setting.
		// #nosec G402 -- skipping verification is the explicit purpose of this opt-in transport.
		tlsConfig.InsecureSkipVerify = true
		baseTransport.TLSClientConfig = tlsConfig
	}
	client.Transport = baseTransport
	client.Transport = platformtracing.NewHTTPTransport(client.Transport)
	if trustedOrigin != "" {
		client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
			redirectOrigin, err := security.HTTPOrigin(request.URL.String())
			if err != nil || redirectOrigin != trustedOrigin {
				return fmt.Errorf("identity provider redirect changed trusted origin")
			}
			return nil
		}
	}
	managed := outboundhttp.ManagedClient{Client: client, CloseIdleConnections: baseTransport.CloseIdleConnections}
	return managed, nil
}
