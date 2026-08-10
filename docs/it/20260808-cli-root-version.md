# CLI root `--version`

本轮为根命令增加 `--version`，与已有 `version` 子命令共享同一个构建版本值。

## 行为

- `eino-assistant --version` 直接输出应用名和版本，不读取配置、不要求 TTY、不启动模型。
- `eino-assistant version` 的既有行为保持不变。
- 版本值仍可通过构建时 `-ldflags "-X main.version=..."` 注入，避免两套来源漂移。

## 验证

已覆盖 root help 中的版本入口和 `--version` 输出，并运行仓库规定的测试、构建和 lint 门槛。
