# Iteration 20260808: configurable high-risk policy

## Goal

把风险分类接入可配置行为：用户可以只提示、只确认 high 风险，或直接拒绝 high 风险，同时保留原有 `permission_mode` 对全部副作用工具的控制。

## Changes

- 新增 `tools.high_risk_policy`：`advisory`（默认）、`confirm`、`deny`。
- `confirm` 模式只对 high 风险请求进入 TUI broker；low/medium 仍按 `permission_mode` 执行。
- `deny` 模式直接拒绝 high 风险请求，不执行副作用。
- advisory 不改变既有权限决策，只展示风险。
- 增加策略组合测试、配置示例和文档。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续需要持久化用户选择、按命令/路径配置风险规则，并逐步替换当前保守字符串启发式为更严谨的 shell 解析。
