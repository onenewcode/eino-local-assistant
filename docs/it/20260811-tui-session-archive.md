# TUI reversible session archive

本轮将已有的 durable session archive lifecycle 接入 TUI，提供与 shell CLI 对称的交互入口：

```text
/archive <session-id-or-name>
/unarchive <session-id-or-name>
/sessions --archived
```

## 行为契约

- `/archive` 仅接受 inactive 的 active session；当前 TUI session 必须先通过 `/new` 或 `/resume` 切换，因此不会将仍可能接收下一轮输入的 session 隐藏。
- `/unarchive` 只按 archived-session 名称解析。两条命令都保持稳定 ID 优先、完整 display name 匹配、同名报错的 selector 语义；不会猜测目标。
- archive 和 unarchive 均读取当前 durable revision 后交给 store 的 CAS lifecycle transition。store 仍是 active turn 和 pending compaction 安全边界的唯一权威，失败不会改变当前 session 或编辑框草稿。
- `/sessions` 默认只展示 active sessions，`/sessions --archived` 显式展示归档列表并提示 `/unarchive`；归档操作不删除 transcript、checkpoint、artifact 或 usage 数据。
- busy/compacting 时 archive lifecycle 命令不可排队，避免操作在用户选择目标后延迟落到变化的 session 状态。

## 参考与取舍

本轮采用 Codex CLI 已观察到的 `archive` / `unarchive` 对称生命周期命令模型，并与仓库已有 shell CLI 保持一致。名称是交互便利层，稳定 ID 仍优先于同名标题，便于脚本和故障诊断；对名称保持 exact-match 和歧义报错，以防状态改变错误落到另一条 session。

调研依据详见 `docs/research/session-archive-research.md`，持久化事件与并发安全约束沿用既有 CLI archive 实现（`docs/it/20260810-cli-session-archive.md`）。

## 验证

- 新增 TUI 测试覆盖按名称 archive/unarchive、归档列表、当前 active session 拒绝和另一进程 active turn 的 durable 安全拒绝。
- 既有 slash parser、帮助目录和 busy-input 测试覆盖新命令的发现性与不可排队行为。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
