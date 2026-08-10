# TUI `/rename` alias

本轮为会话重命名增加 `/rename` 兼容别名，贴近主流 code agent 的 slash 命令习惯。

## 行为

- `/rename <text>` 与 `/title <text>` 完全共享同一解析、idle 边界、持久化和错误处理。
- slash 菜单只保留 canonical `/title` 行，并将 `/rename` 作为别名匹配，避免菜单重复。
- busy 状态下两种写法都遵循原有 mutative 命令策略，不会被加入 FIFO。

## 验证

已覆盖解析、菜单 catalog、帮助文本和 busy/queue 分类，并运行仓库规定的测试、构建和 lint 门槛。
