# CLI Sessions JSON Output

本轮恢复 `eino sessions --output-format json` 的非交互自动化契约，使脚本和外部界面能够读取 durable session 的机器可读索引，而无需启动 TUI、模型或恢复任一 session。

## 行为

- `eino sessions` 默认仍输出现有的人类可读表格；`--output-format json` 输出按当前排序规则排列的 JSON array，内容是公开的 `ThreadMeta` 索引字段。
- JSON 输入格式不区分大小写并忽略首尾空白；`text` 和 `json` 之外的值会明确报错。没有 session 时 JSON 仍为 `[]`，不会混入文本提示。
- 该命令只打开 session catalog 并读取元数据；输出绝不包含 transcript、system prompt、tool artifact 或模型配置中的 API key，也不会写入 storage 或发起模型请求。

## 验证

- CLI 测试覆盖 JSON 解码、大小写归一化、空列表、非法格式，以及 system prompt 不会出现在 JSON 输出中；文本列表和现有 usage/context 显示回归保持不变。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
