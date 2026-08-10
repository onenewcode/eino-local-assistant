# CLI session delete

本轮增加非交互 `delete <session-id> --yes`，补齐脚本和外部 UI 的会话生命周期管理。

## 行为

- 删除必须显式传入 `--yes`；缺少确认时返回非零错误且不修改存储。
- 只接受一个明确 session ID，不提供批量、glob 或全量删除。
- 复用 `ThreadStore.DeleteThread` 和 thread file lock；另一个进程持有锁时拒绝删除。
- 不启动模型、不加载 transcript，也不影响其他 session。
- 成功后输出被删除的 session ID，便于脚本审计。

## 验证

已覆盖 root/command help、缺少确认时保留 session、确认后删除和成功输出，并运行仓库规定的测试、构建和 lint 门槛。
