# TUI saved-session fork selector

本轮补齐 TUI 对已保存 session 的显式分叉入口：

```text
/fork
/fork <session-id-or-name>
/fork --last
```

## 行为契约

- 无参数 `/fork` 保持既有行为：从当前 TUI session 的最新 committed turn 创建并切换到 child。
- `/fork <id-or-name>` 的名称只在 active sessions 中选择 parent；稳定 ID 优先，display name 采用大小写敏感的完整匹配，同名会列出候选 ID 并拒绝猜测。归档名称不进入该 selector；显式归档 ID 仍交由 durable fork boundary 明确拒绝。
- `/fork --last` 显式选择 durable active-session 列表中最新的一条；它不会打开 picker，child 仍从稳定 committed boundary 生成。
- 生产 runtime 在读取 parent 的 durable model/reasoning binding 后构建候选模型，再打开 source 并发布 child。候选构建、source open 或 durable fork 失败时，当前 runtime session/model bundle 和 TUI session 均不切换。
- child 的系统提示、model binding 与完整 provenance 继承 parent；parent journal 不被恢复或改写。若 child 已写入 durable store 但无法打开，错误会显示 child ID，便于后续 `/resume` 或 lifecycle 操作，而不会尝试删除它。

## 参考与取舍

已重新核验的 Codex CLI `0.146.0` 提供 `fork [SESSION_ID] [PROMPT]`、picker 与显式 `--last`；Claude Code `2.1.220` 则将相同的“新 session identity”保证放在 resume 的 `--fork-session` 中。TUI 采用现有 `/fork` 无参数“分叉当前 session”语义以避免破坏已建立工作流，并新增 explicit selector 与 `--last`，而非将 current-session fork 改成 picker。

调研依据详见 `docs/research/session-fork-research.md`。该研究笔记的已部署产品帮助已于 2026-08-11 重核。

## 验证

- 新增 TUI 测试覆盖按名称选择 parent、`--last`、归档名称排除、runtime binding callback，以及分叉后 previous active session 不变。
- runtime 测试覆盖 source durable model/reasoning 继承、child provenance，以及失败 source 不污染当前 bundle。
- chat/TUI 测试覆盖 child 已发布但内存打开失败时显示 durable child ID。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
