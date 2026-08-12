# TUI 任务计划状态补齐

## 迭代目标

对齐 Codex CLI 计划卡片和 Claude Code 状态栏的可见状态语义：任务清单尚未生成时，转录中的 `Updated Plan` 不能只显示空列表。

## 参考依据

- `docs/research/task-dag-concurrency-research.md`：计划展示应保留稳定状态和取消边界；计划本身不应成为写入闸门。
- `docs/research/context-window-status-display-research.md`：常驻状态应使用简短、明确的状态快照，而不是制造未经确认的精确值。
- 网络资料在本迭代不可访问，因此不对参考产品的未公开内部调度实现作推断。

## 实现

- `renderUpdatedPlan` 在 checklist 节点之前先展示 `State` 和 `done/total` 进度。
- fingerprint 纳入进度和 active task，避免状态变化时错误去重。
- 任务卡片不暴露 tool call ID 或原始工具输出。

## 验证

- 新增无节点状态的窄宽渲染测试。
- 新增进度变化触发新计划快照的去重测试。
- 运行 `go test ./...`、`go build ./...` 和 `go tool golangci-lint run ./...`。
