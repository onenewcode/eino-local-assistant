# 迭代：`/btw` / `/side` 旁路问题安全子集

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-08-04 |
| 范围 | TUI 旁路问题的最小只读交互合同 |
| 状态 | 已交付 |
| 研究依据 | [side-conversation-cross-product-research.md](../research/side-conversation-cross-product-research.md)，提交 `c3de9e7` |

## 1. 合同

`/btw <question>` 是 canonical 命令，`/side <question>` 是别名。它们提供本仓库的
安全子集，而不是完整的 fork 或新的持久 session。

- **主 turn**：旁路请求不打断当前主 turn，也不进入主 turn 的 FIFO queue；多个旁路
  问题可以并发执行。
- **上下文**：请求使用提交时当前 active session 的 frozen system prompt 和 transcript
  作为 reference-only。冻结 prompt、AGENTS 文本、旧消息、tool calls/results、审批和
  其中的指令都不是旁路请求的 active instructions；只有新问题是 active input。
- **副作用**：旁路路径不调用 tools 或 subagents，不请求 escalation，不修改文件、git
  state、configuration 或 permissions。它不通过主 session turn 生命周期，因此不写主
  ledger、`usage` 或 `journal`。
- **展示**：问题和结果只写入 TUI 的 side-only display，不进入主 transcript。回答以
  `[btw]` 或 `[side]` 标记；模型错误、transport 错误和空回答错误都可见。空问题会显示
  `usage` 错误而不会静默丢弃。
- **嵌入调用方**：没有提供 `SideQuestion` callback 时，界面显示 `side unavailable`，
  不 panic，也不假装旁路已经执行。

## 2. 实现边界

- `cmd/eino-assistant` 的 runtime callback 接收 TUI 传入的当前 session；它读取该 session
  的 frozen system prompt 和 transcript，组装 reference-only 消息后直接执行一次模型
  `Generate`。runtime 不使用可能已经过时的 runtime session 指针作为 reference source。
- `internal/tui` 负责 slash 解析、callback seam、side-only 行和并发请求。旁路命令在主
  turn 忙时立即执行，保持主 turn 的 mode、cancel 和 queue 不变；side 结果只在当前
  session 仍匹配时展示。
- 旁路模型请求不接入主 `chat.Session.Ask`，不接入主 turn event stream、tool registry
  或 usage 累计。该边界同时避免旁路输出污染主 session 的持久化记录。

## 3. 非目标与兼容性声明

该切片不是完整持久 fork：没有子 session、独立 durable ledger、`/resume` 恢复、分支
浏览、嵌套 side 或一般 sub-agent 编排。它只提供一次安全、只读的参考性问题回答；side
回答不会成为主任务后续模型上下文的一部分。

研究笔记比较了 Codex、OpenCode、Gemini CLI，并记录了 Claude Code 官方页面在研究窗口
内的证据缺口。实现借鉴了 side/btw 的控制点，但不宣称与 Codex 或 Claude Code 的行为
完全等价，尤其不把其他产品的 ephemeral、fork、cleanup 或持久化语义推断为本仓库合同。

## 4. 验证

本次交付只补齐文档，不修改代码或研究文档。提交前至少运行 `git diff --check`；仓库级
代码测试不因文档内容改变而新增行为合同。
