# 迭代：后台分析 agent 的有界 FIFO 队列

日期：2026-08-11

## 背景

后台分析 child 先前在 4 个并发槽位用完时直接拒绝后续 `/agent` 请求。用户需要手工重试，稳定 ID 也不会被分配，无法把多项互不依赖的只读调查作为一个可监督的工作集提交。

`docs/research/background-subagent-control-research.md` 已将并发数与资源上限列为后台 agent 的基本控制面，并指出过低的并发上限需要排队，而非隐式丢弃任务。本轮把该结论落为本地、可取消且有上限的队列；不推断外部产品未公开的全局 scheduler 或跨进程 persistence。

## 实现

- `/agent` 现在立即分配 `agent-N`，最多 4 个为 `working` / `cancelling`；其余为 `queued`，按 `backgroundAgentOrder` 的 FIFO 顺序等待。
- 任一 running child 的 `completed`、`failed` 或 `cancelled` terminal event 只释放一个槽位，并立即启动队首 queued child。queued child 的独立 context、timeout 与模型调用在实际开始时才创建，等待本身不会耗尽 execution deadline。
- `/agents`、`/agents show <id>` 与 `/tasks` 显示 queued 数量及 task scope。`/agents cancel <id>` 对 queued 项直接转为 `cancelled`，不执行 diff collector 或模型调用；对 running 项仍只取消自己的 context。
- process-local retained record 上限维持 16。新任务优先淘汰最早 terminal record；如果 16 项都不是 terminal，则明确报告 queue 已满。退出 TUI 会取消 running child，并使所有 queued 项 terminal，不能在退出过程中被重新调度。

## 边界

队列只调度上一轮已定义的只读、无工具 child，未引入新的文件、shell、approval 或网络权限。记录和队列仍仅存在当前 TUI 进程，不会写入 session ledger，也不会被恢复或自动合流到 parent 模型上下文。

## 验证

- TUI 回归测试覆盖第 5 项在并发槽满时进入 queued、source session 已切换时仍继续 FIFO dispatch 且不污染当前输出、queued 任务完成、16 条全 live queue 的明确拒绝、queued cancel 不执行模型和 terminal record 淘汰后可再入队。
- 保留启动/取消、退出取消、diff scope、result append 与 display sanitization 的既有覆盖。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
