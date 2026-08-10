# TUI fork parent picker

本轮为 TUI 增加可搜索的 fork parent picker：

```text
/fork --pick
```

## 行为契约

- picker 使用 active saved-session 列表，搜索 display name 或 stable ID；当前 TUI session 也在候选中，因为分叉当前工作是有效且常见的操作。归档 session 不出现。
- `Up`/`Down` 或 `j`/`k` 选择，`Enter` 只从选中的 durable session 创建 child，`Esc` 取消。打开、搜索或取消不改变当前 session、composer draft 或 durable session data。
- confirmation 复用 `/fork <id>` 的唯一执行路径：source 的 durable model/reasoning binding、committed-boundary 检查和 child provenance 与直接 selector 一致。
- 若 source 在 picker 打开后已无 committed turn、变为 active 或其他 durable fork 边界拒绝状态，picker 保持打开并显示错误，用户可以换一个目标或取消。
- `/fork` 无参数仍分叉 current session，`/fork --last` 保持直接的 newest-session 快路径；引入显式 `--pick` 不破坏这两个已建立行为。

## 参考与取舍

Codex CLI `0.146.0` 的 `fork` 在未提供 selector 时默认打开 session picker，并用 `--last` 选择最新 session。Eino TUI 先前已将无参数 `/fork` 定义为分叉 current session；本轮采用明确的 `--pick` 加入同样的可发现搜索能力，避免将一个常用的现有命令静默改成不同动作。

调研依据详见 `docs/research/session-fork-research.md`，其 Codex 与 Claude Code 本机帮助已于 2026-08-11 重核。

## 验证

- 新增 TUI 测试覆盖 current/other active 候选、归档排除、搜索、取消和 draft/viewport 保持。
- 测试覆盖确认后 child 的 selected-parent provenance，以及 durable fork 失败时 picker/当前 session/draft 均保持。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
