# TUI Explicit Latest-Session Resume

本轮在 TUI 的 `/resume` 选择器与直接 ID 恢复之间，补齐不弹出选择器的明确最近会话路径：

```text
/resume --last
/resume --last --recover
```

## 行为契约

- `/resume --last` 从现有活跃 session 列表的第一项（最近更新）选择目标，不打开 picker，也不按标题或搜索词做二次推断。
- `/resume --last --recover` 将显式恢复授权传入与直接 ID 恢复相同的会话打开路径；`--recover` 仍不能单独使用。
- `--last` 与 ID 互斥，且只接受可选的精确尾随 `--recover`。错误组合在读取 session metadata 之前失败。
- 若最近 session 已经是当前会话，命令记录 `latest active session is already open` 并保持当前的 transcript chrome、输入历史与 composer draft，不会为了“恢复”而重置本地状态。
- 列表读取失败、空列表或归档/空 ID 条目不会切换当前 session。归档 session 保持不在候选范围内。
- 真正选中其他 session 时，继续调用既有 `resumeSession`：runtime `OpenSession` 回调、模型/effort snapshot、更换通知、规则快照失效和 transcript replay 均无分叉实现。

## 参考与取舍

- 已观察的 Codex CLI 0.146.0 将 `resume` 说明为 picker by default，并以 `--last` 作为跳过 picker 的明确最近会话动作。
- 已观察的 Claude Code 2.1.220 将可交互 resume 与当前目录中的 explicit continue 作为不同选择方式。
- OpenCode 的 CLI 同样将具体 `--session` 与最近会话的 `--continue` 区分。因此本轮不把取消 picker 或遗漏 selector 等同于“最近”，只用文字明确的 `--last` 触发该行为。

调研依据复用本轮前序的 `docs/research/tui-session-resume-picker-research.md`；其中记录了上述产品的观察时间与证据边界。

## 验证

- TUI 单元测试覆盖 `--last` 的普通与 recovery 路径、最新候选的列表顺序、无 picker 保证、当前会话 no-op、严格解析和列表读取失败。
- 回归测试覆盖已有直接 ID resume、picker、side-question session replacement 及恢复参数的历史契约。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 已知边界

`--last` 只使用当前 repository 的 active 列表，不增加跨目录筛选、归档恢复或模糊“最近”推断。TUI 已在一个 session 中运行时，最近项恰为当前会话会安全 no-op；这与新进程启动时必须打开该会话的 shell CLI 情境不同。
