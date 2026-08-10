# Session name selectors

本轮把现有 durable thread title 提升为 shell 可用的 session display name，对齐 Codex CLI 的 “session ID or session name” selector 与 Claude Code 的 `-n/--name`。不迁移磁盘 schema：name 继续由已有 `ThreadMeta.Title` 和 `title.changed` journal event 持久化，因此旧 session 立即可按现有 title 选择。

## 命名入口

- bare invocation、`chat` 与 `fork` 新增 `-n/--name`；它与已有 `--title` 是同一 display-name 语义的两个入口。
- 同时指定 `--name` 和 `--title` 会在 TTY/config/provider 初始化前失败，避免 flag 顺序决定持久化名称。
- `/title` 继续修改同一个 durable 字段，修改后旧 name 不再匹配，新 name 立即用于后续 shell selector。

## Selector 规则

- ID 始终优先：若 selector 与一个真实 thread ID 完全相同，即使另一个 thread 的 name 相同，也选择 ID。
- name 使用大小写敏感、完整字符串匹配，不做前缀、模糊或 substring 猜测。
- 多个 thread name 相同时明确报 ambiguous，并列出所有候选 ID；用户可改用 ID 或先 `/title` 消除冲突。
- active name 视图用于 resume/fork/archive；archived name 视图用于 unarchive；active + archived 视图用于 export/delete。显式 archived ID 仍到达既有 archived 错误边界。

## 覆盖入口

resolver 接入交互 `resume` / `fork`、旧 `exec --resume`、nested `exec resume`、exec fork-session、archive/unarchive、export 和 confirmed delete。`--last` 继续按 workspace/time 选择，不经过 name matching。

## 验证

单元测试覆盖 ID precedence、active/archived/all scopes、精确 name、missing 与 duplicate ambiguity；flag 测试覆盖 root/chat/fork help 和 title/name 冲突。真实 SSE nested resume 回归改为使用含空格的 name 并验证仍写回原 session；archive/unarchive、export 与 confirmed delete CLI 回归也使用 name selector。全仓既有 ID、latest、workspace、fork、archive 和 recovery 测试保持兼容。

## 已知边界

本轮不强制 name 全局唯一，以兼容已有可重复 title 和自由 `/title`；歧义在选择时安全失败。没有 name 的旧 session 仍只能使用 ID 或 latest selector。
