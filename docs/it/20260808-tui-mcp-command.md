# TUI `/mcp`

本轮为 TUI 增加 `/mcp` 只读状态命令，补齐 MCP 集成的可观测入口。

## 行为

- MCP 连接层在启动时保留每个配置 server 的名称和 `tools/list` 发现数量。
- `/mcp` 展示 server 名称与工具数量，不重新启动进程或发起额外远程调用。
- 未配置 MCP 时明确显示空目录状态。
- busy 或 compaction 状态下 `/mcp` 立即执行，不进入自然语言 FIFO。
- 工具实际调用仍沿用现有 permission handler；此命令只读，不改变权限状态。

## 取舍

状态来自启动阶段已经完成的 MCP discovery，因此输出稳定且不会因 `/mcp` 阻塞当前 agent turn。当前版本展示连接配置和工具计数，后续可在 SDK 暴露健康检查能力后增加 server capabilities 或重连状态。

## 验证

已覆盖 MCP server 状态计数、slash catalog、TUI 输出和 busy 调度分类，并运行仓库规定的测试、构建和 lint 门槛。
