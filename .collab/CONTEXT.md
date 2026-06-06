# CONTEXT.md — Reasonix Fork 项目上下文

> 创建: 2026-06-10 | Reasonix

---

## 项目来源

基于 [esengine/DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix) `main-v2` 分支（Go 重写版，MIT 许可证）。目标是将 [video-ai-analyzer](https://github.com/serend11/video-ai-analyzer/tree/v3-alpha) 的视频分析能力融合为**内置工具**（非技能），同时补齐多模型 API 适配。

## 关键架构

```
internal/
├── provider/
│   ├── openai/        # ✅ 已有 — 完整 OpenAI 兼容（含 DeepSeek thinking）
│   ├── anthropic/     # ✅ 已有 — 完整 Anthropic Messages API（含 thinking/vision）
│   └── google/        # 🆕 新增 — Gemini API（流式/vision/tool calling）
├── tool/builtin/
│   ├── video_metadata.go       # 🆕 ffprobe 视频元数据
│   ├── video_extract_frames.go # 🆕 ffmpeg 帧提取
│   ├── video_perceive.go       # 🆕 本地场景感知（零 API）
│   ├── video_ocr.go            # 🆕 tesseract OCR
│   ├── video_face_detect.go    # 🆕 ffmpeg 人脸检测
│   ├── video_transcribe.go     # 🆕 whisper.cpp → OpenAI fallback
│   ├── video_vision.go         # 🆕 多模型视觉分析
│   └── video_analyze.go        # 🆕 一键 pipeline
```

## 修改的文件

| 文件 | 改动 |
|------|------|
| `internal/provider/provider.go` | 新增 `ImagePart` 类型 + `Message.Images` 字段 |
| `cmd/reasonix/main.go` | 添加 `_ "reasonix/internal/provider/google"` 导入 |

## 依赖

- **编译**: Go 1.26.4 (go.mod 要求)
- **运行时**: ffmpeg + ffprobe（视频工具硬依赖）
- **可选**: tesseract（OCR）、whisper-cpp（本地转录）
- **API 密钥**: GOOGLE_API_KEY / ANTHROPIC_API_KEY / OPENAI_API_KEY（按需）

## 当前状态

⚠️ 沙箱环境只有 Go 1.23，无法完整编译。所有文件通过 `gofmt -e` 语法检查，但未做集成编译验证。

## 待办

1. 在本地 Go 1.26 环境 `make build` 验证编译
2. 修复任何编译错误（包引用、import 路径等）
3. 端到端测试：用测试视频跑 `video_analyze`
4. 验证 Gemini / Anthropic provider 的流式响应
5. 将 fork 从 /tmp 移到永久目录并 git remote 指向自己的仓库
