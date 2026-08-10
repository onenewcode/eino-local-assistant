# TUI no alternate screen

本轮为 `chat` 和 `resume` 增加 `--no-alt-screen`，对齐主流 code agent 在终端复用器、录屏和日志采集场景中的 TUI 控制能力。

## 行为

- 默认仍启用 alternate screen，保持现有全屏 TUI 体验。
- `--no-alt-screen` 不传入 Bubble Tea 的 alternate-screen option，TUI 内容保留在普通终端滚屏中。
- 鼠标、上下文取消、会话恢复提示和现有快捷键语义不变。
- flag 只适用于交互式 `chat` / `resume`，不影响非交互 `exec`。

## 验证

已覆盖默认开启、显式关闭和两个 CLI 入口的 flag/help，并运行仓库规定的测试、构建和 lint 门槛。
