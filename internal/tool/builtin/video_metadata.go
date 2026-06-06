package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoMetadata{}) }

type videoMetadata struct{}

func (videoMetadata) Name() string        { return "video_metadata" }
func (videoMetadata) ReadOnly() bool      { return true }
func (videoMetadata) Description() string { return "Extract metadata from a video file (duration, resolution, codec, fps, bitrate, audio tracks) using ffprobe. No API required — runs locally." }

func (videoMetadata) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to the video file"}},"required":["path"]}`)
}

func (videoMetadata) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	meta, err := probeVideo(p.Path)
	if err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(meta, "", "  ")
	return string(b), nil
}

// VideoMeta holds extracted video metadata.
type VideoMeta struct {
	File        string       `json:"file"`
	Path        string       `json:"path"`
	DurationSec float64      `json:"duration_sec"`
	DurationFmt string       `json:"duration_fmt"`
	SizeMB      float64      `json:"size_mb"`
	Video       *VideoStream `json:"video,omitempty"`
	Audio       *AudioStream `json:"audio,omitempty"`
}

type VideoStream struct {
	Codec      string  `json:"codec"`
	Resolution string  `json:"resolution"`
	FPS        float64 `json:"fps"`
	Bitrate    string  `json:"bitrate,omitempty"`
}

type AudioStream struct {
	Codec      string `json:"codec"`
	Channels   int    `json:"channels"`
	SampleRate int    `json:"sample_rate"`
}

func probeVideo(path string) (*VideoMeta, error) {
	// ffprobe JSON output
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w (is ffmpeg installed?)", err)
	}

	var data struct {
		Format struct {
			Filename string `json:"filename"`
			Duration string `json:"duration"`
			Size     string `json:"size"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			Channels   int    `json:"channels"`
			SampleRate string `json:"sample_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	dur, _ := strconv.ParseFloat(data.Format.Duration, 64)
	size, _ := strconv.ParseInt(data.Format.Size, 10, 64)
	durSec := int(dur)
	durFmt := fmt.Sprintf("%02d:%02d:%02d", durSec/3600, (durSec%3600)/60, durSec%60)

	meta := &VideoMeta{
		File:        data.Format.Filename,
		Path:        path,
		DurationSec: dur,
		DurationFmt: durFmt,
		SizeMB:      float64(size) / (1024 * 1024),
	}

	bitrate := data.Format.BitRate
	if bitrate != "" {
		if br, err := strconv.Atoi(bitrate); err == nil && br > 0 {
			bitrate = fmt.Sprintf("%d kbps", br/1000)
		}
	}

	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			if meta.Video == nil {
				fps := 0.0
				if parts := strings.Split(s.RFrameRate, "/"); len(parts) == 2 {
					num, _ := strconv.ParseFloat(parts[0], 64)
					den, _ := strconv.ParseFloat(parts[1], 64)
					if den > 0 {
						fps = num / den
					}
				}
				meta.Video = &VideoStream{
					Codec:      s.CodecName,
					Resolution: fmt.Sprintf("%dx%d", s.Width, s.Height),
					FPS:        fps,
					Bitrate:    bitrate,
				}
			}
		case "audio":
			if meta.Audio == nil {
				sr, _ := strconv.Atoi(s.SampleRate)
				meta.Audio = &AudioStream{
					Codec:      s.CodecName,
					Channels:   s.Channels,
					SampleRate: sr,
				}
			}
		}
	}
	return meta, nil
}
