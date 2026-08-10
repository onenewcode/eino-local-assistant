# CLI fork initial prompt

本轮为交互式 `fork` 增加可选 initial prompt，对齐 Codex CLI 的 `fork [SESSION_ID] [PROMPT]` 启动模式。分支创建与上一迭代保持不变，本轮只补齐“进入 child 后立即开始工作”的入口语义。

## 行为

- `eino-assistant fork <session-id> <prompt...>` 将第一个位置参数作为父 session ID，其余位置参数拼接为初始 prompt。
- `eino-assistant fork --last <prompt...>` 已由 flag 选择父 session，因此全部位置参数都属于初始 prompt。
- prompt 通过 Bubble Tea startup message 进入现有 user transcript、input history、`Session.AskWithEvents` 和 turn lifecycle，不建立旁路 model 调用。
- shell 位置参数始终是用户 prompt；即使内容以 `/` 开头，也不会意外执行 `/clear`、`/exit` 等 TUI slash command。
- 未提供 prompt 时保持原行为：打开 child 后停留在 idle composer。

## 验证

CLI 测试覆盖显式 ID 与 `--last` 两种多词 prompt 解析、无 prompt 兼容和 selector 校验。TUI 测试验证 `Init` 发布一次 startup message、模型 turn 进入 busy 状态、用户行可见，并确认 `/help ...` 形式的初始文本不会被 slash parser 执行。
