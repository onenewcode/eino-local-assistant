# exec JSON 错误结果

## 背景

JSON 成功结果已经便于脚本消费，但配置错误、权限模式不兼容、恢复冲突或模型失败仍只通过 stderr 返回，调用方必须同时解析 stdout 和 stderr 才能得到统一结果。

## 本轮变更

- `exec --output-format json` 在失败时输出单个 `{"type":"error","error":"..."}` envelope。
- 保留非零进程退出码，JSON 结果不会把失败伪装成成功。
- 文本模式和无效 output format 的行为保持不变。
- JSON 模式的响应在 turn 成功完成前仍不会写入 stdout，避免半截 result 与 error envelope 混杂。

## 边界

错误 envelope 仅包含稳定的错误类型和人类可读消息，不承诺错误字符串作为长期 API；自动化调用应同时检查 `type` 和进程退出码。
