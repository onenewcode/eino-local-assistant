# CLI shell completion

本轮为 CLI 增加 `completion` 子命令，补齐主流 code agent 的终端集成能力。

## 行为

- 支持 `bash`、`zsh`、`fish`、`powershell`，另外接受 `pwsh` 作为 PowerShell 别名。
- 补全脚本由 Cobra 当前命令树直接生成，新增子命令后不会维护第二份静态列表。
- shell 参数缺失或不支持时返回明确错误和非零退出状态。
- 生成过程只写指定输出流，不读取配置、不启动模型、不改变 session 或 workspace。

## 示例

```sh
eino-assistant completion zsh > _eino-assistant
eino-assistant completion bash > /tmp/eino-assistant.bash
```

## 验证

已覆盖 root/command help、四种 shell 输出、PowerShell 别名和非法 shell 参数，并运行仓库规定的测试、构建和 lint 门槛。
