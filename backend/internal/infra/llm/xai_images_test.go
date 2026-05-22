package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildXAIImageRequestBody(t *testing.T) {
	payload, err := buildXAIImageRequestBody("grok-imagine-image-quality", GenerateInput{
		Messages: []Message{
			{Role: "system", Content: "ignore"},
			{Role: "user", Content: "A clean product render"},
		},
		Options: map[string]interface{}{
			"aspect_ratio":    "16:9",
			"n":               2,
			"resolution":      "2k",
			"response_format": "b64_json",
			"prompt":          "override",
			"stream":          true,
			"quality":         "high",
		},
	})
	if err != nil {
		t.Fatalf("build xAI image request body: %v", err)
	}
	if payload["model"] != "grok-imagine-image-quality" || payload["prompt"] != "A clean product render" {
		t.Fatalf("unexpected model or prompt: %#v", payload)
	}
	if payload["aspect_ratio"] != "16:9" || payload["n"] != 2 || payload["resolution"] != "2k" || payload["response_format"] != "b64_json" {
		t.Fatalf("expected xAI image params, got %#v", payload)
	}
	for _, key := range []string{"stream", "quality"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("unexpected xAI image param %q in payload %#v", key, payload)
		}
	}
}

func TestGenerateXAIImageUsesImageEndpoint(t *testing.T) {
	var requestPath string
	var requestBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if got := r.Header.Get("Authorization"); got != "Bearer xai-key" {
			t.Fatalf("unexpected auth header %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "img_xai_1",
			"data": [
				{"url": "https://example.com/generated.jpg"}
			],
			"usage": {"input_tokens": 11, "output_tokens": 1}
		}`))
	}))
	defer server.Close()

	output, err := NewClient().Generate(context.Background(), RouteConfig{
		Protocol:      AdapterXAIImage,
		BaseURL:       server.URL + "/v1",
		APIKey:        "xai-key",
		UpstreamModel: "grok-imagine-image-quality",
	}, GenerateInput{
		Messages: []Message{{Role: "user", Content: "A clean product render"}},
		Options: map[string]interface{}{
			"aspect_ratio": "16:9",
		},
	})
	if err != nil {
		t.Fatalf("generate xAI image: %v", err)
	}
	if requestPath != "/v1/images/generations" {
		t.Fatalf("expected xAI images endpoint, got %q", requestPath)
	}
	if requestBody["model"] != "grok-imagine-image-quality" || requestBody["aspect_ratio"] != "16:9" {
		t.Fatalf("unexpected request body: %#v", requestBody)
	}
	if output.ResponseID != "img_xai_1" || len(output.GeneratedImages) != 1 || output.GeneratedImages[0].URL != "https://example.com/generated.jpg" {
		t.Fatalf("unexpected generated image output: %#v", output)
	}
	if output.Usage.InputTokens != 11 || output.Usage.OutputTokens != 1 {
		t.Fatalf("expected upstream usage, got %#v", output.Usage)
	}
}

func TestParseXAIImageOutput(t *testing.T) {
	output, err := parseXAIImageOutput([]byte(`{
		"id": "img_xai_1",
		"data": [
			{"url": "https://example.com/a.jpg"},
			{"b64_json": "aGVsbG8=", "revised_prompt": "A revised render"}
		]
	}`), "b64_json")
	if err != nil {
		t.Fatalf("parse xAI image output: %v", err)
	}
	if output.Text != "" {
		t.Fatalf("xAI image adapter must not write image data into text, got %q", output.Text)
	}
	if len(output.GeneratedImages) != 2 {
		t.Fatalf("expected two generated images, got %#v", output.GeneratedImages)
	}
	if output.GeneratedImages[0].URL != "https://example.com/a.jpg" {
		t.Fatalf("unexpected URL image: %#v", output.GeneratedImages[0])
	}
	if output.GeneratedImages[1].B64JSON != "aGVsbG8=" || output.GeneratedImages[1].MIMEType != "image/jpeg" {
		t.Fatalf("unexpected base64 image: %#v", output.GeneratedImages[1])
	}
	if len(output.Citations) != 1 || output.Citations[0] != "https://example.com/a.jpg" {
		t.Fatalf("expected URL citation, got %#v", output.Citations)
	}
}
