# CLI output last message

本轮为非交互 `exec` 增加 `-o <file>` / `--output-last-message <file>`，对齐 Codex CLI 将最终 assistant message 与 stdout 事件流分离的自动化能力。

## 行为

- text、JSON 和 JSONL 三种输出格式都可同时写最终回答文件；stdout 协议保持不变。
- text 仍实时流式输出，内部同时累积完整 assistant response；JSON/JSONL 仍只按原协议发布对象或事件。
- 文件路径相对调用进程的当前目录解析，不受 agent 的 `--cd` workspace 覆盖影响；父目录必须已存在。
- 新文件使用仅当前用户可读写的权限；覆盖现有普通文件时保留原权限。
- 使用同目录临时文件后原子替换，避免中途写入留下半份回答或先截断旧文件。
- 模型 turn、session 清理或文件写入任一步失败都会返回非零；JSON/JSONL 在文件安装成功前不会发布成功 `result`。
- setup 或模型执行失败时不创建、不截断也不替换目标文件。

## 验证

覆盖 flag help、原子替换与权限保留、text/JSON/JSONL 三种真实 SSE 执行、目标路径错误时单对象 JSON error，以及 setup 失败时保留原文件。最后运行仓库规定的测试、构建和 lint 门槛。
