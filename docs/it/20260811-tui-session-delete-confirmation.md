# TUI confirmed session deletion

本轮将 TUI permanent-delete lifecycle 与 shell CLI 及主流 agent 的安全边界对齐：

```text
/delete <session-id-or-name> --yes
/delete <session-id-or-name> --force
```

## 行为契约

- `/delete` 不再将 ID 或名称本身视为不可恢复删除的授权；缺少末尾 `--yes` 或 `--force` 时只提示确认方式，不解析、删除或切换 session。
- selector 保持 stable ID 优先、大小写敏感的完整名称匹配和同名歧义报错。删除可访问 active 与 archived 的名称范围，便于直接移除已经归档的工作；不提供隐式 `--last`。
- current TUI session 即使以名称选中也会被拒绝。最终删除仍调用 store 的 writer-locked lifecycle primitive，因此另一进程的 active turn 或 pending compaction 不会因 UI 的 idle 状态而被越过。
- confirmation token 是仅限末尾的控制参数，名称主体可包含空格。`--force` 保持仓库现有 shell CLI 的确认别名；脚本和精确故障恢复仍应优先使用 stable ID。

## 参考与取舍

本机重新核验的 Codex CLI `0.146.0` 将 `delete <SESSION>` 描述为 ID/name selector（UUID 优先），并以 `--force` 区分无提示的确认路径。shell 产品已有 `--yes`/`--force` 明确确认模型；本轮在 TUI 采用这一已存在的语义，而不是继续保留“输入一次 slash command 就永久删除”的不对称边界。Codex 对其 `--force` 额外要求 UUID；Eino 的 `--force` 继续保持自身 shell CLI 的等价确认语义，以免 TUI 与已发布的 headless contract 分叉。

调研依据见 `docs/research/session-deletion-research.md` 与 `docs/research/session-name-selection-research.md`，两份本机产品观察均已在 2026-08-11 重核。

## 验证

- TUI 测试覆盖缺少 confirmation 时状态和草稿不变、按 active/archived display name 删除、`--force`、当前 session 名称拒绝及参数解析。
- 既有删除测试继续覆盖 stable ID、current-session 拒绝与 durable removal；共享 resolver 的既有测试覆盖 ID precedence、exact match、重复名称和 scope。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
