# TUI 任务计划状态补齐

## 迭代目标

对齐 Codex CLI 计划卡片和 Claude Code 状态栏的可见状态语义：任务图尚未生成、或控制器仍有待处理 gap 时，转录中的 `Updated Plan` 不能只显示空列表。

## 参考依据

- `docs/research/task-dag-concurrency-research.md`：计划展示应保留稳定状态、失败原因和取消边界；依赖阻塞应可见。
- `docs/research/context-window-status-display-research.md`：常驻状态应使用简短、明确的状态快照，而不是制造未经确认的精确值。
- 网络资料在本迭代不可访问，因此不对参考产品的未公开内部调度实现作推断。

## 实现

- `renderUpdatedPlan` 在 DAG 节点之前先展示 `State` 和有界的首个 `Next` gap。
- fingerprint 纳入 requirements、scenarios 和 gaps，避免 gap 变化时错误去重，确保用户能看到新的下一步。
- 保留完整 DAG 由 `/goal` 展示，任务卡片不暴露 proof 命令、tool call ID 或原始工具输出。

## 验证

- 新增无节点/需规划 gap 的窄宽渲染测试。
- 新增 gap 变化触发新计划快照的去重测试。
- 运行 `go test ./...`、`go build ./...` 和 `go tool golangci-lint run ./...`。
