package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoOCR{}) }

type videoOCR struct{}

func (videoOCR) Name() string        { return "video_ocr" }
func (videoOCR) ReadOnly() bool      { return true }
func (videoOCR) Description() string { return "Extract text from a video frame image using tesseract OCR. Requires tesseract installed (tesseract). Returns gracefully if unavailable." }

func (videoOCR) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"frame_path":{"type":"string","description":"Path to the frame image (jpg/png)"},"languages":{"type":"string","description":"OCR languages, e.g. 'eng+chi_sim' (default: 'eng+chi_sim')","default":"eng+chi_sim"}},"required":["frame_path"]}`)
}

func (videoOCR) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		FramePath string `json:"frame_path"`
		Languages string `json:"languages"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.FramePath == "" {
		return "", fmt.Errorf("frame_path is required")
	}
	if p.Languages == "" {
		p.Languages = "eng+chi_sim"
	}

	if _, err := exec.LookPath("tesseract"); err != nil {
		result := map[string]interface{}{
			"available": false,
			"texts":     []string{},
			"error":     "tesseract not installed. Install: brew install tesseract (macOS) or apt install tesseract-ocr (Linux)",
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	cmd := exec.CommandContext(ctx, "tesseract", p.FramePath, "stdout",
		"-l", p.Languages, "--psm", "6", "--oem", "1",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tesseract failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var texts []map[string]interface{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) >= 2 {
			texts = append(texts, map[string]interface{}{
				"text":       line,
				"confidence": 0,
			})
		}
	}

	result := map[string]interface{}{
		"available": true,
		"count":     len(texts),
		"texts":     texts,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}
