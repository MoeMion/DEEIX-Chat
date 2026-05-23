package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// geminiImageGenerationAdapter 实现 Google Gemini 图片生成/编辑协议。
type geminiImageGenerationAdapter struct {
	client *Client
}

func (a *geminiImageGenerationAdapter) Name() string { return AdapterGoogleImageGeneration }

// Generate 调用 Gemini generateContent 图片能力，返回结构化图片结果。
func (a *geminiImageGenerationAdapter) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	return a.client.generateGeminiImageGeneration(ctx, route, input)
}

// GenerateStream 调用 Gemini streamGenerateContent；最终图片仍由媒体任务落库。
func (a *geminiImageGenerationAdapter) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	return a.client.generateGeminiImageGenerationStream(ctx, route, input, onEvent)
}

// ListModels 复用 Gemini models 目录，供渠道校验和展示使用。
func (a *geminiImageGenerationAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	return a.client.listModelsGemini(ctx, route)
}

// generateGeminiImageGeneration 调用 generateContent，并强制请求图片模态输出。
func (c *Client) generateGeminiImageGeneration(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	base := geminiBaseURL(route)
	requestURL := buildGeminiGenerateURL(base, normalizeGeminiImageGenerationModel(route.UpstreamModel))

	requestBody, err := buildGeminiImageGenerationRequestBody(input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()

	req, err := c.newGeminiRequest(requestCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}

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
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	return parseGeminiImageGenerationOutput(body)
}

// generateGeminiImageGenerationStream 调用 Gemini 图片 SSE 输出并复用 GenerateContentResponse 解析。
func (c *Client) generateGeminiImageGenerationStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	base := geminiBaseURL(route)
	requestURL := buildGeminiStreamURL(base, normalizeGeminiImageGenerationModel(route.UpstreamModel))

	requestBody, err := buildGeminiImageGenerationRequestBody(input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	firstByteCtx, firstByteCancel := context.WithCancel(ctx)
	defer firstByteCancel()

	firstByteTimer := time.AfterFunc(resolveReadTimeout(route.ReadTimeoutMS), firstByteCancel)
	defer firstByteTimer.Stop()

	req, err := c.newGeminiRequest(firstByteCtx, http.MethodPost, requestURL, bytes.NewReader(payload), route)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClientForRoute(route).Do(req)
	firstByteTimer.Stop()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readUpstreamBody(resp.Body)
		return nil, parseGeminiError(resp.StatusCode, body, upstreamDebugSnapshot(req, payload, resp, body))
	}

	result := &GenerateOutput{
		ToolCalls: make([]ToolCall, 0),
	}
	idleReader := newIdleTimeoutReader(resp.Body, resolveStreamIdleTimeout(route.StreamIdleTimeoutMS))
	streamBody := newUpstreamBodyRecorder(idleReader)
	if err = consumeGeminiStream(streamBody, result, onEvent); err != nil {
		return nil, attachUpstreamDebug(err, upstreamDebugSnapshot(req, payload, resp, streamErrorBody(streamBody, err)))
	}
	for i := range result.GeneratedImages {
		result.GeneratedImages[i].RevisedPrompt = strings.TrimSpace(result.Text)
	}
	return result, nil
}

// normalizeGeminiImageGenerationModel 将产品别名收敛为 Google API 接受的真实模型 ID。
func normalizeGeminiImageGenerationModel(model string) string {
	switch strings.TrimSpace(strings.ToLower(model)) {
	case "nano-banana-2":
		return "gemini-3.1-flash-image-preview"
	case "nano-banana-pro":
		return "gemini-3-pro-image-preview"
	case "nano-banana":
		return "gemini-2.5-flash-image"
	default:
		return strings.TrimSpace(model)
	}
}

// buildGeminiImageGenerationRequestBody 构造 Gemini 图片生成/编辑请求字段。
func buildGeminiImageGenerationRequestBody(input GenerateInput) (map[string]interface{}, error) {
	prompt := buildOpenAIImageGenerationPrompt(input.Messages)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("image generation prompt required")
	}

	generationConfig := map[string]interface{}{
		"responseModalities": []string{"TEXT", "IMAGE"},
	}
	applyGeminiImageGenerationParams(generationConfig, input.Options)

	return map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"role":  "user",
				"parts": buildGeminiImageGenerationParts(prompt, collectImageInputParts(input.Messages)),
			},
		},
		"generationConfig": generationConfig,
	}, nil
}

// buildGeminiImageGenerationParts 按 Google GenerateContent 格式组合文本提示词和编辑输入图。
func buildGeminiImageGenerationParts(prompt string, images []ContentPart) []map[string]interface{} {
	parts := []map[string]interface{}{
		{"text": strings.TrimSpace(prompt)},
	}
	for _, image := range images {
		if len(image.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(image.MimeType)
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		parts = append(parts, map[string]interface{}{
			"inline_data": map[string]interface{}{
				"mime_type": mimeType,
				"data":      base64.StdEncoding.EncodeToString(image.Data),
			},
		})
	}
	return parts
}

// applyGeminiImageGenerationParams 映射 Google 图片生成文档中的 responseFormat.image 参数。
func applyGeminiImageGenerationParams(generationConfig map[string]interface{}, options map[string]interface{}) {
	if len(options) == 0 {
		return
	}
	imageConfig := map[string]interface{}{}
	mergeGeminiImageConfig(imageConfig, modelParamMap(options, "imageConfig"))
	mergeGeminiImageConfig(imageConfig, modelParamMap(options, "image_config"))
	if format := modelParamMap(options, "responseFormat"); len(format) > 0 {
		mergeGeminiImageConfig(imageConfig, asMap(format["image"]))
	}
	if generation := modelParamMap(options, "generationConfig"); len(generation) > 0 {
		mergeGeminiImageConfig(imageConfig, asMap(generation["imageConfig"]))
		mergeGeminiImageConfig(imageConfig, asMap(generation["image_config"]))
		if format := asMap(generation["responseFormat"]); len(format) > 0 {
			mergeGeminiImageConfig(imageConfig, asMap(format["image"]))
		}
	}
	if aspectRatio := firstGeminiStringOption(options, "aspect_ratio", "aspectRatio"); aspectRatio != "" {
		imageConfig["aspectRatio"] = aspectRatio
	}
	if imageSize := firstGeminiStringOption(options, "image_size", "imageSize"); imageSize != "" {
		imageConfig["imageSize"] = imageSize
	}
	if len(imageConfig) > 0 {
		generationConfig["responseFormat"] = map[string]interface{}{"image": imageConfig}
	}
}

func mergeGeminiImageConfig(dst map[string]interface{}, raw map[string]interface{}) {
	for key, value := range raw {
		switch key {
		case "aspectRatio", "imageSize":
			dst[key] = value
		case "aspect_ratio":
			dst["aspectRatio"] = value
		case "image_size":
			dst["imageSize"] = value
		default:
			if strings.TrimSpace(key) != "" {
				dst[key] = value
			}
		}
	}
}

// parseGeminiImageGenerationOutput 抽取 Gemini inlineData 图片，文本片段只作为 revised prompt。
func parseGeminiImageGenerationOutput(body []byte) (*GenerateOutput, error) {
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	revisedPrompt := extractGeminiText(parsed)
	result := &GenerateOutput{
		ResponseID:      strings.TrimSpace(getString(parsed["responseId"])),
		Text:            revisedPrompt,
		Usage:           parseGeminiUsage(parsed),
		ToolCalls:       make([]ToolCall, 0),
		ServerToolCalls: make([]ToolCall, 0),
		GeneratedImages: extractGeminiGeneratedImages(parsed, revisedPrompt),
		RawJSON:         string(body),
	}
	return result, nil
}

// extractGeminiGeneratedImages 扫描所有候选内容，避免只读取第一个候选导致丢图。
func extractGeminiGeneratedImages(parsed map[string]interface{}, revisedPrompt string) []GeneratedImage {
	images := make([]GeneratedImage, 0)
	for _, rawCandidate := range asSlice(parsed["candidates"]) {
		candidate := asMap(rawCandidate)
		content := asMap(candidate["content"])
		for _, rawPart := range asSlice(content["parts"]) {
			part := asMap(rawPart)
			if isGeminiThoughtPart(part) {
				continue
			}
			inlineData := asMap(part["inlineData"])
			if len(inlineData) == 0 {
				// Google 文档的 REST 与 SDK 示例同时出现 camelCase / snake_case，适配器边界统一收敛。
				inlineData = asMap(part["inline_data"])
			}
			if len(inlineData) == 0 {
				continue
			}
			b64 := strings.TrimSpace(getString(inlineData["data"]))
			if b64 == "" {
				continue
			}
			mimeType := strings.TrimSpace(getString(inlineData["mimeType"]))
			if mimeType == "" {
				mimeType = strings.TrimSpace(getString(inlineData["mime_type"]))
			}
			if mimeType == "" {
				mimeType = "image/png"
			}
			images = append(images, GeneratedImage{
				B64JSON:       b64,
				MIMEType:      mimeType,
				RevisedPrompt: revisedPrompt,
			})
		}
	}
	return images
}

func isGeminiThoughtPart(part map[string]interface{}) bool {
	thought, ok := part["thought"].(bool)
	return ok && thought
}
