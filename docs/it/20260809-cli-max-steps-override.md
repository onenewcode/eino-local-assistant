# CLI max steps override

本轮为 `chat`、`resume` 和 `exec` 增加单次 `--max-steps` 覆盖，用于按任务复杂度调整 ReAct model/tool 循环预算。

## 行为

- 接受 `1..64`；`0` 表示继续使用配置中的 `agent.max_steps`。
- 覆盖仅在本次进程生效，不修改配置或历史 thread。
- 非法负数和超过 64 的值在 provider 初始化前失败。
- `/status` 与 TUI 状态继续展示本次实际生效的 max steps。
- 该 flag 不改变 context token budget、权限模式或 sandbox。

## 验证

已覆盖三条命令的 flag/help、默认保留、有效覆盖和上下界拒绝，并运行仓库规定的测试、构建和 lint 门槛。
