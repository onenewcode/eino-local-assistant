# Session archive lifecycle

本轮增加 `archive <SESSION_ID>` / `unarchive <SESSION_ID>`，对齐 Codex CLI 的非破坏性会话整理能力。归档不是删除或移动目录，而是 thread hash-chain 中的正式状态转换；journal、checkpoint、artifact、usage 和 transcript 都保持原位，可审计、导出并在 unarchive 后继续。

## 存储语义

- 新增 `thread.archived` 与 `thread.unarchived` journal events；`ThreadMeta.ArchivedAt` 由 replay 投影，`state.json` / `meta.json` 仍只是可重建缓存。
- archive/unarchive 在 thread advisory lock 内执行 revision CAS。stale writer、重复 archive、重复 unarchive 都明确失败，不会写重复事件。
- 活动 turn 存在时拒绝改变 archive 状态，避免运行中的 writer 突然从 selector 消失；归档后 store 也拒绝新的 `StartTurn`。
- 默认 `ListThreads` 仅返回 active threads，因而 `sessions`、TUI session list、`resume --last`、`fork --last`、`exec --continue` 与 `exec resume --last` 自动排除归档内容。
- `ListArchivedThreads` 为显式归档视图；`sessions --archived` 支持 text/JSON 输出并在 JSON 中保留 `archived_at`。
- 显式 resume 归档 ID 返回 `store.ErrThreadArchived`，提示先 unarchive；显式 fork 同样拒绝。export 与 delete 仍可直接访问归档 thread。

## CLI 行为

- `archive <id>` / `unarchive <id>` 不要求 TTY，也不要求永久删除确认。
- 成功输出包含动作和 session ID，适合 shell 脚本记录。
- CLI 先读取当前 revision，再提交 archive transition；并发 turn 或 metadata writer 抢先更新时由 CAS 阻止静默覆盖。

## 验证

store 回归覆盖活动 turn 拒绝、stale revision、重复转换、active/archived 两类列表、归档 transcript 保留、新 turn 拒绝和 unarchive 恢复。chat 回归确认显式 OpenSession 返回可识别的 archived error。CLI 回归完成 archive -> 默认列表隐藏 -> archived JSON 可见 -> resume 拒绝 -> unarchive -> 默认列表恢复的完整生命周期，并验证 root help 暴露两个命令。

## 已知边界

本轮按 session ID 操作，不提供交互 picker 或 display-title selector。归档不会压缩或迁移磁盘内容，也不等同于 retention policy；永久清理仍必须使用带 `--yes` 的 `delete`。
