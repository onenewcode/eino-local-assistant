# 迭代：exec 的 SIGINT graceful cancellation

日期：2026-08-08

## 背景

非交互 `exec` 之前只监听 `SIGTERM`。用户在终端或自动化任务中按 `Ctrl+C` 时，进程没有机会沿 `Session` 的取消路径完成 turn 收尾；这会削弱 thread journal 对取消状态的审计价值，也与 TUI 的中断语义不一致。

## 实现

- `exec` 同时监听 `SIGINT` 和 `SIGTERM`。
- 两种信号都取消 process context，正在运行的 model、工具和子进程沿已有 context cancellation 路径退出。
- `Session` 继续负责把取消结果写入 `turn.cancelled` 生命周期事件；未改变 `--recover` 对真正遗留活动 turn 的显式接管要求。
- 不吞掉信号后的非零错误语义，自动化调用方仍能根据退出状态判断任务未完成。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
