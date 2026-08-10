# 迭代：MCP stdio 外部工具桥接

日期：2026-08-08

## 背景

主流 code agent 通常允许通过 MCP 接入项目或组织已有的外部工具。此前本仓库只提供内置 Eino tools，研究阶段已确认 Eino 本身没有可直接复用的 MCP client/transport，因而无法接入外部工具生态。

## 实现

- 使用 `github.com/modelcontextprotocol/go-sdk@latest` 的 `CommandTransport`，按 `mcp.servers` 配置启动本地 stdio server。
- 连接后调用 `tools/list`，支持 MCP cursor 分页；外部 schema 转换为 Eino `ToolInfo`，保留 JSON Schema 参数定义。
- 工具名命名为 `mcp__<server>__<tool>`，避免远程工具名和内置工具冲突；重复命名空间启动失败。
- MCP server session 由 `MCPToolSet` 持有，TUI 与 `exec` 退出时显式关闭，避免留下子进程。
- 每次 `tools/call` 通过现有 permission handler，`confirm`、`deny`、风险策略对 MCP 工具同样生效。
- MCP 的工具错误作为结构化软结果返回给模型；协议连接错误则终止当前启动/调用。
- 当前仅接入显式配置的本地 stdio server；未默认联网发现，不支持将 MCP server 的任意 sampling/elicitation 请求授权给外部进程。

## 配置示例

```yaml
mcp:
  servers:
    - name: local-tools
      command: /absolute/path/to/mcp-server
      args: [--stdio]
      env: {}
```

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
