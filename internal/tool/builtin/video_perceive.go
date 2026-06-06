package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoPerceive{}) }

type videoPerceive struct{}

func (videoPerceive) Name() string   { return "video_perceive" }
func (videoPerceive) ReadOnly() bool { return true }
func (videoPerceive) Description() string {
	return "Analyze a video locally (no AI API) — detect scenes, measure color/brightness/motion, and produce structured natural-language segment descriptions. Output is a JSON array like Whisper segments that any AI agent can consume. Requires ffmpeg."
}

func (videoPerceive) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"video_path":{"type":"string","description":"Path to the video file"},"scene_threshold":{"type":"number","description":"Scene detection sensitivity 0-1 (default: 0.3)","default":0.3},"max_segments":{"type":"integer","description":"Maximum scene segments (default: 50)","default":50},"enable_ocr":{"type":"boolean","description":"Enable OCR text extraction (needs tesseract)","default":true},"enable_face_detect":{"type":"boolean","description":"Enable face detection via ffmpeg","default":true}},"required":["video_path"]}`)
}

type PerceiveSegment struct {
	Index       int          `json:"index"`
	Start       float64      `json:"start"`
	End         float64      `json:"end"`
	StartFmt    string       `json:"start_fmt"`
	EndFmt      string       `json:"end_fmt"`
	Duration    float64      `json:"duration"`
	Scene       SceneInfo    `json:"scene"`
	OCR         []OCRResult  `json:"ocr,omitempty"`
	Description string       `json:"description"`
}

type SceneInfo struct {
	Brightness     float64  `json:"brightness"`
	DominantColors []string `json:"dominant_colors"`
	Motion         float64  `json:"motion"`
	FacesDetected  int      `json:"faces_detected"`
}

type OCRResult struct {
	Text       string `json:"text"`
	Confidence int    `json:"confidence"`
}

func (videoPerceive) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		VideoPath       string  `json:"video_path"`
		SceneThreshold  float64 `json:"scene_threshold"`
		MaxSegments     int     `json:"max_segments"`
		EnableOCR       bool    `json:"enable_ocr"`
		EnableFaceDetect bool   `json:"enable_face_detect"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.VideoPath == "" {
		return "", fmt.Errorf("video_path is required")
	}
	if p.SceneThreshold <= 0 {
		p.SceneThreshold = 0.3
	}
	if p.MaxSegments <= 0 {
		p.MaxSegments = 50
	}

	meta, err := probeVideo(p.VideoPath)
	if err != nil {
		return "", err
	}
	duration := meta.DurationSec
	if duration <= 0 {
		return "", fmt.Errorf("could not determine video duration")
	}

	// Step 1: Detect scene changes
	scenes := detectScenes(ctx, p.VideoPath, p.SceneThreshold, duration)
	if len(scenes) < 2 {
		scenes = []float64{0, duration}
	}
	if len(scenes) > p.MaxSegments+1 {
		step := len(scenes) / p.MaxSegments
		sampled := []float64{scenes[0]}
		for i := step; i < len(scenes)-1; i += step {
			sampled = append(sampled, scenes[i])
		}
		if sampled[len(sampled)-1] < duration {
			sampled = append(sampled, duration)
		}
		scenes = sampled
	}

	// Step 2: Create temp dir for frame extraction
	tmpDir, err := os.MkdirTemp("", "reasonix-video-perceive-")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Step 3: Process each segment
	total := len(scenes) - 1
	segments := make([]PerceiveSegment, 0, total)
	hasTesseract := hasCmd("tesseract")
	hasFacedetect := hasCmd("ffmpeg") && p.EnableFaceDetect

	for i := 0; i < total; i++ {
		start, end := scenes[i], scenes[i+1]
		mid := (start + end) / 2
		framePath := filepath.Join(tmpDir, fmt.Sprintf("seg_%04d.jpg", i))

		seg := PerceiveSegment{
			Index:    i,
			Start:    roundTo(start, 2),
			End:      roundTo(end, 2),
			StartFmt: secToFmt(start),
			EndFmt:   secToFmt(end),
			Duration: roundTo(end-start, 2),
		}

		if extractFrameAt(ctx, p.VideoPath, mid, framePath) {
			seg.Scene = analyzeScene(ctx, framePath, p.VideoPath, start, end)

			if p.EnableOCR && hasTesseract {
				seg.OCR = runOCR(ctx, framePath)
			}
			if hasFacedetect {
				seg.Scene.FacesDetected = countFaces(ctx, framePath)
			}
		} else {
			seg.Scene = SceneInfo{Brightness: 0.5, Motion: 0}
		}
		seg.Description = synthesizeDesc(seg)
		segments = append(segments, seg)
	}

	result := map[string]interface{}{
		"engine":         "reasonix-video-perceive",
		"version":        "1.0.0",
		"video":          meta,
		"segment_count":  len(segments),
		"segments":       segments,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}

// ─── Scene Detection ──────────────────────────────────────────────────

func detectScenes(ctx context.Context, videoPath string, threshold float64, duration float64) []float64 {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", videoPath,
		"-vf", fmt.Sprintf("select='gt(scene\\,%f)',showinfo", threshold),
		"-vsync", "vfr", "-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run() // ignore errors; we parse what we get

	timestamps := []float64{0.0}
	re := regexp.MustCompile(`pts_time:([\d]+(?:\.[\d]+)?)`)
	for _, m := range re.FindAllStringSubmatch(stderr.String(), -1) {
		if t, err := strconv.ParseFloat(m[1], 64); err == nil {
			if t-timestamps[len(timestamps)-1] >= 0.5 {
				timestamps = append(timestamps, roundTo(t, 2))
			}
		}
	}
	if len(timestamps) > 0 && timestamps[len(timestamps)-1] < duration-1 {
		timestamps = append(timestamps, roundTo(duration, 2))
	}
	return timestamps
}

// ─── Frame Extraction ─────────────────────────────────────────────────

func extractFrameAt(ctx context.Context, videoPath string, ts float64, outPath string) bool {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-ss", fmt.Sprintf("%.3f", ts),
		"-i", videoPath,
		"-frames:v", "1", "-q:v", "3",
		outPath,
	)
	cmd.Run()
	_, err := os.Stat(outPath)
	return err == nil
}

// ─── Scene Analysis (color / brightness / motion) ─────────────────────

func analyzeScene(ctx context.Context, framePath, videoPath string, start, end float64) SceneInfo {
	si := SceneInfo{Brightness: 0.5, Motion: 0.0}

	// Dominant colors via palettegen
	colors := extractDominantColors(ctx, framePath)
	si.DominantColors = colors

	// Brightness from 10x10 thumbnail
	si.Brightness = estimateBrightness(ctx, framePath)

	// Motion from ffmpeg scene scores
	dur := end - start
	if dur < 0.5 {
		dur = 0.5
	}
	if dur > 30 {
		dur = 30
	}
	si.Motion = estimateMotion(ctx, videoPath, start, dur)

	return si
}

func extractDominantColors(ctx context.Context, framePath string) []string {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", framePath,
		"-vf", "palettegen=stats_mode=diff:max_colors=5:reserve_transparent=0",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-frames:v", "1", "-",
	)
	out, _ := cmd.Output()
	colors := make([]string, 0, 5)
	for i := 0; i < len(out)-2 && len(colors) < 5; i += 3 {
		r, g, b := out[i], out[i+1], out[i+2]
		colors = append(colors, fmt.Sprintf("#%02x%02x%02x", r, g, b))
	}
	if len(colors) == 0 {
		return []string{"#808080"}
	}
	return colors
}

func estimateBrightness(ctx context.Context, framePath string) float64 {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", framePath,
		"-vf", "scale=10:10",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-",
	)
	out, _ := cmd.Output()
	if len(out) < 3 {
		return 0.5
	}
	var total float64
	count := 0
	for i := 0; i+2 < len(out); i += 3 {
		r, g, b := float64(out[i]), float64(out[i+1]), float64(out[i+2])
		lum := 0.299*r + 0.587*g + 0.114*b
		total += lum / 255.0
		count++
	}
	if count == 0 {
		return 0.5
	}
	return roundTo(total/float64(count), 3)
}

func estimateMotion(ctx context.Context, videoPath string, start, dur float64) float64 {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-ss", fmt.Sprintf("%.3f", start),
		"-t", fmt.Sprintf("%.1f", dur),
		"-i", videoPath,
		"-vf", "select='gt(scene\\,0.1)',metadata=print:file=-",
		"-an", "-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run()

	re := regexp.MustCompile(`lavfi\.scene_score=([\d.]+)`)
	scores := re.FindAllStringSubmatch(stderr.String(), -1)
	if len(scores) == 0 {
		return 0.0
	}
	var total float64
	for _, m := range scores {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			total += v
		}
	}
	motion := roundTo(total/float64(len(scores)), 3)
	return math.Min(motion, 1.0)
}

// ─── OCR ──────────────────────────────────────────────────────────────

func runOCR(ctx context.Context, framePath string) []OCRResult {
	if !hasCmd("tesseract") {
		return nil
	}
	cmd := exec.CommandContext(ctx, "tesseract", framePath, "stdout",
		"-l", "eng+chi_sim", "--psm", "6", "--oem", "1",
	)
	out, _ := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var results []OCRResult
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) >= 2 && len(results) < 20 {
			results = append(results, OCRResult{Text: line, Confidence: 0})
		}
	}
	return results
}

// ─── Face Detection ───────────────────────────────────────────────────

func countFaces(ctx context.Context, framePath string) int {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", framePath,
		"-vf", "facedetect",
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run()

	re := regexp.MustCompile(`(\d+)\s*faces?\s*detected`)
	matches := re.FindAllStringSubmatch(stderr.String(), -1)
	maxFaces := 0
	for _, m := range matches {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxFaces {
			maxFaces = n
		}
	}
	return maxFaces
}

// ─── Description Synthesis ────────────────────────────────────────────

func synthesizeDesc(seg PerceiveSegment) string {
	var parts []string
	s := seg.Scene

	if s.FacesDetected == 1 {
		parts = append(parts, "1 person face detected")
	} else if s.FacesDetected > 1 {
		parts = append(parts, fmt.Sprintf("%d faces detected", s.FacesDetected))
	}

	switch {
	case s.Brightness > 0.75:
		parts = append(parts, "bright scene")
	case s.Brightness > 0.5:
		parts = append(parts, "moderately lit scene")
	case s.Brightness > 0.25:
		parts = append(parts, "dim scene")
	default:
		parts = append(parts, "very dark scene")
	}

	// Warm / cool tone heuristic
	if len(s.DominantColors) >= 3 {
		warm := 0
		for _, c := range s.DominantColors[:3] {
			r, _ := strconv.ParseInt(c[1:3], 16, 64)
			b, _ := strconv.ParseInt(c[5:7], 16, 64)
			if r > b {
				warm++
			}
		}
		if warm >= 2 {
			parts = append(parts, "warm tones dominate")
		} else if warm == 0 {
			parts = append(parts, "cool tones dominate")
		}
	}

	switch {
	case s.Motion > 0.3:
		parts = append(parts, "significant motion")
	case s.Motion > 0.1:
		parts = append(parts, "subtle movement")
	case s.Motion > 0.02:
		parts = append(parts, "mostly static")
	default:
		parts = append(parts, "completely still")
	}

	if len(seg.OCR) > 0 {
		preview := seg.OCR[0].Text
		if len(preview) > 120 {
			preview = preview[:117] + "..."
		}
		parts = append(parts, fmt.Sprintf(`text visible: "%s"`, preview))
	}

	if len(parts) == 0 {
		return "Scene with no notable features."
	}
	return strings.Join(parts, ". ") + "."
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func secToFmt(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}
