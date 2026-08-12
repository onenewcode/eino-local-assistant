# TUI context status unknown label

## 背景

`docs/research/context-window-status-display-research.md` 对 Codex、Gemini CLI 和 Claude Code 的公开行为进行了对照：常驻状态栏应表达完整 context window 的已用/剩余快照；在没有 provider usage 或本地估算时，应明确标记未知，而不是制造精确百分比。

## 变更

- 将 TUI 状态栏在没有上下文快照或本地估算时的 `Context 0% used` 改为 `Context unknown`。
- 保留有 provider 快照时的已用比例，以及仅有本地估算时的估算标签。
- 增加未知状态的单元测试，并更新 `/statusline` picker 的视图断言。

## 验证

- `go test ./internal/tui`
- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## 对齐说明

该小步对齐研究中“未知容量/usage 不应冒充 0%”的边界；它不改变 session ledger 或模型请求，仅修正用户可见的状态语义。
