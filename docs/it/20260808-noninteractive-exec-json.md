# exec JSON 输出

## 背景

非交互执行除了供人类阅读，也常被 CI、脚本和 IDE 集成消费。直接把流式文本接到管道时，调用方无法稳定获得 session、响应和 usage 的边界。

## 本轮变更

- `exec` 新增 `--output-format text|json`，默认仍为 text。
- text 保持逐 chunk 写入 stdout 的低延迟行为。
- json 模式缓冲完整 assistant 响应，turn 成功提交后输出一个单行 JSON result envelope。
- envelope 包含 `session_id`、`response`、prompt/completion/total tokens、`cost_usd` 和 `usage_estimated`。
- 不支持的格式在模型初始化前明确报错。

## 边界

JSON 模式只在成功完成 turn 后输出结果；失败仍通过非零进程退出和 stderr 错误报告，避免把错误文本伪装成成功 result。工具生命周期仍写入 thread ledger，响应内容不会包含 TUI 卡片。
