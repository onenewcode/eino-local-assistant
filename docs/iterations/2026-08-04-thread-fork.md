# 迭代：`ThreadStore.ForkThread` V1 与 `/fork` session 接入

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-08-04 |
| 范围 | `internal/store` source-preserving child ledger、`internal/chat.Session.Fork`、TUI `/fork` |
| 状态 | 已交付；TUI `/fork` 已提供；Esc backtrack 由后续 V1 迭代补充，仍不实现 destructive rewind |
| 实现提交 | store `9f785eb`；chat `89fffbc`；TUI `53c98db` |

## 1. 合同

`ThreadStore.ForkThread(ctx, sourceID, childID, lastTurnID)` 在同一 `ThreadStore` 根目录下
发布一个新的 child thread。调用方可以指定 child ID，也可以让 store 生成新 ID；child 不会
复用 source ID。边界为空时选 source 最新的完整 `turn.committed`，指定边界则必须精确匹配
完整 committed turn。child 从 `thread.created` 到该边界包含完整 journal event prefix，不能
从半个 turn 或任意 transcript 消息位置切分。

child 的 journal 是重建的，不是 source journal 的文件复制：event ID、child thread ID、
`seq`、revision、`expected_revision`、`payload_hash`、`previous_hash` 和 `hash` 都按 child
重新生成。source 边界事件的原始 hash 通过 `ForkSourceHash` 记录为 provenance。child 的
`thread.created` / `meta` 同时记录 `ParentID`、`ForkBoundaryTurnID` 和该 source hash。

边界前 `tool.completed` 事件引用的 content-addressed artifacts 会复制到 child；非截断项有
metadata 和 blob，截断项不复制不存在的 blob。source 的锁不复制，V1 也不复制 checkpoint
文件；child projection 由重建后的 journal 生成。

## 2. 拒绝与安全边界

V1 在 child 发布前拒绝：

- source 有活动 turn 或 pending compaction；
- source 有 active checkpoint，或存在 checkpoint/compaction-derived journal、文件或 usage 状态；
- source 存在 `task.state.updated` 等 task-derived state；
- source 没有完整 committed turn，或请求的 `lastTurnID` 不是完整的 `turn.committed` 边界；
- journal、payload、artifact metadata 或 artifact 内容地址不一致，source 在读取期间变化，或 child 目标已存在。

校验失败、source 变化和 staging/publish 失败不会发布部分 child，也不会改写 source。
source thread 本身只被稳定读取；该操作不是 source thread 的恢复或回滚。

## 3. Chat 与 TUI 产品接入

`chat.Session.Fork(ctx, childID, lastTurnID)` 调用可选的 `ThreadForkRepository`，继承 source
的 model、frozen system prompt、context、pricing、compactor、validator 等配置，并以
`RecoverInterrupted: false` 打开 child。source 不写入、不切换；child 打开失败时已发布的 child
ledger 不回滚，但调用方仍收到错误。

TUI `/fork` 是当前用户入口，合同固定为：

- 仅 idle、无参数执行；按 source 最新完整 `turn.committed` 自动生成 child ID；busy、compacting
  或 pending approval 时拒绝且不入 FIFO，busy 时保留 composer draft。
- child 成功打开前 source 保持 active 且 source ledger 不变；打开成功后才切换到 child。
- child 继承 source title 与 frozen system prompt；切换时重载 child transcript，清理旧 queue、
  sideLines、tool/reasoning cards、task pane 和 turn UI，并发送 active-session notification。
- 当前用户入口是 TUI `/fork`，cmd 层不单独解析同名 CLI 命令；历史 prompt 的 Esc backtrack 另见 [backtrack 迭代记录](./2026-08-04-backtrack.md)，不改变本迭代 `/fork` 的最新 committed boundary 合同。

Fork 只复制 session ledger 的可证明前缀；它不快照或回滚 workspace 文件、git working
tree/index、进程、网络请求、项目 semantic memory 或其他外部副作用，也不实现 destructive
rewind。Codex、Claude 等产品的 fork/rewind 行为只作为交互研究参考，本迭代不宣称等价。

## 4. 验证

实现提交包含 source 保持不变、child journal 重建、parent provenance、artifact 复制、生成
child ID、非法边界/活动状态拒绝、checkpoint/task-state 拒绝、源损坏和原子发布失败等
focused tests；chat 覆盖配置继承、冻结 system prompt 和 active/pending 错误透传；TUI 覆盖
无参数/自动 child ID、source 保持 active、title 继承、旧 queue/side/tool/task UI 清理、busy/
compacting/pending approval 拒绝、draft 保留、stale generation 防护和 child 打开失败回退。
本次同步只更新架构、会话持久化、README 和迭代文档，不修改 Go 代码。
