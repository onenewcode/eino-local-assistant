# CLI exec resume command

本轮增加 `exec resume [SESSION_ID] [PROMPT]`，对齐本机 Codex CLI 0.146.0 的非交互恢复命令形状。已有 `exec --resume <id>` / `exec --continue` 保持不变，新入口只负责 selector 与 prompt 解析，随后进入同一个 durable `runExecWithOverrides`。

## 行为

- `exec resume <id> <prompt...>` 将首个位置参数作为 session ID，其余参数拼接为 prompt；ID 会去除首尾空白。
- `exec resume --last <prompt...>` 按当前 `--cd` workspace 选择最近会话，此时全部位置参数属于 prompt；`--last --all` 显式允许跨 workspace 和旧 metadata session。
- prompt 复用上一迭代的输入契约：sole `-` 或省略 prompt 时读取 piped stdin；位置 prompt 与管道并存时追加 `<stdin>` block；文本总计限制为 1 MiB。
- 支持 `--recover`、`--fork-session`、`-i/--image`、model/workspace/permission/risk/sandbox/max-step 覆盖，以及 text/JSON/JSONL、output schema 和 last-message file。
- `--all` 必须搭配 `--last`；缺少 ID 与 `--last` 会在读取 prompt 或 provider 初始化前失败。
- nested resume 不提供 `--ephemeral`：当前 ephemeral store 与 durable source session 隔离，无法安全读取后再承诺不持久化。已有顶层 exec 也继续拒绝 ephemeral + resume/continue。

## 验证

解析测试覆盖 explicit ID、`--last`、`--last --all`、缺失 selector 和非法 `--all`。真实 OpenAI-compatible SSE 回归创建 durable thread，通过 nested command 恢复并提交 follow-up，验证 JSON result 保持原 session ID、response 正确且 thread message count 增长。help 回归确认 nested usage 和 selector/image/output/fork flags 可见；已有 latest、recover、fork、multimodal 与并发 writer 测试继续覆盖共享 runtime。

## 已知边界

Codex help 同时接受 UUID 或 thread name；本仓库 session store 当前只按 ID 定位，display title 尚不是唯一 selector。本轮不引入含糊的 title lookup，也不实现交互 picker。
