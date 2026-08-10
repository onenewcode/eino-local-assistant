# 迭代：可配置的 ReAct 循环预算

日期：2026-08-08

## 背景

当前 agent 的 model/tool 循环固定为 8 步。主流 code agent 会把多步执行预算作为可审计的运行参数：简单问答可以降低上限，复杂修复或自动化任务可以显式提高上限，避免代码里隐藏一个不可调整的成本边界。

## 实现

- 新增 `agent.max_steps` 配置，`0` 使用默认值 8，允许范围为 1-64。
- `chat`/TUI 与 `exec` 统一使用同一个有效预算，不改变默认行为。
- `/status` 继续显示 `max_step`，现在显示实际生效值而不是硬编码常量。
- 保留 `NewReActModel` 默认构造函数，内部 API 调用方不需要立即迁移；CLI 使用带显式预算的构造函数。
- 无效配置在启动前拒绝，避免运行中途才发现 loop budget 不可用。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
