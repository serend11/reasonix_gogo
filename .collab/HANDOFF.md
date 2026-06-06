# HANDOFF.md — 任务交接

> 最后更新: 2026-06-10

---

## 当前交接

| 字段 | 值 |
|------|-----|
| **当前 Owner** | WorkBuddy |
| **状态** | ⏳ 等待接手 |
| **上一位** | Reasonix |
| **开始时间** | 2026-06-10 |

---

## Reasonix 已完成的工作

1. **Google Gemini provider** (`internal/provider/google/google.go`)
   - 完整的 `provider.Provider` 接口实现
   - 支持 SSE 流式响应、vision (inlineData)、tool calling (functionCall/functionResponse)
   - Usage 统计（含缓存命中）

2. **8 个视频分析内置工具** (`internal/tool/builtin/video_*.go`)
   - `video_metadata` — ffprobe 元数据提取
   - `video_extract_frames` — ffmpeg 按间隔提取帧
   - `video_perceive` — 本地场景感知（场景检测/色彩/亮度/运动/OCR/人脸/自然语言描述合成）
   - `video_ocr` — tesseract OCR
   - `video_face_detect` — ffmpeg facedetect
   - `video_transcribe` — whisper.cpp → OpenAI API 二级 fallback
   - `video_vision` — 多模型视觉分析（OpenAI/Anthropic/Google/Ollama）
   - `video_analyze` — 一键 pipeline 编排器

3. **Provider 层改动**
   - `provider.go`: 新增 `ImagePart` 类型和 `Message.Images` 字段
   - `main.go`: 注册 Google provider

---

## 需要 WorkBuddy 做的事

### 优先级 1: 编译验证（阻塞项）

```bash
git clone --branch main-v2 https://github.com/esengine/DeepSeek-Reasonix.git
cd DeepSeek-Reasonix
# 将 /tmp/reasonix-fork 中所有改动复制过来（或直接用那个目录）
go build ./... 2>&1 | head -50
```

修复任何编译错误。已知风险点：
- `video_analyze.go` 和 `video_vision.go` 的 import 可能需要调整
- `netclient` 包引用路径是否正确
- Go 1.26 工具链需要下载

### 优先级 2: 端到端测试

```bash
# 测试视频元数据
./bin/reasonix run "use video_metadata on test.mp4"

# 测试一键分析
# 先准备一个 30 秒测试视频
```

### 优先级 3: 移到永久目录

```bash
cp -r /tmp/reasonix-fork ~/projects/reasonix-fork
cd ~/projects/reasonix-fork
git remote rename origin upstream
git remote add origin <你的私有仓库>
```

---

## 给 WorkBuddy 的提示

- 所有新工具遵循 Reasonix 的 `tool.RegisterBuiltin()` 注册模式
- `video_vision.go` 直接调 HTTP API（不经过 agent 循环），避免工作区污染
- 所有视频工具在依赖不可用时优雅降级（不 crash）
- Provider 测试模式参考 `internal/provider/openai/openai_test.go`
