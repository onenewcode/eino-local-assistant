# CLI MCP get

本轮补齐 MCP 管理的只读单服务查询，采用 Codex/Claude Code 都可观察到的 `mcp get <name>` 命令拓扑。

## 行为

- `eino mcp get <name>` 从用户级 `config.toml` 严格加载指定的 stdio MCP 配置；名称按配置中的唯一 server 名精确匹配，空名称、额外位置参数和未配置名称都会返回明确错误。
- 默认文本输出为单个 server 的纵向详情：name、enabled、transport、command、quoted args、env variable names 与 working directory。`eino mcp get <name> --json` 输出一个 `name`/`enabled`/`transport` 对象，而非单元素数组。
- 查询只读取配置：不启动 MCP 子进程、不调用 `tools/list`、不检查 command 是否存在，也不执行 health check。输出绝不包含环境变量值，避免 token 从排障命令泄露。
- `mcp list` 与 `mcp get` 共用同一投影视图，所以 transport、args、工作目录、启用状态和按字母排序的 env key 语义保持一致。

## 边界

当前仍只支持用户级 TOML 的本地 stdio server。没有实现远程 HTTP/OAuth、project-scoped server、login/logout 或配置写入命令；这些能力需要分别处理认证和副作用边界，不能把静态查询伪装成实时健康状态。

同行产品的可观察行为、静态配置和 live health check 的区别见 `docs/research/mcp-configuration-inspection-research.md`。

## 验证

- 增加单服务文本/JSON 输出、env value 脱敏、空/多余/未知名称和命令帮助的回归测试。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
