package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoExtractFrames{}) }

type videoExtractFrames struct{}

func (videoExtractFrames) Name() string        { return "video_extract_frames" }
func (videoExtractFrames) ReadOnly() bool      { return false }
func (videoExtractFrames) Description() string { return "Extract key frames from a video at regular intervals using ffmpeg. Returns list of frame paths with timestamps." }

func (videoExtractFrames) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"video_path":{"type":"string","description":"Path to the video file"},"output_dir":{"type":"string","description":"Directory to save extracted frames (created if needed)"},"interval":{"type":"number","description":"Seconds between frames (default: 10)","default":10},"max_frames":{"type":"integer","description":"Maximum number of frames to extract (default: 20)","default":20}},"required":["video_path","output_dir"]}`)
}

type FrameInfo struct {
	Index     int     `json:"index"`
	Path      string  `json:"path"`
	Timestamp float64 `json:"timestamp_sec"`
	TimeFmt   string  `json:"timestamp_fmt"`
}

type ExtractFramesResult struct {
	Frames      []FrameInfo `json:"frames"`
	Count       int         `json:"count"`
	OutputDir   string      `json:"output_dir"`
	IntervalSec float64     `json:"interval_sec"`
}

func (videoExtractFrames) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		VideoPath string  `json:"video_path"`
		OutputDir string  `json:"output_dir"`
		Interval  float64 `json:"interval"`
		MaxFrames int     `json:"max_frames"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.VideoPath == "" || p.OutputDir == "" {
		return "", fmt.Errorf("video_path and output_dir are required")
	}
	if p.Interval <= 0 {
		p.Interval = 10
	}
	if p.MaxFrames <= 0 {
		p.MaxFrames = 20
	}

	if err := os.MkdirAll(p.OutputDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", p.OutputDir, err)
	}

	meta, err := probeVideo(p.VideoPath)
	if err != nil {
		return "", err
	}
	duration := meta.DurationSec
	if duration <= 0 {
		return "", fmt.Errorf("could not determine video duration")
	}

	frameCount := int(duration / p.Interval)
	if frameCount > p.MaxFrames {
		frameCount = p.MaxFrames
		p.Interval = duration / float64(frameCount)
	}
	if frameCount < 1 {
		frameCount = 1
	}

	var frames []FrameInfo
	for i := 0; i < frameCount; i++ {
		sec := float64(i) * p.Interval
		if sec >= duration {
			sec = duration - 1
		}
		tf := fmt.Sprintf("%02d:%02d:%02d",
			int(sec)/3600,
			(int(sec)%3600)/60,
			int(sec)%60,
		)
		fname := fmt.Sprintf("frame_%04d_%02d%02d%02d.jpg",
			i,
			int(sec)/3600, (int(sec)%3600)/60, int(sec)%60,
		)
		fpath := filepath.Join(p.OutputDir, fname)

		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-y", "-ss", tf,
			"-i", p.VideoPath,
			"-frames:v", "1", "-q:v", "3",
			fpath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Log but continue with remaining frames
			_ = out
			continue
		}

		if _, err := os.Stat(fpath); err == nil {
			frames = append(frames, FrameInfo{
				Index:     i,
				Path:      fpath,
				Timestamp: sec,
				TimeFmt:   tf,
			})
		}
	}

	result := ExtractFramesResult{
		Frames:      frames,
		Count:       len(frames),
		OutputDir:   p.OutputDir,
		IntervalSec: roundTo(p.Interval, 2),
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}

func roundTo(v float64, decimals int) float64 {
	format := fmt.Sprintf("%%.%df", decimals)
	s := fmt.Sprintf(format, v)
	r, _ := strconv.ParseFloat(s, 64)
	return r
}
