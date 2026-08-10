# Workspace-aware latest session resume

本轮增加无需复制 session ID 的最近会话恢复，对齐 Codex CLI `resume --last` / `exec resume --last` 与 Claude Code `--continue` 的当前项目语义。

## 行为

- `exec -c` / `exec --continue` 继续所选 workspace 最近更新的会话。
- `resume --last` 在 TUI 中恢复同一范围的最近会话；仍可使用显式 session ID。
- 两条路径都支持 `--all`，仅在用户显式要求时跨 workspace 选择，并包含升级前没有 workspace metadata 的旧 thread。
- `--continue` 与 `--resume` 互斥；`--all` 必须搭配 `--continue` 或 `--last`；ephemeral 执行不能恢复任何会话。
- `--recover` 可与显式 ID 或 latest 选择组合，活动 turn 的保护边界不变。

## Workspace metadata

- 新建 session 持久化经过绝对路径和 symlink 解析的 workspace；`--cd` 会参与该规范化。
- TUI `/new`、`/clear` 和 `/fork` 延续当前运行 workspace，避免后续 latest 选择串到其他项目。
- `ThreadMeta.workspace` 是向后兼容的可选字段，不升级账本格式；旧 thread 默认不会被 workspace-scoped latest 误选。
- latest resolver 复用 `ListThreads` 的 `updated_at` 降序契约，并在连接模型前完成选择和空结果校验。

## 验证

覆盖 symlink 规范化、workspace 与全局排序、空 workspace、参数互斥、metadata 持久化、fork 继承，以及连接本地 OpenAI-compatible SSE 服务后只有目标 workspace 的最近 thread 被继续。最后运行仓库规定的测试、构建和 lint 门槛。
