# ROLES.md — Reasonix Fork 项目角色

> 创建: 2026-06-10 | 版本: v1.0

---

## 项目

**Reasonix Fork** — 基于 esengine/DeepSeek-Reasonix (MIT) 的深度定制版本
- 融合 video-ai-analyzer v3-alpha 视频分析能力为内置工具
- 补齐多模型 API 支持（Google Gemini / Anthropic Claude / OpenAI / Ollama）
- 源码位置: `/tmp/reasonix-fork`（建议移到永久目录如 `~/projects/reasonix-fork`）

---

## 团队

### Trea — 项目统筹
- 分配任务、把控方向

### Reasonix — 架构设计 & 代码审查
- 已完成: 9 个新文件 + 3 个修改文件的初始实现
- 擅长: 架构决策、provider 层设计、工具接口抽象

### WorkBuddy — 实现 & 调试
- 擅长: Go 编译调试、测试、CI/CD、功能补全
- 待接手: 编译验证、端到端测试、bug 修复

---

## 当前分工

| 角色 | 负责 |
|------|------|
| Reasonix | provider 层、工具架构、技能系统 |
| WorkBuddy | 编译通过、测试、CI 适配 |
