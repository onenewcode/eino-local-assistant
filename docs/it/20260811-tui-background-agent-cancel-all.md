# 迭代：后台 agent 的批量取消

日期：2026-08-11

## 背景

在有界 FIFO 队列下，逐一执行 `/agents cancel <id>` 不适合用户放弃一整组后台调查的场景。退出 TUI 已会停止 child，但用户可能希望保留当前会话、foreground turn 和编辑器草稿，同时只清除后台工作集。

已调研的 Claude Code `agents` 管理面与后台 agent 生命周期强调独立、可查询的 child 控制域（见 `docs/research/background-subagent-control-research.md`）。批量取消是该控制域的本地监督操作，不能借此扩展到 parent turn 或工作区操作。

## 实现

- 新增 `/agents cancel all`。它遍历当前 process-local child：`working` 任务进入 `cancelling` 并只调用自己的 context cancel；`queued` 任务直接变为 `cancelled`，不会读取 diff 或请求模型。
- terminal record 保持不变，已在 `cancelling` 的 child 不会重复发出 cancel。命令明确报告 running 与 queued 两类影响数量；没有可新取消任务时返回无副作用状态。
- 批量取消不会取消 foreground turn、修改 parent session、改变 follow-up queue 或向模型注入结果。后续 child completion 仍走已有的 terminal lifecycle，不会重新调度已取消的 queued work。

## 验证

- TUI 测试覆盖 4 个 running + 2 个 queued child 的批量停止、queued 直接终止、foreground busy state/turn cancel/queue 不受影响，以及重复 `cancel all` 不重复取消。
- 更新 slash catalog/help、README 能力说明。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
