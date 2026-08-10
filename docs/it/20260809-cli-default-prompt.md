# CLI default interactive prompt

本轮让 bare invocation 对齐 Codex CLI 的 `[OPTIONS] [PROMPT]` 默认交互契约。此前 `eino-assistant` 能打开新 TUI，但任何位置 prompt 都会被拒绝，model、workspace、permission、sandbox 和 image 等启动选项必须额外写 `chat`；现在默认入口与显式 `chat` 使用同一个 `sessionStart`。

## 行为

- `eino-assistant <prompt...>` 创建新 session、打开 TUI，并通过既有 startup message 立即提交 prompt。
- `-i/--image` 可重复附加 initial images，继续使用统一 MIME、数量、大小、base64、durable Session 与 context accounting 路径；图片仍必须配合非空 prompt。
- bare invocation 支持 `--title`、`--model`、`--cd`、`--permission-mode`、`--high-risk-policy`、`--sandbox`、`--workspace-only`、`--max-steps` 和 `--no-alt-screen`。
- 无 prompt、无 image 时保持原行为，只打开 idle 新会话。
- 已有子命令拥有自己的同名 local flags，不受 root 默认选项污染；全局 `--config` 仍可放在子命令前后。

## 验证

测试验证 root help 暴露关键 runtime/image flags，纯文本、多词 prompt、image prompt、model/title 与 inline TUI 组合均通过参数解析并到达 TTY 边界，同时 image-without-prompt 在启动前明确拒绝。全仓已有 `exec -i`、`chat -i`、resume/fork 与子命令 help 测试共同覆盖同名 flag 不冲突。
