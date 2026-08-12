# TUI `/context` 显示 pending compaction

日期：2026-08-12

## 背景

`/context` 已明确区分 provider 快照（`context=...`）、本地规划估算
（`planned_view` / `current_request_estimate`）和未知值；但压缩事务开始后，
用户看不到 durable pending operation。压缩调用可能正在等待 provider、被中断或
需要显式恢复，缺少 operation 标识会让状态无法和账本对照。

## 实现

- `chat.ContextStatus` 暴露只读的 `PendingCompaction` 副本（operation ID、是否自动、开始时间由 durable store 维护）。
- `/context` 在存在 pending operation 时输出 `pending_compaction=<id> automatic=<bool>`。
- 不改变 unknown/exact/estimate 的既有标签，也不把 pending 状态伪装成 provider context usage；完成或失败事件清除该字段。

该状态边界参考 Codex 的 context remaining 快照与 Claude Code status-line 的可见上下文生命周期：用户可见状态应标明测量来源和未完成操作，而不是猜测 provider 结果。详见 `docs/research/context-window-status-display-research.md`。

## 验证

- 新增 TUI 测试：压缩 provider 调用阻塞期间，`/context` 显示实际 pending operation ID 和 `automatic=false`。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
