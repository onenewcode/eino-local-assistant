# Iteration 20260808: git status boundary parsing

## Goal

修正 porcelain `-z` 状态解析的边界，确保 agent 在重命名和冲突场景下拿到可用的结构化事实，而不是错误地把原路径当作另一条状态记录。

## Changes

- 按 NUL 记录消费 Git status 输出。
- 对 rename/copy 消费后续原路径记录，并填充 `original_path` 与 `renamed`。
- 识别 `U` 和 DD/AU/UD/UA/DU/AA/UU 冲突组合，填充 `conflicted`。
- 增加解析级测试，覆盖重命名和冲突状态。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需实现真正的恢复/回滚操作，并为这些会修改或丢弃工作树的 Git 行为接入确认策略。
