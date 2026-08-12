# `/context` measurement semantics audit

## 审阅范围

对照 `docs/research/context-window-status-display-research.md` 审阅了 `/context` 命令、上下文格式化函数及现有回归测试。

## 结论

当前实现已清楚区分三种语义，无需代码调整：

- `Last provider request: context=<used>/<window> (<percent>%)` 只来自最近一次 primary provider usage，表示 exact snapshot。
- `Planner estimate (local truncation/compaction only; not API usage)` 下的 `planned_view` / `current_request_estimate` 明确是本地估算，不冒充 provider usage。
- 没有可信 provider snapshot 时显示 `Last provider request: context=unknown`；不会用 planner 的零值伪造 `0%`。

## 验证

- `go test ./internal/tui ./internal/usage`
- 已有 `TestContextCommandSeparatesAPISnapshotFromPlannerEstimate` 覆盖 unknown 与 estimate 标签。
- 已有 usage formatter 测试覆盖 exact snapshot 与 unknown 的输出格式。

本迭代是审计记录，不改变运行时代码或用户可见文字。
