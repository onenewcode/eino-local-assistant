# CLI inline JSON Schema

本轮增加 Claude Code print-mode 风格的 `--json-schema '<JSON>'`，与已有 Codex-style `--output-schema <FILE>` 共同覆盖自动化 structured output。两种入口只改变 schema 来源，provider response format、stream completion validation、session transaction 和 machine-readable error 都复用同一实现。

## 行为

- `exec --json-schema '<schema>' <prompt>` 接受 JSON object 或 boolean schema，最大 1 MiB。
- root `review`、`exec review` 与 `exec resume` 同样暴露内联 schema flag；所有入口都通过 `execRuntimeOptions.OutputSchemaJSON` 进入共享 loader。
- `--json-schema` 与 `--output-schema` 互斥，即使内容等价也在读取 config、创建 session 或请求 provider 前失败，避免参数顺序决定 schema 来源。
- inline 与 file schema 都使用 `UseNumber`、single-JSON-value 检查、同一 JSON Schema compiler 和禁止外部 `$ref` 的 loader，不因内联输入放宽网络/文件边界。
- provider 仍接收 strict `response_format.type=json_schema`；最终 assistant response 在 turn commit 和 `-o` 写入前执行本地校验。

## 验证

单元测试覆盖 inline schema 编译/验证、1 MiB 上限与双来源冲突。真实 OpenAI-compatible SSE 回归通过 inline schema 返回合法 JSON，捕获请求并确认 `additionalProperties=false` 被转发；冲突回归确认 provider request count 保持为零。review 与 nested resume help 测试确认 flag 可发现，既有 schema mismatch 不提交 turn、不覆盖 last-message file 的事务回归继续通过。

## 已知边界

shell 会处理引号和转义，复杂 schema 更适合继续使用 `--output-schema <FILE>`。本轮不增加 YAML schema、远程 schema URL 或外部 `$ref`；这些形式会扩大解析与供应链边界，且不是参考 CLI 的必要契约。
