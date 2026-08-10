# CLI invocation-scoped tool filtering

本轮为 bare invocation、`chat`、`resume`、`fork`、`exec`、`exec resume`、`review` 与 `exec review` 增加 `--tools` / `--disallowed-tools`，让调用方按任务缩小 agent capability surface，而不修改持久配置。

## 参考与取舍

- Claude Code CLI 提供 invocation-scoped tools allow/deny 入口；本轮沿用同一可预期命令模型，并接受逗号分隔名称。
- Codex CLI 将可用能力与 approval/sandbox 分层。这里同样保持“是否把工具 schema 发给模型”与“工具获准执行的条件”正交，过滤不提升权限，也不绕过现有 permission、risk、command policy 或 sandbox。
- 工具过滤放在内置工具和 MCP server 完成发现与注册之后，因此按 provider 最终看到的真实名称匹配；MCP 名称继续使用 `mcp__<server>__<tool>`，不会另造不稳定 alias。
- Eino ReAct agent 要求至少一个工具。显式 `--tools ""` 不伪造占位工具，而是走同一个 provider 的直接流式模型路径，保持 session、context、usage、structured output 和输出协议不变。

## 行为契约

- 未指定 `--tools` 时默认暴露全部已注册工具；显式空值表示零工具。`default` 和 `*` 作为唯一 allow 值时恢复默认全量。
- `--tools` 与 `--disallowed-tools` 都支持逗号分隔、空白清理和去重。allow 先选集合，deny 后移除，因此 deny 始终优先。
- allow 或 deny 中的未知名称会列出排序后的 available names，并在首次 provider request 前失败。重复注册的最终工具名也会拒绝，避免过滤结果含糊。
- TUI 状态只读取过滤后的 registry，因此 `/status` 和工具列表反映模型实际可见的集合。
- `review` 仍强制 plan、high-risk deny 与 workspace-only，但尊重调用方的过滤。若显式移除 `git_status`、`git_diff`、`git_show` 或读取工具，review 不会暗中恢复它们；调用方负责保留目标所需证据能力。
- 工具被暴露不等于已授权。模型发起调用后，原有 permission mode、high-risk policy、command prefix policy、workspace 和 sandbox 检查仍照常执行；deny filter 则让模型从一开始就看不到该工具。

## 验证

- registry 单元测试覆盖默认全量、allow、deny、deny 优先、显式空列表、`default` / `*`、逗号拆分、去重、未知名称与重复注册名。
- direct model 单元测试确认 nil 拒绝及无 tool binding 的流式 session 调用。
- OpenAI-compatible SSE 回归直接检查 provider request 中的 tools schema，覆盖 allow subset、deny subset、deny 优先和零工具请求；unknown tool 回归确认 provider request 数为零，并保留 JSON `type=error` 契约。
- CLI help 回归覆盖 bare、chat、resume、fork、exec、exec resume、review 与 exec review 的两个 flag。
- 按仓库门槛运行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。

## 已知边界

本轮只支持精确最终工具名，不支持 glob、正则、tool group 或交互式 picker。过滤是单次进程参数，不写回配置或 session；恢复同一 session 时可以选择不同工具集合。零工具模式不会执行模型返回的 tool call，provider 在没有 tools schema 时仍返回非法 tool call 的行为由其兼容层决定。
