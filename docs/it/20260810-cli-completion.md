# CLI Shell Completion

本轮恢复 `eino completion <shell>`，让当前 CLI 命令树可以直接提供 shell 补全脚本，避免用户手工维护 command、flag 与子命令列表。

## 行为

- 支持 `bash`、`zsh`、`fish`、`powershell`，并接受 `pwsh` 作为 PowerShell 别名。
- 脚本由 Cobra 从当前命令树即时生成，因此包含新的 `mcp`、`sessions --output-format` 等命令和参数，不存在静态补全清单漂移。
- shell 参数缺失或不支持时返回明确错误。生成过程只写标准输出，不读取用户配置、不启动模型、不创建 session，也不修改 workspace。

## 示例

```sh
eino completion zsh > _eino
eino completion bash > /tmp/eino.bash
```

## 验证

- CLI 测试覆盖 root/help 暴露、四种 shell、PowerShell 别名、非空脚本输出以及不支持 shell 的错误。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
