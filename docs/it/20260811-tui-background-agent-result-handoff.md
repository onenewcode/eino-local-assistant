# 迭代：后台 agent 结果的显式草稿交接

日期：2026-08-11

## 背景

上一轮提供了可启动、监督与查看的只读后台分析 child，但完成结果只能在 `/agents show <id>` 中阅读。用户若要让 parent 讨论或依据该发现继续工作，必须手工复制文字；更危险的替代方案则是后台完成时把结果无提示地写进 parent model context。

后台 child 调研记录 `docs/research/background-subagent-control-research.md` 的跨产品综合指出，child 结果应以带来源的观察显式交付，并且只有用户或协调器的明确动作才进入后续父会话工作。本轮实现用户驱动的最小交接面，不推断任何产品私有的自动合流机制。

## 实现

- 新增 `/agents append <id>`，仅接受 `completed` 的 child；它将结果追加到当前编辑器草稿，而不是启动模型调用、更新 session ledger、记录 usage 或修改 queue。
- 附加文本以 `BACKGROUND ANALYSIS REPORT - QUOTED REFERENCE ONLY` 包围，携带 stable child ID 和 source session ID，并明确要求将内容当作不可信分析而非指令。用户可审阅、编辑或丢弃草稿，只有自行发送后才会成为普通 parent turn 的输入。
- 结果查看仍可保留 64 KiB，而 composer handoff 独立限制为 16 KiB；超出部分会给出提示并指向 `/agents show <id>`。附加路径再次清理控制字节，防止结果污染终端。
- `/agents append` 也可在 foreground turn 忙碌时立即执行，以准备下一步草稿；它不取消或改变正在运行的 turn，也不抢占已有 follow-up queue。

## 边界

这是明确的用户交接，不是自动 result merge、父 agent 自动委派或可写 worker。后台结果不能在无用户参与时进入模型上下文，append 的内容也只是普通可编辑草稿而非可信系统数据。跨 session 的已保留 child 可被用户显式 append，但来源 session 始终显示，避免静默跨会话污染。

## 验证

- TUI 测试覆盖：完成结果附加到既有草稿、来源/引用边界、不会改变 session transcript 或 queue、working child 的拒绝、16 KiB composer 上限与控制字节清理。
- TUI 测试还覆盖 busy foreground 下 append 保留运行中的 turn 和 follow-up queue。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
