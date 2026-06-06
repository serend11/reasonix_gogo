// Package google implements the Gemini API provider (generateContent +
// streamGenerateContent with SSE) using a hand-written net/http client — no SDK.
// It self-registers under the "google" kind, so any Gemini model is a config
// instance rather than code.
//
// Vision is supported: when provider.Message has non-empty Images, the first
// image is sent as an inlineData part alongside the text prompt. For multi-image
// or advanced use-cases, use the video_vision built-in tool instead.
package google

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"reasonix/internal/netclient"
	"reasonix/internal/provider"
)

func init() {
	provider.Register("google", New)
}

// New builds a Gemini provider from a resolved config.
func New(cfg provider.Config) (provider.Provider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("google: model is required for provider %q", cfg.Name)
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("google: API key is required for provider %q — set GOOGLE_API_KEY", cfg.Name)
	}
	name := cfg.Name
	if name == "" {
		name = "google"
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	keyEnv, _ := cfg.Extra["api_key_env"].(string)
	httpClient, err := newHTTPClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("google: network: %w", err)
	}
	return &client{
		name:    name,
		apiKey:  cfg.APIKey,
		keyEnv:  keyEnv,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   cfg.Model,
		http:    httpClient,
	}, nil
}

func newHTTPClient(cfg provider.Config) (*http.Client, error) {
	spec, _ := cfg.Extra["proxy_spec"].(netclient.ProxySpec)
	return netclient.NewHTTPClient(spec, netclient.TransportOptions{})
}

type client struct {
	name    string
	apiKey  string
	keyEnv  string
	baseURL string
	model   string
	http    *http.Client
}

func (c *client) Name() string { return c.name }

var bufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	body, err := json.Marshal(c.buildRequest(req))
	if err != nil {
		bufPool.Put(buf)
		return nil, fmt.Errorf("%s: marshal request: %w", c.name, err)
	}
	bufPool.Put(buf)

	// Gemini streaming endpoint: .../models/{model}:streamGenerateContent?alt=sse&key={key}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s",
		c.baseURL, url.PathEscape(c.model), url.QueryEscape(c.apiKey))

	newReq := func(ctx context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		return httpReq, nil
	}
	resp, err := provider.SendWithRetry(ctx, c.http, c.name, c.keyEnv, newReq)
	if err != nil {
		return nil, err
	}

	out := make(chan provider.Chunk)
	go c.readStream(resp, out)
	return out, nil
}

// buildRequest converts the transport-agnostic Request into Gemini's
// generateContent shape. System messages become system_instruction; the
// conversation is flattened into a single contents array (Gemini doesn't
// enforce strict user/model alternation). Tool definitions become
// function_declarations in the tools array with Gemini's flat schema shape.
func (c *client) buildRequest(req provider.Request) geminiRequest {
	var systemParts []geminiPart
	var contents []geminiContent

	msgs := provider.SanitizeToolPairing(req.Messages)
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleSystem:
			if m.Content != "" {
				systemParts = append(systemParts, geminiPart{Text: m.Content})
			}
		case provider.RoleUser:
			var parts []geminiPart
			// Attach images from the message when present (vision).
			for _, img := range m.Images {
				parts = append(parts, geminiPart{
					InlineData: &geminiInlineData{
						MimeType: img.MimeType,
						Data:     img.Data,
					},
				})
			}
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "user", Parts: parts})
			}
		case provider.RoleTool:
			// Tool results become function_response parts in a "function" role turn.
			contents = append(contents, geminiContent{
				Role: "function",
				Parts: []geminiPart{{
					FunctionResponse: &geminiFunctionResponse{
						Name:     m.Name,
						Response: json.RawMessage(jsonString(m.Content)),
					},
				}},
			})
		case provider.RoleAssistant:
			var parts []geminiPart
			if m.ReasoningContent != "" {
				parts = append(parts, geminiPart{Text: m.ReasoningContent})
			}
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Arguments)
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "model", Parts: parts})
			}
		}
	}

	var tools []geminiTool
	if len(req.Tools) > 0 {
		var decls []geminiFunctionDecl
		for _, t := range req.Tools {
			schema := t.Parameters
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			decls = append(decls, geminiFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			})
		}
		tools = append(tools, geminiTool{
			FunctionDeclarations: decls,
		})
	}

	gr := geminiRequest{
		Contents: contents,
		Tools:    tools,
		GenerationConfig: &geminiGenerationConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		},
	}
	if len(systemParts) > 0 {
		gr.SystemInstruction = &geminiContent{Role: "system", Parts: systemParts}
	}
	return gr
}

// readStream parses the Gemini SSE stream into Chunks. Text parts emit as
// ChunkText; function_call parts accumulate by name and emit a ChunkToolCall
// when the part completes; usage is extracted from usageMetadata on the final
// chunk.
func (c *client) readStream(resp *http.Response, out chan<- provider.Chunk) {
	defer resp.Body.Close()
	defer close(out)

	var textBuf strings.Builder
	var acc *provider.ToolCall // accumulating function call
	var usage *provider.Usage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.Trim(strings.TrimPrefix(line, "data:"), " \t\n\r")
		if data == "" || data == "[DONE]" {
			continue
		}

		var resp geminiStreamResponse
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			out <- provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("%s: decode stream: %w", c.name, err)}
			return
		}

		// Check for errors
		if resp.Error != nil && resp.Error.Message != "" {
			out <- provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("%s: %s (status %s)", c.name, resp.Error.Message, resp.Error.Status)}
			return
		}

		// Collect usage metadata
		if resp.UsageMetadata != nil {
			usage = &provider.Usage{
				PromptTokens:     resp.UsageMetadata.PromptTokenCount,
				CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      resp.UsageMetadata.TotalTokenCount,
				CacheHitTokens:   resp.UsageMetadata.CachedContentTokenCount,
				CacheMissTokens:  resp.UsageMetadata.PromptTokenCount - resp.UsageMetadata.CachedContentTokenCount,
			}
		}

		if len(resp.Candidates) == 0 {
			continue
		}
		candidate := resp.Candidates[0]

		// Check finish reason
		if candidate.FinishReason != "" && usage != nil {
			usage.FinishReason = mapGeminiFinishReason(candidate.FinishReason)
		}

		if candidate.Content == nil {
			continue
		}

		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				textBuf.WriteString(part.Text)
				out <- provider.Chunk{Type: provider.ChunkText, Text: part.Text}
			}
			if part.FunctionCall != nil {
				// Start a new tool call
				if acc != nil {
					// Flush previous if any
					out <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: acc}
				}
				acc = &provider.ToolCall{
					ID:        fmt.Sprintf("call_%s", part.FunctionCall.Name),
					Name:      part.FunctionCall.Name,
					Arguments: string(part.FunctionCall.Args),
				}
				out <- provider.Chunk{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: acc.ID, Name: acc.Name}}
				out <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: acc}
				acc = nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		out <- provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("%s: read stream: %w", c.name, err)}
		return
	}

	if usage != nil {
		out <- provider.Chunk{Type: provider.ChunkUsage, Usage: usage}
	}
	out <- provider.Chunk{Type: provider.ChunkDone}
}

func mapGeminiFinishReason(r string) string {
	switch r {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default:
		return strings.ToLower(r)
	}
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

// --- Gemini wire protocol ---

type geminiRequest struct {
	SystemInstruction *geminiContent          `json:"system_instruction,omitempty"`
	Contents          []geminiContent          `json:"contents"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type geminiStreamResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
	Error         *geminiError         `json:"error"`
}

type geminiCandidate struct {
	Content      *geminiContent `json:"content"`
	FinishReason string         `json:"finishReason"`
}

type geminiUsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
