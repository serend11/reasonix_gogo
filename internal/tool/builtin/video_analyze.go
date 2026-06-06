package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoAnalyze{}) }

// videoAnalyze is the one-tool full video analysis pipeline. It chains:
// metadata → frame extraction → local perception → optional OCR/transcription →
// optional AI vision → report generation.
type videoAnalyze struct{}

func (videoAnalyze) Name() string   { return "video_analyze" }
func (videoAnalyze) ReadOnly() bool { return false }
func (videoAnalyze) Description() string {
	return "Complete AI-powered video analysis pipeline. Extracts metadata, scenes, frames, and optionally transcribes audio and analyzes frames with vision AI. Supports local mode (zero API cost) and vision mode (OpenAI/Anthropic/Google/Ollama). Outputs a comprehensive structured JSON report."
}

func (videoAnalyze) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"video_path":{"type":"string","description":"Path to the video file"},"output_dir":{"type":"string","description":"Output directory for frames, analysis, and report (auto-created if empty)"},"mode":{"type":"string","description":"Analysis mode: local (zero API, scene detection+color/motion) or vision (AI vision API analyzes each frame)","enum":["local","vision"],"default":"local"},"provider":{"type":"string","description":"AI provider for vision mode","enum":["openai","anthropic","google","ollama","openai-compatible"],"default":"openai"},"model":{"type":"string","description":"Vision model (provider default if empty)"},"interval":{"type":"number","description":"Seconds between frames (default: 10)","default":10},"max_frames":{"type":"integer","description":"Max frames to extract (default: 20)","default":20},"transcribe":{"type":"boolean","description":"Enable audio transcription","default":false},"language":{"type":"string","description":"Language hint for transcription (e.g. zh, en)"},"scene_threshold":{"type":"number","description":"Scene detection sensitivity 0-1 (default: 0.3)","default":0.3},"parallel":{"type":"integer","description":"Max concurrent frame analyses for vision mode (default: 5)","default":5}},"required":["video_path"]}`)
}

type AnalysisReport struct {
	Version    string      `json:"version"`
	AnalyzedAt string      `json:"analyzed_at"`
	Mode       string      `json:"mode"`
	Video      *VideoMeta  `json:"video"`
	Transcript *TranscribeResult `json:"transcript,omitempty"`
	Perception *json.RawMessage  `json:"perception,omitempty"`
	Frames     []FrameAnalysis   `json:"frames,omitempty"`
	Summary    string       `json:"summary,omitempty"`
}

type FrameAnalysis struct {
	Index     int    `json:"index"`
	Timestamp string `json:"timestamp"`
	Path      string `json:"path"`
	Vision    string `json:"vision,omitempty"`
}

func (videoAnalyze) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		VideoPath      string  `json:"video_path"`
		OutputDir      string  `json:"output_dir"`
		Mode           string  `json:"mode"`
		Provider       string  `json:"provider"`
		Model          string  `json:"model"`
		Interval       float64 `json:"interval"`
		MaxFrames      int     `json:"max_frames"`
		Transcribe     bool    `json:"transcribe"`
		Language       string  `json:"language"`
		SceneThreshold float64 `json:"scene_threshold"`
		Parallel       int     `json:"parallel"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.VideoPath == "" {
		return "", fmt.Errorf("video_path is required")
	}
	if p.Mode == "" {
		p.Mode = "local"
	}
	if p.Interval <= 0 {
		p.Interval = 10
	}
	if p.MaxFrames <= 0 {
		p.MaxFrames = 20
	}
	if p.SceneThreshold <= 0 {
		p.SceneThreshold = 0.3
	}
	if p.Parallel <= 0 {
		p.Parallel = 5
	}
	if p.Provider == "" {
		p.Provider = "openai"
	}

	// Setup output directory
	if p.OutputDir == "" {
		base := filepath.Base(p.VideoPath)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		p.OutputDir = fmt.Sprintf("./video-analysis-%s-%s", name, time.Now().Format("20060102-150405"))
	}
	framesDir := filepath.Join(p.OutputDir, "frames")
	analysisDir := filepath.Join(p.OutputDir, "frame-analysis")
	for _, d := range []string{p.OutputDir, framesDir, analysisDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Step 1: Metadata
	meta, err := probeVideo(p.VideoPath)
	if err != nil {
		return "", fmt.Errorf("metadata: %w", err)
	}

	report := &AnalysisReport{
		Version:    "1.0.0",
		AnalyzedAt: time.Now().UTC().Format(time.RFC3339),
		Mode:       p.Mode,
		Video:      meta,
	}

	// Step 2: Transcription (if requested)
	if p.Transcribe && meta.Audio != nil {
		tr := TranscribeResult{}
		if t, err := tryTranscribe(ctx, p.VideoPath, p.Language); err == nil && t != nil {
			tr = *t
		}
		if tr.Text != "" {
			report.Transcript = &tr
		}
	}

	// Step 3: Local perception (always run — it's fast and informative)
	perceiveResult, err := runPerception(ctx, p.VideoPath, p.SceneThreshold, 50)
	if err == nil {
		raw := json.RawMessage(perceiveResult)
		report.Perception = &raw
	}

	// Step 4: Extract frames
	extractResult, err := runExtractFrames(ctx, p.VideoPath, framesDir, p.Interval, p.MaxFrames)
	if err != nil {
		return "", fmt.Errorf("frame extraction: %w", err)
	}

	var extractData struct {
		Frames []FrameInfo `json:"frames"`
	}
	json.Unmarshal([]byte(extractResult), &extractData)

	// Step 5: Vision analysis (if vision mode)
	if p.Mode == "vision" {
		type job struct {
			frame FrameInfo
			path  string
		}
		var jobs []job
		for _, f := range extractData.Frames {
			cacheFile := filepath.Join(analysisDir, strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))+".txt")
			if cached, err := os.ReadFile(cacheFile); err == nil && len(cached) > 0 {
				report.Frames = append(report.Frames, FrameAnalysis{
					Index: f.Index, Timestamp: f.TimeFmt, Path: f.Path, Vision: string(cached),
				})
				continue
			}
			jobs = append(jobs, job{frame: f, path: cacheFile})
		}

		// Process sequentially for simplicity (parallel would need goroutine management)
		for _, j := range jobs {
			result, err := runVision(ctx, j.frame.Path, p.Provider, p.Model, p.MaxFrames)
			if err != nil {
				result = fmt.Sprintf("[AI analysis error: %v]", err)
			}
			os.WriteFile(j.path, []byte(result), 0o644)
			report.Frames = append(report.Frames, FrameAnalysis{
				Index: j.frame.Index, Timestamp: j.frame.TimeFmt, Path: j.frame.Path, Vision: result,
			})
		}

		// Sort frames by index
		sort.Slice(report.Frames, func(i, j int) bool {
			return report.Frames[i].Index < report.Frames[j].Index
		})
	} else {
		// Local mode: just record frame info
		for _, f := range extractData.Frames {
			report.Frames = append(report.Frames, FrameAnalysis{
				Index: f.Index, Timestamp: f.TimeFmt, Path: f.Path,
			})
		}
	}

	// Step 6: Generate report
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}

	reportPath := filepath.Join(p.OutputDir, "report.json")
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}

	// Also generate a Markdown report
	mdReport := generateMarkdownReport(report)
	mdPath := filepath.Join(p.OutputDir, "report.md")
	os.WriteFile(mdPath, []byte(mdReport), 0o644)

	return fmt.Sprintf("Analysis complete.\nReport: %s\nMarkdown: %s\nFrames: %s (%d frames)\nOutput: %s",
		reportPath, mdPath, framesDir, len(report.Frames), p.OutputDir), nil
}

// ─── Sub-command helpers (reuse logic from individual tools) ────────

func runPerception(ctx context.Context, videoPath string, threshold float64, maxSeg int) (string, error) {
	p := videoPerceive{}
	args, _ := json.Marshal(map[string]interface{}{
		"video_path":       videoPath,
		"scene_threshold":  threshold,
		"max_segments":     maxSeg,
		"enable_ocr":       true,
		"enable_face_detect": true,
	})
	return p.Execute(ctx, args)
}

func runExtractFrames(ctx context.Context, videoPath, outputDir string, interval float64, maxFrames int) (string, error) {
	e := videoExtractFrames{}
	args, _ := json.Marshal(map[string]interface{}{
		"video_path": videoPath,
		"output_dir": outputDir,
		"interval":   interval,
		"max_frames": maxFrames,
	})
	return e.Execute(ctx, args)
}

func runVision(ctx context.Context, framePath, provider, model string, maxFrames int) (string, error) {
	v := videoVision{}
	args, _ := json.Marshal(map[string]interface{}{
		"frame_path": framePath,
		"provider":   provider,
		"model":      model,
		"max_tokens": 500,
	})
	result, err := v.Execute(ctx, args)
	if err != nil {
		return "", err
	}
	// Extract just the analysis text from the JSON result
	var vr VisionResult
	if err := json.Unmarshal([]byte(result), &vr); err != nil {
		return result, nil // return raw result if we can't parse
	}
	return vr.Analysis, nil
}

func tryTranscribe(ctx context.Context, videoPath, language string) (*TranscribeResult, error) {
	tmpDir, err := os.MkdirTemp("", "reasonix-transcribe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	wavPath := filepath.Join(tmpDir, "audio.wav")
	if !extractAudio(ctx, videoPath, wavPath) {
		return nil, fmt.Errorf("no audio")
	}

	if r := tryWhisperCPP(ctx, wavPath, language); r != nil {
		return r, nil
	}
	return tryOpenAIWhisper(ctx, wavPath, language), nil
}

func generateMarkdownReport(report *AnalysisReport) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Video Analysis Report: %s\n\n", report.Video.File))
	sb.WriteString(fmt.Sprintf("> Analyzed: %s | Mode: %s\n\n", report.AnalyzedAt, report.Mode))

	sb.WriteString("## Video Info\n\n")
	sb.WriteString("| Property | Value |\n|----------|-------|\n")
	sb.WriteString(fmt.Sprintf("| Duration | %s (%.1fs) |\n", report.Video.DurationFmt, report.Video.DurationSec))
	if report.Video.Video != nil {
		sb.WriteString(fmt.Sprintf("| Resolution | %s |\n", report.Video.Video.Resolution))
		sb.WriteString(fmt.Sprintf("| Codec | %s @ %.2f fps |\n", report.Video.Video.Codec, report.Video.Video.FPS))
	}
	if report.Video.Audio != nil {
		sb.WriteString(fmt.Sprintf("| Audio | %s, %d ch, %d Hz |\n", report.Video.Audio.Codec, report.Video.Audio.Channels, report.Video.Audio.SampleRate))
	}
	sb.WriteString("\n")

	if report.Transcript != nil && report.Transcript.Text != "" {
		sb.WriteString("## Transcript\n\n")
		sb.WriteString(report.Transcript.Text)
		sb.WriteString("\n\n")
	}

	if report.Perception != nil {
		sb.WriteString("## Scene Perception\n\n")
		var perception struct {
			Segments []PerceiveSegment `json:"segments"`
		}
		if err := json.Unmarshal(*report.Perception, &perception); err == nil {
			for _, seg := range perception.Segments {
				sb.WriteString(fmt.Sprintf("### Scene %d — %s → %s\n\n", seg.Index+1, seg.StartFmt, seg.EndFmt))
				sb.WriteString(fmt.Sprintf("%s\n\n", seg.Description))
				sb.WriteString(fmt.Sprintf("> Brightness: %.2f | Motion: %.2f | Faces: %d | Colors: %s\n\n",
					seg.Scene.Brightness, seg.Scene.Motion, seg.Scene.FacesDetected,
					strings.Join(seg.Scene.DominantColors, ", ")))
			}
		}
	}

	if len(report.Frames) > 0 {
		sb.WriteString("## Frame Analysis\n\n")
		for _, f := range report.Frames {
			sb.WriteString(fmt.Sprintf("### Frame %d — %s\n\n", f.Index+1, f.Timestamp))
			if f.Vision != "" {
				sb.WriteString(f.Vision)
				sb.WriteString("\n\n")
			}
		}
	}

	return sb.String()
}
