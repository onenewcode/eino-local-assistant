# CLI session export

本轮增加 `export <session-id>`，补齐无需启动 TUI 或模型的会话归档和迁移能力。

## 行为

- 默认输出 Markdown，包含会话 ID、标题和完整可见 transcript。
- `--output-format json` 输出 `{meta, messages}`，保留现有 `ThreadMeta` 和 Eino message 结构，方便外部工具继续处理。
- 使用 durable thread ledger 的 transcript 读取接口，包含 system prompt 与所有可见消息，不受 TUI 最近消息分页窗口限制。
- 只读读取，不创建新 turn、不调用模型、不改变 journal；不存在的 session 返回明确错误。
- 支持的格式为 `markdown` 和 `json`，非法格式返回非零错误。

## 示例

```sh
eino-assistant export 20260715-120000-abc123 > session.md
eino-assistant export 20260715-120000-abc123 --output-format json > session.json
```

## 验证

已覆盖命令帮助、Markdown/JSON 输出、完整 system transcript 和非法格式拒绝，并运行仓库规定的测试、构建和 lint 门槛。
