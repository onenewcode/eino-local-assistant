# 迭代：TUI `/diff` 快速审阅

日期：2026-08-08

## 背景

仓库已经有工作区受限的 `git_diff` 工具，但用户必须等待模型主动调用它，无法像主流 code agent 一样直接查看当前修改。代码修改后的快速 diff 审阅是高频交互，不应依赖自然语言回合。

## 实现

- 新增 `/diff` 查看 working-tree diff，`/diff staged` 查看 index diff。
- TUI 复用 registry 中同一个 `git_diff` 实例，因此工作区边界、64 KiB 默认输出上限和截断语义保持一致。
- 命令只读，busy 时也可立即执行，不进入模型 FIFO 队列。
- 无变化时显示 `(no changes)`；工具不可用、Git 错误或参数错误会显示明确错误。
- `git_diff` 的 JSON 结构在 CLI/TUI 两处仍保持同源，不重复实现 Git 命令。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
