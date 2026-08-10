# exec 会话恢复

## 背景

上一轮的 `exec` 只创建一次性新会话，无法在脚本或 CI 中继续已有 thread。主流 code agent 的非交互入口通常也保留会话恢复和活动任务接管保护。

## 本轮变更

- `exec` 新增 `--resume <session-id>`，沿用 thread 账本、checkpoint 和上下文投影。
- 默认恢复仍拒绝存在活动 turn 的 session，避免两个进程同时写入同一个 thread。
- 新增 `--recover`，且必须与 `--resume` 一起使用；它显式终止活动 turn 后再执行新 prompt。
- 新会话与恢复会话共用同一套工具、权限和模型配置。

## 边界

`exec` 仍然是单次 prompt 执行，不提供 TUI 斜杠命令；`confirm` 权限模式仍会在非交互启动阶段拒绝。恢复保护依赖 thread store 的 revision/CAS 和已有生命周期校验。
