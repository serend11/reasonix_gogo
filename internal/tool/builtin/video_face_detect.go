package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"

	"reasonix/internal/tool"
)

func init() { tool.RegisterBuiltin(videoFaceDetect{}) }

type videoFaceDetect struct{}

func (videoFaceDetect) Name() string        { return "video_face_detect" }
func (videoFaceDetect) ReadOnly() bool      { return true }
func (videoFaceDetect) Description() string { return "Detect and count faces in a video frame using ffmpeg's facedetect filter. Returns face count and coordinates." }

func (videoFaceDetect) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"frame_path":{"type":"string","description":"Path to the frame image"}},"required":["frame_path"]}`)
}

func (videoFaceDetect) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		FramePath string `json:"frame_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.FramePath == "" {
		return "", fmt.Errorf("frame_path is required")
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", p.FramePath,
		"-vf", "facedetect",
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run()

	type faceCoord struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	}
	var faces []faceCoord
	count := 0

	output := stderr.String()
	// Parse "X Y W H" face coordinates
	coordRe := regexp.MustCompile(`(\d+)\s+(\d+)\s+(\d+)\s+(\d+)`)
	// Find the count from "N faces detected"
	countRe := regexp.MustCompile(`(\d+)\s*faces?\s*detected`)

	// Collect coordinates between "faces detected" markers
	lines := regexp.MustCompile(`\n`).Split(output, -1)
	for _, line := range lines {
		if m := coordRe.FindStringSubmatch(line); m != nil {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			w, _ := strconv.Atoi(m[3])
			h, _ := strconv.Atoi(m[4])
			faces = append(faces, faceCoord{X: x, Y: y, W: w, H: h})
		}
		if m := countRe.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > count {
				count = n
			}
		}
	}

	result := map[string]interface{}{
		"face_count":   count,
		"faces":        faces,
		"any_detected": count > 0,
	}
	b, _ := json.MarshalIndent(result, "", "  ")
	return string(b), nil
}
