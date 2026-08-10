# 审批请求超时

## 背景

主流 code agent 的审批请求不能无限期阻塞工具循环。此前本项目只有 turn context 取消路径；用户离开终端或忘记处理请求时，工具可能一直等待。

## 本轮变更

- `PermissionBroker` 为每个审批请求增加独立等待超时，默认 5 分钟。
- 新增 `tools.permission_timeout_seconds`，范围为 `0..3600`；`0` 使用默认值。
- 超时只会拒绝当前请求，不会自动批准，并返回明确的 timeout 错误。
- TUI 收到超时事件后移除对应 FIFO 请求并显示 `permission timed out`。
- turn 取消仍优先按原有取消路径处理，不误报为审批超时。

## 验证与边界

超时是审批层的生命周期保护，不是命令执行超时；`run_command.timeout_seconds` 仍独立控制 shell 进程。超时不会替代沙箱或风险策略，也不会改变持久化授权规则的 TTL。
