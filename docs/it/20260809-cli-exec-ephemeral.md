# CLI ephemeral exec

本轮为非交互 `exec` 增加 `--ephemeral`，对齐 Codex CLI 的同名参数和 Claude Code `--no-session-persistence` 的自动化语义。

## 行为

- 新执行仍使用完整 thread ledger、tool artifact 和上下文管理能力，但写入进程私有的临时 store。
- 命令结束时清理整个临时 store；配置的 `storage.data_dir` 不会新增可恢复 session。
- `--ephemeral` 与 `--resume` / `--recover` 明确互斥，避免一边要求延续持久会话、一边要求不保留会话的歧义。
- text、JSON 和 JSONL 输出协议不变；其中的 session ID 只用于本次运行关联事件，退出后不能恢复。
- 临时目录由系统以当前用户私有权限创建；正常退出、模型错误和受控信号取消均经过统一清理路径。

## 验证

覆盖 flag help、互斥参数、临时 store 生命周期，以及连接本地 OpenAI-compatible SSE 服务完成一次真实 `exec` 后配置存储仍为空。最后运行仓库规定的测试、构建和 lint 门槛。
