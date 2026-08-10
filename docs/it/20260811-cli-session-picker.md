# CLI 交互式会话选择器

本轮让交互式恢复与分叉在未提供 source selector 时对齐主流 Code Agent 的 picker 默认行为：

```sh
eino resume
eino resume --last
eino fork
eino fork --last "从另一种方案继续"
```

## 行为契约

- `eino resume` 无参数时显示最近更新的活跃 session 并要求输入序号；显式 ID 或完整 display name 保持原有直接选择语义。
- `eino resume --last` 明确选择最新活跃 session 且不显示 picker。它不能与 ID 或 display name 组合，避免选择器含义不明确。
- `eino fork` 无参数时也显示同一 picker；被选 session 是 source，随后正常创建独立 child。`eino fork --last [prompt]` 继续保留无 picker 的明确最近会话路径。
- picker 只读取 journal-derived active metadata，不修复 SQLite projection、不启动模型、不打开 TUI、不创建 fork，也不恢复 active turn。archived session 不会显示。
- picker 显示最多 30 条最近 session（ID、标题、消息数、更新时间）；未显示的旧条目仍能用显式 ID 或完整 display name 选择。
- 输入 `q` / `quit`、空输入、EOF 或 `Esc`（后接回车）会取消；取消和空列表均不会隐式恢复或 fork 最近 session。非法序号会在同一 picker 中提示并重试。

## 参考与取舍

- 已观察的 Codex CLI 0.146.0 将 `resume` 与 `fork` 标记为 picker by default，`--last` 则跳过 picker；本轮沿用这一主路径和明确的最近会话快捷入口。
- 已观察的 Claude Code 2.1.220 将 `--resume` 无参数定义为 interactive picker，而 `--continue` 是明确的最近会话路径；本轮同样不把取消等同于“最近”。
- OpenCode CLI 将 `--continue`、`--session` 与 `--fork` 分别建模。本轮保留脚本可用的直接 ID/display-name 路径，picker 仅用于交互 TTY。

调研依据详见 `docs/research/interactive-session-picker-research.md`。

## 验证

- picker 单元测试覆盖稳定序号选择、标题清理、非法输入重试、`q`/EOF 取消、30 条可见上限、active-only 列表和最新选择。
- CLI 测试覆盖无参数 `resume` / `fork` 调用 picker、显式 selector 不调用 picker、`resume --last` 直接选择，以及 `resume ID --last` 拒绝。
- 本轮提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 已知边界

这是终端行式的最小 picker，暂不支持 fuzzy search、跨目录范围过滤、归档列表、鼠标或快捷键导航。非 TTY 环境仍要求显式 session selector（或 interactive `--last`），不会读取管道输入来选择持久会话。
