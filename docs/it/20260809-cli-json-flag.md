# CLI JSON event flag

本轮为非交互入口增加 Codex-style `--json`，作为既有 `--output-format jsonl` 生命周期事件协议的短入口。Claude-style 的 `--output-format text|json|jsonl` 保持不变，因此脚本可选择单对象 JSON result 或流式 JSONL events，而不需要迁移旧参数。

## 行为

- `exec --json` 输出既有 `turn_started`、`assistant_delta`、`tool_*`、`result` / `error` JSONL，每行一个完整对象。
- root `review`、`exec review` 与 `exec resume` 同样暴露 `--json`，都进入同一个 event writer。
- `--json --output-format jsonl` 允许显式重复声明；与 `text` 或单对象 `json` 同时指定时在读取 prompt 和 provider 初始化前失败，避免参数顺序决定结果。
- `--output-format json` 的 buffered single-result 契约不变，`--json` 不作为它的别名。
- text streaming、`-o/--output-last-message` 与 structured output schema 继续沿用原实现。

## 验证

单元测试覆盖默认映射、显式 jsonl 和冲突组合。真实 OpenAI-compatible SSE 回归执行 `exec --json`，逐行解码并确认首事件为 `turn_started`、末事件为带最终 response 的 `result`。review 与 nested resume help 回归确认 flag 在共享入口可发现；全仓既有 JSON/JSONL success、setup error、tool lifecycle、last-message file 和 schema 测试继续通过。
