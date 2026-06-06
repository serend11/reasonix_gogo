# DECISIONS.md — 架构决策记录

> Reasonix Fork 项目

---

## [2026-06-10] [Reasonix] 视频分析以内置工具实现，非技能

**背景**: video-ai-analyzer 可以做成 skill（Markdown 剧本）或内置工具（Go 编译时注册）。

**决策**: 作为内置工具（`tool.RegisterBuiltin()`）实现，不走 skill 路径。

**理由**:
- 工具可被 agent 直接调用，不需要用户手动 invoke
- 与 bash / read_file 等同级，语义清晰
- 编译时静态注册，性能最优

**后果**: 增加编译产物大小约 50KB；需要 Go 编译环境。

---

## [2026-06-10] [Reasonix] Provider 层不承载视觉请求

**背景**: `video_vision` 工具需要直接调 AI vision API。可以考虑在 `provider.Stream()` 中支持 image payload，或让工具直接调 HTTP API。

**决策**: `video_vision` 直接调 HTTP API（使用 `netclient`），不经过 provider 抽象层。

**理由**:
- provider 接口设计为 chat completion（文本流），vision 是单次 request/response
- 改动 provider 接口需要更新 OpenAI/Anthropic/Google 三个实现
- 视频分析是低频场景，不值得为此重构核心抽象

**后果**: `video_vision` 与 provider 层有部分代码重复（base URL 选择逻辑）。如果后续 vision 需求增多，可重新考虑。

---

## [2026-06-10] [Reasonix] 本地感知（local mode）作为默认

**背景**: video-ai-analyzer v3-alpha 的 `--local` 模式提供零 API 成本的场景感知（场景检测/色彩/运动/OCR），`--vision` 模式需要 API 调用。

**决策**: `video_analyze` 默认 `mode=local`，`mode=vision` 按需启用。

**理由**:
- 对齐 Reasonix 的成本优先哲学（DeepSeek 缓存命中 90%+）
- 本地模式在无 API 密钥时也能工作
- 场景感知 JSON 可被任何 AI agent 消费，不绑定特定模型

**后果**: 用户需要显式传 `mode=vision` 才能用 AI 分析帧。

---

## [2026-06-10] [Reasonix] ImagePart 放在 Message 上而非 Request 上

**背景**: 视觉支持需要在某处传递 base64 图片。选项：(a) `Message.Images`，(b) `Request.Images`，(c) 独立的 vision API。

**决策**: 放在 `Message.Images []ImagePart` 上，只有 RoleUser 消息携带。

**理由**:
- 与 OpenAI/Anthropic 的 content array 语义对齐（图片是用户消息的一部分）
- 未来多图像场景天然支持（`Images` 是 slice）
- 不 pollute `Request` 的顶层结构
