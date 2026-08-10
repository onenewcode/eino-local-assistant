# CLI 单次调用工具筛选

本轮为所有会启动 agent 的入口增加不持久化的工具可见性控制：bare 交互启动、`chat`、`resume`、`fork`、`exec` 与 `exec resume` 都支持：

```sh
eino chat --tools shell,read_artifact --disallowed-tools shell
eino exec --tools "" "只根据给定上下文回答"
eino exec resume SESSION_ID --tools mcp__docs__search
```

## 行为契约

- `--tools` 是逗号分隔的精确最终工具名列表；未传时保留全部已注册工具，显式 `--tools ""` 禁用全部工具。`default` 或 `*` 作为唯一值表示全部工具。
- `--disallowed-tools` 在 allow 之后执行，始终优先；两个 flag 都会清理空白并去重。混用 `default`/`*` 与具体名称会报错，避免含义不明确。
- 名称按完整注册表验证，包含内置工具、`update_plan` 及已发现的 MCP 工具（如 `mcp__server__tool`）。未知名称会展示排序后的可用名称，并在任何模型 provider 请求前失败。
- 筛选仅决定模型看到的 schema，不增加能力。已选工具继续经过现有 approval、command policy、risk policy、workspace 和 sandbox 边界。
- 工具集为空时使用直接流式模型，不构建空 ReAct 循环；session、持久化、context compaction、最终响应校验和 `exec` 输出协议保持可用。TUI 状态显示空工具集且不展示 ReAct step 预算。
- 筛选只对当前进程有效，不写入配置或 session。恢复同一 session 时可选择不同工具集合。

## 参考与取舍

- Claude Code CLI 2.1.220 的已观察 help 提供 `--tools`、`--allowed-tools` 和 `--disallowed-tools`，其中空 `--tools` 表示禁用工具；本轮采用其清晰的单次调用模型。
- OpenCode 权限文档将 tool permission 的 `allow`、`ask`、`deny` 与 agent 覆盖分层。相应地，本轮不把 schema 筛选误当作权限提升机制。
- Codex CLI 0.146.0 的已观察 help 将 sandbox 与 approval 独立暴露。本轮也将它们同工具可见性保持正交。

调研依据详见 `docs/research/invocation-tool-filtering-research.md`。

## 验证

- `internal/tools` 单元测试覆盖默认、allow、deny、deny 优先、显式空工具集、`default` / `*`、逗号拆分、去重、未知名称和重复注册名。
- 命令测试覆盖六个 agent 入口的 help，以及交互、`exec`、`exec resume` 的 flag 传递与显式空值。
- 运行时测试覆盖零工具时不构造 ReAct、使用直接模型并安全显示为零 step budget。
- 本轮提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 已知边界

本轮仅支持精确工具名，不支持 glob、正则、工具组或按工具参数筛选；后者仍应由既有命令策略和 permission/risk 策略负责。`--tools ""` 不阻止已配置 MCP server 的启动连接，但不会将其工具 schema 暴露给模型。
