package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// xAIImageAdapter 实现 xAI 图片生成协议。
type xAIImageAdapter struct {
	client *Client
}

func (a *xAIImageAdapter) Name() string { return AdapterXAIImage }

// Generate 调用 xAI 图片生成接口，返回结构化图片结果。
func (a *xAIImageAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	return a.client.generateXAIImage(ctx, route, input)
}

// GenerateStream 当前不伪造图片流式；媒体任务会通过非流式调用落库生成结果。
func (a *xAIImageAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedStream, AdapterXAIImage)
}

// ListModels 复用 xAI models 目录，供渠道校验和展示使用。
func (a *xAIImageAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route.Protocol = AdapterXAIImage
	return a.client.listModelsOpenAICompatible(ctx, route)
}

// generateXAIImage 构造并执行 xAI Images API 请求。
func (c *Client) generateXAIImage(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	route.Protocol = AdapterXAIImage
	route.Endpoint = EndpointImageGenerations
	requestURL := buildOpenAIRequestURL(route.BaseURL, EndpointImageGenerations)
	if requestURL == "" {
		return nil, fmt.Errorf("invalid base url")
	}

	requestBody, err := buildXAIImageRequestBody(route.UpstreamModel, input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey := strings.TrimSpace(route.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	setAdditionalHeaders(req, route.HeadersJSON)

	resp, err := c.httpClientForRoute(route).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := readUpstreamBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseUpstreamError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	return parseXAIImageOutput(body, modelParamString(input.Options, "response_format"))
}

// buildXAIImageRequestBody 只允许 xAI 图片生成端点支持的字段进入上游。
func buildXAIImageRequestBody(model string, input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("image generation prompt required")
	}
	payload := map[string]interface{}{
		"model":  strings.TrimSpace(model),
		"prompt": prompt,
	}
	applyXAIImageParams(payload, input.Options)
	return payload, nil
}

// applyXAIImageParams 从 options 中提取 xAI 图片生成官方参数。
func applyXAIImageParams(payload map[string]interface{}, options map[string]interface{}) {
	if len(options) == 0 {
		return
	}
	for _, key := range []string{"aspect_ratio", "resolution", "response_format"} {
		if value := modelParamString(options, key); value != "" {
			payload[key] = value
		}
	}
	if value := modelParamInt(options, "n"); value > 0 {
		payload["n"] = value
	}
}

// parseXAIImageOutput 解析 xAI 图片响应；图片字节只进入 GeneratedImages。
func parseXAIImageOutput(body []byte, responseFormat string) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	result := &GenerateOutput{
		ResponseID:      strings.TrimSpace(getString(parsed["id"])),
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		RawJSON:         string(body),
	}
	if usage := parseOpenAICompatibleUsageForAdapter(AdapterXAIImage, parsed); usage != (Usage{}) {
		result.Usage = usage
	}
	data := asSlice(parsed["data"])
	citations := make([]string, 0, len(data))
	for _, item := range data {
		if image, ok := parseXAIImagePayload(asMap(item), responseFormat); ok {
			if url := strings.TrimSpace(image.URL); url != "" {
				citations = append(citations, url)
			}
			result.GeneratedImages = append(result.GeneratedImages, image)
		}
	}
	if len(data) == 0 {
		if image, ok := parseXAIImagePayload(parsed, responseFormat); ok {
			if url := strings.TrimSpace(image.URL); url != "" {
				citations = append(citations, url)
			}
			result.GeneratedImages = append(result.GeneratedImages, image)
		}
	}
	result.Citations = appendUniqueStrings(result.Citations, citations...)
	return result, nil
}

func parseXAIImagePayload(payload map[string]interface{}, responseFormat string) (GeneratedImage, bool) {
	if len(payload) == 0 {
		return GeneratedImage{}, false
	}
	revisedPrompt := strings.TrimSpace(getString(payload["revised_prompt"]))
	if revisedPrompt == "" {
		revisedPrompt = strings.TrimSpace(getString(payload["revisedPrompt"]))
	}
	if url := strings.TrimSpace(getString(payload["url"])); url != "" {
		return GeneratedImage{
			URL:           url,
			MIMEType:      xAIImageMIMEType(responseFormat),
			RevisedPrompt: revisedPrompt,
		}, true
	}
	if b64 := strings.TrimSpace(getString(payload["b64_json"])); b64 != "" {
		return GeneratedImage{
			B64JSON:       b64,
			MIMEType:      xAIImageMIMEType(responseFormat),
			RevisedPrompt: revisedPrompt,
		}, true
	}
	return GeneratedImage{}, false
}

// xAIImageMIMEType 根据 xAI 文档示例的默认图片格式给 base64 结果设置初始 MIME。
func xAIImageMIMEType(responseFormat string) string {
	switch strings.TrimSpace(strings.ToLower(responseFormat)) {
	case "b64_json", "url", "":
		return "image/jpeg"
	default:
		return "image/jpeg"
	}
}
