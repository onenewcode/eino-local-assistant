# CLI structured output

本轮为非交互 `exec` 增加 `--output-schema <file>`，对齐 Codex CLI 的同名能力和 Claude Code 的 `--json-schema` structured output 语义。该 flag 约束的是最终 assistant response；`--output-format text|json|jsonl` 仍只控制 CLI 自身的输出 envelope。

## 行为

- 启动时从进程 cwd 解析 schema 文件，限制为 1 MiB，并接受 JSON Schema object 或 boolean schema。
- schema 使用本地 validator 编译；外部 `$ref` 一律拒绝，避免 schema 加载阶段隐式访问网络。本地 fragment 引用仍由 validator 正常处理。
- agent provider 收到 OpenAI-compatible `response_format.type=json_schema`，其中保留原始 schema，并设置稳定名称与 `strict=true`。
- 上下文 compactor 始终使用不带用户 schema 的普通 provider，避免 checkpoint JSON 被最终输出协议错误约束。
- 最终流到达 EOF 时再次在本地验证完整 JSON。验证失败会记录 `turn.failed`，不提交 user/assistant 消息，也不会写入 `-o` 目标文件。
- JSON 模式失败时只输出 `type=error`；JSONL/text 可能已发布增量内容，但不会发布成功 `result`，进程仍以非零状态退出。

`strict=true` 依赖所选 OpenAI-compatible 服务支持 JSON Schema response format；不支持该协议的服务会返回 provider 错误。schema 路径与 `-o` 一样相对调用进程 cwd，而不是 `--cd` 指定的 agent workspace。

## 验证

测试覆盖 schema 文件大小和类型限制、boolean schema、非 JSON/多 JSON 值、schema mismatch、外部 `$ref` 拒绝，以及 EOF 前的流式校验。真实 SSE CLI 测试还验证 schema 请求下推、合法响应成功、非法响应只产生 JSON error、账本保持 failed/uncommitted、`-o` 保留原内容，并确认无效 schema 在 provider 请求前失败。
