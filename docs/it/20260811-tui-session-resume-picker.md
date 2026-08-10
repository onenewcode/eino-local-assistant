# TUI Resume Session Picker

本轮为运行中的 TUI 增加无参数 `/resume` 的可搜索会话选择器，对齐主流 Code Agent 的 picker-by-default 恢复路径：

```text
/resume
/resume SESSION_ID
/resume SESSION_ID --recover
```

## 行为契约

- 无参数 `/resume` 读取活跃 session 元数据并打开选择器；它不会启动模型、打开目标会话、创建 fork，或修改当前 session 与 composer draft。
- 列表按已有 `ListThreads` 的最近更新时间顺序显示，只接受非空、非归档且不是当前 session 的候选项。每行包含标题、稳定 ID、消息数和更新时间。
- 输入会按标题或 ID 过滤；`Up`/`Down` 或 `j`/`k` 循环选择，`Enter` 调用同一条既有恢复路径，`Esc` 关闭选择器。搜索仅改变可见候选，绝不隐式选择“最近”。
- 选择器确认的恢复默认不执行 interrupted-operation recovery；显式 `/resume SESSION_ID --recover` 仍是唯一授权恢复中断 turn/compaction 的入口。
- 打开前若 TUI 忙碌、正在 compact、等待命令审批或处理 side question，选择器拒绝打开并保留现有状态。目标打开失败时，当前 session 和 draft 不变，选择器保持打开以便重试或取消。
- 直接 `/resume SESSION_ID` 保持原有行为，包括其对既有 side-question 清理和 runtime-owned `OpenSession` 回调的支持；选择器不复制第二套 session 替换逻辑。

## 参考与取舍

- 已观察的 Codex CLI 0.146.0 将 `resume` 标记为 picker by default，并把 `--last` 设为无 picker 的明确最近会话动作。
- 已观察的 Claude Code 2.1.220 将无 ID 的 `--resume` 定义为支持搜索的交互选择器。
- OpenCode 将特定 `--session` 与最近的 `--continue` 分开建模。因此本轮把“挑选”“指定 ID”和“显式 recovery”保持为可区分的动作，取消不会退化为选择最新会话。

调研依据详见 `docs/research/tui-session-resume-picker-research.md`。

## 验证

- TUI 测试覆盖活跃/归档/当前 session 的候选范围、标题与 ID 搜索、键盘循环导航、成功恢复的 runtime snapshot 替换、失败重试、Esc 取消、无可选 session、审批/side-question/busy 边界、布局高度恢复和直接 ID 恢复。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 已知边界

本轮只列出普通活跃 session，不提供归档范围、跨目录过滤、fuzzy ranking、鼠标支持或 picker 内的 `--recover` 选择。需要恢复中断操作时，用户仍必须显式输入目标 ID 和 `--recover`，避免浏览操作意外变更 durable session 状态。
