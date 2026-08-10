# TUI initial image prompts

本轮把上一迭代的 durable image message 扩展到交互入口，对齐 Codex CLI 在新会话、`resume` 和 `fork` 上共同支持 `[PROMPT]` 与 `-i/--image` 的行为。三个命令共享同一 image loader、Eino message 与 Session turn 路径，不存在仅 exec 可恢复、TUI 图片却是临时状态的分叉。

## 行为

- `chat [prompt...]` 现在可在创建 session 后立即提交 prompt；可重复使用 `-i <file>` 附加图片。
- `resume <session-id> [prompt...]` 与 `fork <session-id> [prompt...]` 将首个位置参数作为 session ID，其余位置参数拼接为 initial prompt。
- `resume --last [prompt...]` 与 `fork --last [prompt...]` 已由 flag 选择 session，因此全部位置参数都属于 prompt。
- 图片必须配合非空 prompt；没有 prompt、没有图片时仍只打开 idle TUI，不会自动创建 turn。
- TUI startup event 携带完整 `*schema.Message`。文字与 multipart 图片统一进入 `Session.AskMessageWithEvents`，因此沿用 durable commit、tool events、取消/失败、usage 和 context planning。
- startup prompt 即使以 `/` 开头也始终发送给模型，不执行 TUI slash command；这是 shell 参数与交互 composer 的明确边界。
- 图片路径相对调用进程 cwd 解析，不跟随 `--cd` 切换；格式、数量与 20 MiB 总大小限制和 `exec -i` 一致。

## 验证

CLI 测试覆盖三个命令的 help、图片必须带 prompt、显式 ID/`--last` 多词 prompt 解析，以及合法组合进入 TTY 边界。TUI 测试验证 multipart startup message 进入 busy turn，并在用户行显示附件数量；Session 和 provider 的 durable multimodal 测试继续作为下层契约证据。
