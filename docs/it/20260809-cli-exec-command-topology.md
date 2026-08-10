# CLI exec command topology

本轮对齐 Codex CLI 0.146.0 的非交互入口拓扑：`exec` 提供短别名 `e`，code review 同时可从 root `review` 与 `exec review` 进入。此前本仓库只有完整 `exec` 名称和 root review，脚本迁移时需要改写常见命令形状。

## 行为

- `e [prompt]` 是 `exec [prompt]` 的 Cobra alias，继承 stdin、image、session resume/fork、runtime override 和输出协议的全部行为，不创建第二套 flag 或执行路径。
- `exec review [instructions]` 与 `e review [instructions]` 各自创建同一 `newReviewCommand`，因此 selector、只读 plan 边界、merge-base evidence、stdin instructions 和 machine-readable output 与 root `review` 完全一致。
- root `review` 保持兼容；本轮没有重命名或删除已有入口。
- 精确的 `exec review` 现在按子命令解析，这是与 Codex command topology 对齐的有意变化；普通 review prompt 可写成更完整的位置文本。

## 验证

CLI 测试覆盖 `e --help` 的 alias 可见性、`exec review --help`、`e review --help` 的 selector/output flags，以及 nested review 在 provider 创建前执行互斥 selector 校验。既有 root review SSE 与写权限拒绝回归继续验证共享实现的运行时语义。

## 已知边界

Codex 还提供 `exec resume [SESSION_ID] [PROMPT]`。本仓库当前继续使用 `exec --resume <id>` / `exec --continue`；位置参数 selector 会影响 stdin、image、latest 和 fork 组合，留作下一次独立迭代处理。
