package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"reasonix/internal/netclient"
)

func init() { tool.RegisterBuiltin(videoVision{}) }

// videoVision analyzes a single image frame using any configured vision-capable AI
// provider. It directly calls the provider API (not the agent loop) so it works
// as a tool the agent invokes to get frame descriptions.
type videoVision struct{}

func (videoVision) Name() string   { return "video_vision" }
func (videoVision) ReadOnly() bool { return true }
func (videoVision) Description() string {
	return "Analyze a video frame image with AI vision. Supports OpenAI (GPT-4V/GPT-4o), Anthropic (Claude), Google (Gemini), Ollama, and any OpenAI-compatible endpoint. Requires the corresponding API key in environment."
}

func (videoVision) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"frame_path":{"type":"string","description":"Path to the frame image file (jpg/png/webp)"},"provider":{"type":"string","description":"AI provider: openai, anthropic, google, ollama, openai-compatible","enum":["openai","anthropic","google","ollama","openai-compatible"],"default":"openai"},"model":{"type":"string","description":"Vision model name (provider default if empty)"},"prompt":{"type":"string","description":"What to describe in the frame (default: comprehensive scene description)","default":"Describe this video frame in detail. Include: setting, people, objects, text/UI visible, mood, and notable details."},"api_key":{"type":"string","description":"API key (reads from env var if empty: OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY)"},"base_url":{"type":"string","description":"Custom API base URL (overrides default)"},"max_tokens":{"type":"integer","description":"Max response tokens","default":500},"detail":{"type":"string","description":"Image detail level (OpenAI only: low, high, auto)","enum":["low","high","auto"],"default":"low"}},"required":["frame_path"]}`)
}

type VisionResult struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Analysis string `json:"analysis"`
}

func (videoVision) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		FramePath string `json:"frame_path"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
		Prompt    string `json:"prompt"`
		APIKey    string `json:"api_key"`
		BaseURL   string `json:"base_url"`
		MaxTokens int    `json:"max_tokens"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.FramePath == "" {
		return "", fmt.Errorf("frame_path is required")
	}
	if p.Provider == "" {
		p.Provider = "openai"
	}
	if p.Prompt == "" {
		p.Prompt = "Describe this video frame in detail. Include: setting, people, objects, text/UI visible, mood, and notable details."
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = 500
	}
	if p.Detail == "" {
		p.Detail = "low"
	}

	// Read and encode image
	data, err := os.ReadFile(p.FramePath)
	if err != nil {
		return "", fmt.Errorf("read frame: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("frame file is empty")
	}
	imageB64 := base64.StdEncoding.EncodeToString(data)

	// Detect MIME type
	ext := strings.ToLower(p.FramePath)
	mime := "image/jpeg"
	switch {
	case strings.HasSuffix(ext, ".png"):
		mime = "image/png"
	case strings.HasSuffix(ext, ".webp"):
		mime = "image/webp"
	case strings.HasSuffix(ext, ".gif"):
		mime = "image/gif"
	}

	// Resolve API key
	apiKey := p.APIKey
	if apiKey == "" {
		switch p.Provider {
		case "openai", "openai-compatible":
			apiKey = os.Getenv("OPENAI_API_KEY")
		case "anthropic":
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		case "google":
			apiKey = os.Getenv("GOOGLE_API_KEY")
		}
	}
	if apiKey == "" && p.Provider != "ollama" {
		return "", fmt.Errorf("no API key for provider %q — set the corresponding env var or pass api_key", p.Provider)
	}

	// Resolve model
	model := p.Model
	if model == "" {
		model = defaultVisionModel(p.Provider)
	}

	// Resolve base URL
	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL(p.Provider)
	}

	httpClient, _ := netclient.NewHTTPClient(netclient.ProxySpec{}, netclient.TransportOptions{})

	switch p.Provider {
	case "openai", "openai-compatible":
		result, err := callOpenAIVision(ctx, httpClient, baseURL, apiKey, model, imageB64, mime, p.Prompt, p.MaxTokens, p.Detail)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(VisionResult{Provider: p.Provider, Model: model, Analysis: result}, "", "  ")
		return string(b), nil

	case "anthropic":
		result, err := callAnthropicVision(ctx, httpClient, baseURL, apiKey, model, imageB64, mime, p.Prompt, p.MaxTokens)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(VisionResult{Provider: p.Provider, Model: model, Analysis: result}, "", "  ")
		return string(b), nil

	case "google":
		result, err := callGoogleVision(ctx, httpClient, apiKey, model, imageB64, mime, p.Prompt, p.MaxTokens)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(VisionResult{Provider: p.Provider, Model: model, Analysis: result}, "", "  ")
		return string(b), nil

	case "ollama":
		result, err := callOllamaVision(ctx, httpClient, baseURL, model, imageB64, p.Prompt, p.MaxTokens)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(VisionResult{Provider: p.Provider, Model: model, Analysis: result}, "", "  ")
		return string(b), nil

	default:
		return "", fmt.Errorf("unsupported provider: %s", p.Provider)
	}
}

func defaultVisionModel(provider string) string {
	switch provider {
	case "openai", "openai-compatible":
		return "gpt-4o"
	case "anthropic":
		return "claude-3-5-sonnet-20241022"
	case "google":
		return "gemini-2.0-flash-exp"
	case "ollama":
		return "llava"
	default:
		return "gpt-4o"
	}
}

func defaultBaseURL(provider string) string {
	switch provider {
	case "openai":
		return "https://api.openai.com"
	case "anthropic":
		return "https://api.anthropic.com"
	case "google":
		return "https://generativelanguage.googleapis.com"
	case "ollama":
		return "http://localhost:11434"
	default:
		return "https://api.openai.com"
	}
}

// ─── OpenAI Vision ────────────────────────────────────────────────────

func callOpenAIVision(ctx context.Context, httpClient *http.Client, baseURL, apiKey, model, imageB64, mime, prompt string, maxTokens int, detail string) (string, error) {
	body := map[string]interface{}{
		"model":       model,
		"max_tokens":  maxTokens,
		"temperature": 0.7,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url":    fmt.Sprintf("data:%s;base64,%s", mime, imageB64),
							"detail": detail,
						},
					},
					{"type": "text", "text": prompt},
				},
			},
		},
	}
	return callChatAPI(ctx, httpClient, baseURL+"/chat/completions", apiKey, body)
}

// ─── Anthropic Vision ─────────────────────────────────────────────────

func callAnthropicVision(ctx context.Context, httpClient *http.Client, baseURL, apiKey, model, imageB64, mime, prompt string, maxTokens int) (string, error) {
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": maxTokens,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "image",
						"source": map[string]string{
							"type":       "base64",
							"media_type": mime,
							"data":       imageB64,
						},
					},
					{"type": "text", "text": prompt},
				},
			},
		},
	}
	payload, _ := json.Marshal(body)
	return callHTTP(ctx, httpClient, "POST", baseURL+"/v1/messages", map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}, payload, func(data map[string]interface{}) string {
		parts := []string{}
		for _, block := range getSlice(data, "content") {
			if t, ok := block["text"].(string); ok {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	})
}

// ─── Google Vision ────────────────────────────────────────────────────

func callGoogleVision(ctx context.Context, httpClient *http.Client, apiKey, model, imageB64, mime, prompt string, maxTokens int) (string, error) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		model, apiKey)
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"inlineData": map[string]string{"mimeType": mime, "data": imageB64}},
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": maxTokens,
		},
	}
	payload, _ := json.Marshal(body)
	return callHTTP(ctx, httpClient, "POST", url, map[string]string{"Content-Type": "application/json"}, payload, func(data map[string]interface{}) string {
		candidates := getSlice(data, "candidates")
		if len(candidates) == 0 {
			return ""
		}
		content := candidates[0]["content"].(map[string]interface{})
		parts := getSlice(content, "parts")
		var texts []string
		for _, p := range parts {
			if t, ok := p["text"].(string); ok {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	})
}

// ─── Ollama Vision ────────────────────────────────────────────────────

func callOllamaVision(ctx context.Context, httpClient *http.Client, baseURL, model, imageB64, prompt string, maxTokens int) (string, error) {
	body := map[string]interface{}{
		"model":  model,
		"stream": false,
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": prompt,
				"images":  []string{imageB64},
			},
		},
		"options": map[string]interface{}{
			"num_predict": maxTokens,
		},
	}
	payload, _ := json.Marshal(body)
	return callHTTP(ctx, httpClient, "POST", baseURL+"/api/chat", map[string]string{"Content-Type": "application/json"}, payload, func(data map[string]interface{}) string {
		if msg, ok := data["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				return content
			}
		}
		return ""
	})
}

// ─── Generic OpenAI-compatible chat ───────────────────────────────────

func callChatAPI(ctx context.Context, httpClient *http.Client, url, apiKey string, body map[string]interface{}) (string, error) {
	payload, _ := json.Marshal(body)
	return callHTTP(ctx, httpClient, "POST", url, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
	}, payload, func(data map[string]interface{}) string {
		choices := getSlice(data, "choices")
		if len(choices) == 0 {
			return ""
		}
		if msg, ok := choices[0]["message"].(map[string]interface{}); ok {
			if content, ok := msg["content"].(string); ok {
				return content
			}
		}
		return ""
	})
}

// ─── HTTP helper ──────────────────────────────────────────────────────

func callHTTP(ctx context.Context, httpClient *http.Client, method, url string, headers map[string]string, payload []byte, extract func(map[string]interface{}) string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if resp.StatusCode >= 400 {
		errMsg := ""
		if e, ok := data["error"]; ok {
			switch v := e.(type) {
			case string:
				errMsg = v
			case map[string]interface{}:
				if msg, ok := v["message"].(string); ok {
					errMsg = msg
				}
			}
		}
		return "", fmt.Errorf("API error (HTTP %d)%s", resp.StatusCode, suffix(errMsg))
	}

	result := extract(data)
	if result == "" {
		return "", fmt.Errorf("empty response from API")
	}
	return result, nil
}

func getSlice(data map[string]interface{}, key string) []map[string]interface{} {
	if v, ok := data[key]; ok {
		if arr, ok := v.([]interface{}); ok {
			out := make([]map[string]interface{}, len(arr))
			for i, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					out[i] = m
				}
			}
			return out
		}
	}
	return nil
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}
