# CLI `doctor`

本轮增加非交互 `doctor` 子命令，提供主流 code agent 常见的启动前诊断入口。

## 行为

- 使用与正常运行相同的严格 YAML 加载和配置校验。
- 检查模型配置是否可解析、当前 workspace、session storage 路径、MCP server 可执行文件和工具权限/sandbox 摘要。
- 输出不包含 API key，也不会联系模型 endpoint、创建 storage 目录或启动 MCP 子进程。
- 所有本地检查通过时输出 `result: ok`；配置或依赖检查失败时返回非零错误。

## 取舍

`doctor` 是本地静态诊断，不把网络连通性误报为配置正确，也不为了探测而产生外部进程或文件副作用。MCP server 的工作目录仍由配置加载阶段验证，命令本身额外检查其 executable 是否能由当前 PATH 找到。

## 验证

已覆盖 root/command help、有效配置诊断和无效配置失败路径，并运行仓库规定的测试、构建和 lint 门槛。
