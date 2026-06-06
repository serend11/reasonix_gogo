package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoTranscribe{}) }

type videoTranscribe struct{}

func (videoTranscribe) Name() string        { return "video_transcribe" }
func (videoTranscribe) ReadOnly() bool      { return true }
func (videoTranscribe) Description() string { return "Transcribe audio from a video file. Tries local whisper.cpp first, falls back to OpenAI Whisper API. Requires ffmpeg to extract audio." }

func (videoTranscribe) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"video_path":{"type":"string","description":"Path to the video file"},"language":{"type":"string","description":"Language hint for transcription (e.g. zh, en, ja). Empty = auto-detect"}},"required":["video_path"]}`)
}

type TranscribeResult struct {
	Engine   string           `json:"engine"`
	Text     string           `json:"text"`
	Segments []TranscribeSeg  `json:"segments,omitempty"`
	Language string           `json:"language,omitempty"`
}

type TranscribeSeg struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

func (videoTranscribe) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		VideoPath string `json:"video_path"`
		Language  string `json:"language"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.VideoPath == "" {
		return "", fmt.Errorf("video_path is required")
	}

	// Extract audio to temp WAV
	tmpDir, err := os.MkdirTemp("", "reasonix-transcribe-")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	wavPath := filepath.Join(tmpDir, "audio.wav")
	if !extractAudio(ctx, p.VideoPath, wavPath) {
		return "", fmt.Errorf("failed to extract audio — video may have no audio track")
	}

	// Try whisper.cpp first
	result := tryWhisperCPP(ctx, wavPath, p.Language)
	if result != nil {
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	// Fallback to OpenAI Whisper API
	result = tryOpenAIWhisper(ctx, wavPath, p.Language)
	if result != nil {
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b), nil
	}

	return "", fmt.Errorf("transcription unavailable: no whisper.cpp found and OPENAI_API_KEY not set")
}

func extractAudio(ctx context.Context, videoPath, wavPath string) bool {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", videoPath,
		"-vn", "-ar", "16000", "-ac", "1", "-sample_fmt", "s16",
		wavPath,
	)
	cmd.Run()
	_, err := os.Stat(wavPath)
	return err == nil
}

func tryWhisperCPP(ctx context.Context, wavPath, language string) *TranscribeResult {
	bin := "whisper-cpp"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "whisper"
		if _, err := exec.LookPath(bin); err != nil {
			return nil
		}
	}

	modelPath := findWhisperModel()
	if modelPath == "" {
		return nil
	}

	args := []string{"-m", modelPath, "-f", wavPath, "-oj", "-of", wavPath}
	if language != "" {
		args = append(args, "-l", language)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Run()

	jsonPath := wavPath + ".json"
	data, err := os.ReadFile(jsonPath)
	os.Remove(jsonPath)
	if err != nil {
		return nil
	}

	var raw struct {
		Transcription []struct {
			Timestamps struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"timestamps"`
			Text string `json:"text"`
		} `json:"transcription"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		// Try simpler format
		var simple struct {
			Text string `json:"text"`
		}
		if err2 := json.Unmarshal(data, &simple); err2 != nil {
			return nil
		}
		return &TranscribeResult{Engine: "whisper.cpp", Text: simple.Text}
	}

	tr := &TranscribeResult{Engine: "whisper.cpp", Text: raw.Text}
	for _, s := range raw.Transcription {
		var start, end float64
		fmt.Sscanf(s.Timestamps.From, "%f", &start)
		fmt.Sscanf(s.Timestamps.To, "%f", &end)
		tr.Segments = append(tr.Segments, TranscribeSeg{Start: start, End: end, Text: s.Text})
	}
	return tr
}

func findWhisperModel() string {
	dirs := []string{
		os.ExpandEnv("$HOME/.cache/whisper"),
		os.ExpandEnv("$HOME/whisper.cpp/models"),
		"/usr/local/share/whisper",
	}
	models := []string{"ggml-base.bin", "ggml-small.bin", "ggml-tiny.bin", "ggml-base.en.bin"}
	for _, d := range dirs {
		for _, m := range models {
			p := filepath.Join(d, m)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func tryOpenAIWhisper(ctx context.Context, wavPath, language string) *TranscribeResult {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	_ = w.WriteField("model", "whisper-1")
	_ = w.WriteField("response_format", "verbose_json")
	if language != "" {
		_ = w.WriteField("language", language)
	}

	fw, _ := w.CreateFormFile("file", "audio.wav")
	f, err := os.Open(wavPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	io.Copy(fw, f)
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/audio/transcriptions", &body)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil
	}

	var raw struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}

	tr := &TranscribeResult{
		Engine:   "openai-whisper",
		Text:     raw.Text,
		Language: raw.Language,
	}
	for _, s := range raw.Segments {
		tr.Segments = append(tr.Segments, TranscribeSeg{Start: s.Start, End: s.End, Text: s.Text})
	}
	return tr
}
