# 迭代：TUI 队列面板取消反馈

日期：2026-08-12

## 背景

队列面板已经支持通过 `x` 删除选中 follow-up，并保证该内容不会进入 transcript 或后续 FIFO drain。此前该快捷操作没有反馈，而 `/queue drop` 会报告被删除的编号和预览，用户难以确认一次面板操作是否生效。

本轮对照 `docs/research/queued-prompts-research.md` 中“删除必须是可观察取消、不能只是隐藏 UI”的结论，补齐面板与斜杠命令的一致反馈。

## 实现

- 面板 `x` 删除选中项后输出 `queue cancelled (N): <preview>` 系统行，并保留原有队列顺序、暂停状态归一化和焦点行为。
- 取消项不会写入用户消息 transcript，也不会被 `drainQueue` 再次提交。
- 新增 TUI 回归断言：面板取消不产生 user 行，同时产生带 1-based 编号和预览的确认行。

## 验证

- `go test ./internal/tui`
- 提交前运行仓库门槛：`go test ./...`、`go build ./...`、`go tool golangci-lint run ./...`
