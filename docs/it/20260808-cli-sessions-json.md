# CLI sessions JSON

本轮为 `sessions` 增加机器可读输出，方便脚本、恢复 UI 和自动化工具查询本地会话。

## 行为

- 默认 `--output-format text` 保持现有表格输出。
- `--output-format json` 输出 JSON 数组，元素使用现有 `store.ThreadMeta` 字段，包括 ID、标题、更新时间、消息数、token 和 cost 统计。
- 没有会话时 JSON 输出为 `[]`，不会混入人类提示文本。
- 不读取 system prompt/API key，不启动模型，也不改变 session 存储。
- 不支持的格式返回明确错误和非零状态。

## 示例

```sh
eino-assistant sessions --output-format json
```

## 验证

已覆盖默认表格兼容、JSON 解码、空/大小写格式归一化和非法格式拒绝，并运行仓库规定的测试、构建和 lint 门槛。
