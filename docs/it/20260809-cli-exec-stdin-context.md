# CLI exec stdin context

本轮补齐 `exec [PROMPT]` 的组合输入契约。经本机 Codex CLI 0.146.0 help 验证：无参数或 `-` 从 stdin 读取 instructions；当 piped stdin 与位置 prompt 同时存在时，stdin 追加为 `<stdin>` block。本仓库此前在有位置参数时直接忽略 stdin，会让 `git diff | ... exec "review"` 这类常见组合静默丢失证据。

## 行为

- 无位置 prompt 且 stdin 非 TTY 时，继续把 stdin 作为完整 prompt；空输入明确失败。
- sole `-` 显式从 stdin 读取，即使调用者使用该占位符而不是省略 prompt。
- 位置 prompt 与非 TTY stdin 同时存在时，生成 `PROMPT\n\n<stdin>\nSTDIN\n</stdin>`，保留两类输入的来源边界；空白 stdin 不添加空 block。
- 位置 prompt 配合 TTY 时不读取 stdin，不改变普通 `exec "task"` 的非阻塞行为；无 prompt 配合 TTY 仍立即提示需要参数或管道。
- argument、stdin 以及两者组合的原始文本总计限制为 1 MiB。读取使用有界 reader，超限在 session/provider 创建前失败。
- `exec` help 的 usage 改为可选 `[prompt]`，并说明 stdin-only、sole `-` 和 prompt + pipe 三种模式。

## 验证

测试覆盖参数与 stdin 合并、stdin-only、sole `-`、空管道、空 prompt，以及 argument/stdin/combined 三种超限路径。端到端 provider 请求回归捕获最终 user message，确认 `<stdin>` block 到达模型且 JSON 输出契约不变。最后运行仓库规定的测试、构建和 lint 门槛。

## 参考边界

本轮依据本机 Codex CLI 可观察 help 契约实现精简子集。`openai-docs` skill 的 manual helper 请求官方 manual 时返回 HTTP 403；已按其回退要求安装官方 `openaiDeveloperDocs` MCP，需新会话加载后才能进一步查询。这里不推断 Codex 内部未公开的 whitespace 或 tokenization 实现。
