# 迭代：exec 从 stdin 读取 prompt

日期：2026-08-08

## 背景

主流 code agent 的非交互入口既支持位置参数，也支持把任务通过管道交给 CLI，便于和 CI、编辑器或其他脚本组合。此前 README 已描述 `exec` 支持非 TTY 输入，但实现仍强制要求至少一个位置参数，导致管道场景无法工作。

## 实现

- `exec [prompt]` 现在允许省略位置参数。
- 有位置参数时，参数拼接后的 prompt 优先于 stdin，避免无意读取管道内容。
- 无位置参数时从 stdin 读取完整 prompt，并去除首尾空白；空输入直接报错。
- 交互式 stdin 且没有参数时立即提示需要 prompt，不会让命令意外阻塞等待输入。
- JSON/JSONL 的错误封装仍由原有 `exec` 输出协议负责。


## 验证

```sh
printf '%s\n' "检查当前仓库的测试状态" | eino-assistant exec --config config.yml
go test ./...
go build ./...
go tool golangci-lint run ./...
```
