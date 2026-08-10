# CLI Session Export

本轮恢复 `eino export <session-id>`，为脚本、归档与审计提供不启动 TUI 或模型的 session transcript 导出入口。

## 行为

- 默认 `--output-format markdown` 以按角色分段的可读形式导出完整可见 transcript；`--output-format json` 输出 `ThreadMeta` 与消息数组，格式大小写不敏感。
- 命令在共享读锁下直接回放 session ledger，不修复或写入可再生 SQLite projection；它不会恢复 session、启动模型、修改 transcript 或创建 artifact。session 不存在、ID 为空或格式不支持会明确失败。
- 导出可以包含用户已经保存到 session 的敏感内容，因此输出目的地由调用方负责保护；命令本身不会额外注入模型配置或 API key。

## 验证

- CLI 与 store 测试覆盖 root/help、Markdown 角色和标题、JSON 解码、无 API key 泄露、格式归一化、空 session ID 拒绝，以及只读导出不重建 SQLite projection。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
