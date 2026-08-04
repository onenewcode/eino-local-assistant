# 迭代：`/rules` 指令来源可观测性最小切片

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-08-04 |
| 范围 | active session instruction source metadata view |
| 状态 | 已交付 |
| 研究依据 | [rules-hierarchy-cross-product-refresh-research.md](../research/rules-hierarchy-cross-product-refresh-research.md) §1.5、§2.1-2.3、§4.5 |

## 1. 合同

TUI `/rules` 是只读的 source metadata view，不是 reload 命令。它只展示 active
session 创建时已经捕获的用户/项目 instruction source、稳定 title/path、各自预算、
tokens、truncated 和生命周期；不打印正文，不监听文件，也不重新读取磁盘。该能力
保持在 `internal/agent` 的 prompt composition 边界内，通过不含正文的
`PromptLayerSnapshot` 传递 provenance。

fresh session 没有有效 source 时报告 `none`。`/new` 与 `/clear` 的 compose 成功后
更新 snapshot；同一 runtime 内 `/resume` 成功后使旧 snapshot 失效。启动 resume
直接使用 thread 保存的冻结 system prompt，不调用当前 rules composer。因为当前
session 持久化合同没有 provenance，resumed legacy session 会明确报告 source
metadata unavailable，而不会用当前磁盘内容冒充 active snapshot。

## 2. 实现边界

- `/rules` 无参数，加入 slash catalog/help，并在 busy 状态像 `/status`、`/context`
  一样即时执行。
- TUI 通过 `RulesReport` 与 `InvalidateRulesSnapshot` callback/data seam 接入，
  不 import `internal/agent`；缺少 callback 时显示清晰的 unavailable，而不 panic。
- report 只包含用户 bundle 的 path/found/tokens/truncated 与项目 ordered Sources
  的 path/title/tokens/truncated；不新增 watcher、热 reload、remote/import、权限或
  持久化 schema。

## 3. 验证

Focused tests cover snapshot order/metadata, slash parse/catalog/help, `/rules` output
and argument errors, busy execution, missing callback behavior, runtime snapshot
invalidation, and the resume no-recompose seam. Full repository checks are run before
the implementation commit.
