# 迭代：exec JSONL 事件流

日期：2026-08-08

## 背景

`exec --output-format json` 适合一次性脚本，但必须等 turn 成功后才能得到结果，无法实时观察工具调用、取消或长响应。主流 code agent 的自动化接口通常提供事件流，同时保留稳定的最终结果事件。

## 实现

- 新增 `--output-format jsonl`，每行一个 JSON 对象。
- 生命周期事件包括 `turn_started`、`assistant_delta`、`tool_start`、`tool_end`、`tool_error` 和最终 `result`。
- `tool_start`/`tool_end` 保留工具名、tool call id、输入或输出；assistant 流式内容使用 `delta` 字段。
- 最终 `result` 包含完整响应、session id、usage、成本和估算标记；运行失败仍输出 `type=error` 并保持非零退出。
- 原有 `text` 和单对象 `json` 行为不变：JSON 模式不会混入中间事件，避免破坏已有脚本。
- 事件来自 `Session.AskWithEvents` 的持久化生命周期，因此实时输出与 thread ledger 观察一致。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
