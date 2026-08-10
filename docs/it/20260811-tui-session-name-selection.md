# TUI Resume by Session Name

本轮让 TUI `/resume` 的显式 selector 与已支持的 shell CLI 保持同一人机语义：

```text
/resume SESSION_ID
/resume Exact display name
/resume Exact display name --recover
```

## 行为契约

- `/resume` 保留无参数 picker，`/resume --last` 保留最近会话快路径；其余非控制参数被当作一个 ID 或完整 display name，而非拆成多个不相关参数。
- resolver 先按 ID 查询；即使另一个 active session 的标题相同，稳定 ID 仍优先。这保证脚本和粘贴的故障诊断 ID 不会被标题改名改变目标。
- 未命中 ID 时，只在 active session 中进行标题的大小写敏感、完整字符串匹配。带空格的标题可直接使用，`--recover` 只允许作为精确尾随控制参数。
- 同名不猜测：命令保持当前 session 并显示所有匹配 ID；不存在或已归档的名称同样不改变当前 session。归档 ID 仍交由既有 session-open 边界报告其 archive lifecycle 状态。
- 名称解析成功后，TUI 将 canonical ID 传给既有 `resumeSession`，因此 runtime-owned model binding、恢复授权、规则快照失效、active-session 通知与 transcript replay 保持单一路径。

## 参考与取舍

- Codex CLI 0.146.0 的已观察帮助将 session UUID 和名称作为 resume selector，并明确 UUID 优先。
- Claude Code 2.1.220 将持久 display name 展示在 `/resume` picker 等恢复入口中。
- 两者共同支持“人类名称 + 稳定 ID”的模型。本轮采用精确匹配和歧义报错，不采用 substring、大小写折叠或 fuzzy 选择，避免状态切换落到错误 session。

调研依据详见 `docs/research/session-name-selection-research.md`。

## 验证

- 新增 TUI 测试覆盖带空格的精确名称恢复、ID 优先、重复名称的匹配 ID 提示、归档名称排除，以及与 `--recover`/`--last` 共存的严格解析。
- 既有恢复测试继续覆盖恢复授权、side-question 切换、runtime snapshot 与 picker 行为。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 已知边界

本轮不改变标题本身的自由命名规则，也不要求其唯一。名称是交互便利层，不适合作为自动化的长期机器标识；自动化仍应使用 session ID。归档名称需要先显式 unarchive，且未提供跨 scope 的隐式恢复。
