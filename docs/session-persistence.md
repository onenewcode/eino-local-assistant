# 会话账本、上下文压缩与恢复

本文描述当前的 v2 thread 存储和模型工作上下文。它们是两个不同对象：

- 原始事件账本是可审计、可恢复的事实来源；不会因 compact 被改写。
- checkpoint、热 turn 和 artifact digest 组成下一次模型调用的派生工作视图。

持久化数据只写入 `<data_dir>/threads/` 中的 v2 thread 账本。

## 设计边界

1. 每个 thread 只有一个 revision 链；文件锁和 CAS 阻止无声交错写入。
2. 每个 thread 同时只允许一个活动 turn；文件锁、CAS 与生命周期状态机拒绝交错的 tool/commit 事件。
3. user 输入、工具生命周期、完成、失败和取消都可审计；只有已完成 turn 才会进入后续 prompt。
4. checkpoint 不是 assistant 回复，也不覆盖 raw turn；热 JSON 只保留有界的来源证据锚点和 source hash，完整覆盖范围由冷路径 lineage 校验。
5. 大型工具输出优先落为 artifact；TUI 的显示截断不能污染持久化原文。
6. resume 默认只水合活动 checkpoint 与最近 50 个可见 turn；更早 transcript 在向上滚动时分页读取。
7. `/clear` 创建并切换到新 thread，不重写当前 thread；旧队列不会跨 thread 执行。

## 目录布局

默认根目录为 `~/.eino-assistant`，也可用 `storage.data_dir` 覆盖：

```text
<data_dir>/
  threads/
    <thread-id>/
      journal.jsonl
      state.json
      meta.json
      checkpoints/
        <checkpoint-id>.json
      artifacts/
        <sha256>.json
        <sha256>.blob
      locks/
        write.lock
```

`thread-id` 为 UTC 时间排序 ID：`YYYYMMDD-HHMMSS-<6 hex>`。只允许字母、数字、`-` 和 `_`，避免路径穿越。

| 文件 | 角色 |
| --- | --- |
| `journal.jsonl` | 权威 append-only 事件链；恢复时优先重放它。 |
| `state.json` | revision、活动 checkpoint、自动压缩熔断和 meta 的物化投影。 |
| `meta.json` | 列表和 CLI 展示用投影：标题、时间、消息数、token、费用。 |
| `checkpoints/*.json` | 不可变的结构化 checkpoint；有对应 journal 事件后才成为活动 checkpoint。 |
| `artifacts/*.json` | 内容地址、大小、摘要、截断状态和 head/tail 元数据。 |
| `artifacts/*.blob` | 未截断 artifact 的原始字节；截断 artifact 没有 blob。 |

## 事件账本

每条 `journal.jsonl` 事件都带有下列字段：

```text
format_version, seq, event_id, thread_id,
turn_id, correlation_id,
expected_revision, revision, timestamp,
previous_hash, payload_hash, payload, hash
```

`payload_hash` 校验 payload，`previous_hash` 和 `hash` 形成顺序链。每次 mutation 都要求调用方给出已观察到的 `expected_revision`，并通过 CAS 拒绝过期写入。

当前主要事件类型：

| 事件 | 含义 |
| --- | --- |
| `thread.created` | 创建 thread，包含 meta 和 system prompt。 |
| `turn.started` | 接受一个 user 输入、开始 agent 生命周期。 |
| `tool.started` / `tool.completed` | 以稳定 tool call ID 保存工具参数、结束状态和 artifact 引用；同名并发调用不会混淆。 |
| `turn.committed` | 原子写入完整可见 user/assistant 消息及 usage delta。 |
| `turn.cancelled` / `turn.failed` | 保留未提交 turn 的终止原因，不将其送回后续模型。 |
| `context.compacted` | 安装一个活动 checkpoint，并记录父 checkpoint、仅本次新增的直接来源、来源 hash、token 前后值和熔断状态。 |
| `title.changed` | revisioned 标题变更。 |

## 原子写入与恢复

一次 mutation 的顺序如下：

```text
获取 <thread>/locks/write.lock
  -> 重放 journal 并校验 expected_revision
  -> 写入 artifact/checkpoint 临时文件，fsync 后 rename
  -> append 一个 journal 事件并 fsync
  -> 尝试原子写 state.json 和 meta.json（失败时下次从 journal 重建）
释放锁
```

恢复规则：

- journal 是真相源。`state.json` 或 `meta.json` 过期、缺失时，从 journal 重建。
- 新 thread 先在 `threads/.creating-*` 临时目录写入并 fsync 首个 `thread.created`，再原子发布到最终 ID；未发布临时目录不参与列表。
- 已写入但没有被 journal 引用的 artifact/checkpoint 是 orphan，可由后续 GC 清理；不会成为活动上下文。
- journal 已有 checkpoint 事件而 checkpoint 文件缺失时，用事件 payload 重建文件。
- 普通 `resume` 发现仅有 `turn.started` 而没有 terminal 事件时会拒绝接管，避免误伤暂时安静的其他进程；只有用户确认旧进程已退出后显式使用 `resume <id> --recover`，才会以已读取的 revision CAS 写入 `turn.failed`。若期间有其他 writer 更新 revision，则恢复显式失败，不会终止更新后的 turn。
- 末尾 torn journal record 会被恢复到最后一个完整、哈希有效的事件；中间记录、序号、hash 或 thread ID 不一致会显式报 `ErrJournalCorrupt`，不静默丢历史。
- 多 writer 使用 advisory file lock 和 revision CAS。CAS 失败返回 `ErrRevisionConflict`；compactor 丢弃过期候选，而不是覆盖新 turn。

## 工具 artifact

工具回调先把完整参数/结果送到 session recorder；TUI 仅在 `formatToolCard` 渲染时限制字符和行数。

默认保留上限：

| 限制 | 默认值 | 超限行为 |
| --- | ---: | --- |
| 单一 artifact | 4 MiB | 不失败；保存 SHA-256、原始大小、digest、head/tail，标记 `truncated=true`。 |
| 单一 thread 的 blob 总量 | 64 MiB | 不失败；后续 artifact 使用相同的 metadata-only 形式。 |

未截断内容同时存在 `.json` 元数据和 `.blob` 原文。artifact ID 由 SHA-256 派生，重复输出会去重。模型 prompt 不直接塞入原文，而使用 artifact URI、hash 和简短 digest；需要证据时可调用 `read_artifact` 按 byte range（默认 16 KiB、最多 64 KiB）重新打开。该工具从 turn context 获取当前 thread，不能用模型提供的 ID 越权读取其他 thread。截断 artifact 只返回持久化的 head/tail excerpt，并明确标记不完整。

## 模型工作上下文

`ContextPlanner` 按完整 turn/tool group 而不是扁平消息裁剪。它的顺序为：

1. 固定保留系统指令、当前 user 输入、活动 checkpoint 和完整 tool-call/result 组。
2. 以 artifact 引用替代可重新读取的大结果。
3. 优先保留最近 `keep_recent_turns`（默认 12）个完整 turn。
4. 需要时用 checkpoint 覆盖更早稳定 turn；已被 checkpoint lineage 任一祖先覆盖的 raw group 不重复放入 prompt。
5. 仅在压缩不可用或仍超预算时，执行可见的确定性完整 group fallback。

如果系统指令和当前输入本身超过输入预算，planner 返回 `ErrImmutableOverBudget`，要求拆分输入；不会静默删除当前请求。

默认预算：

```text
model_context_tokens       = 32000
output_reserve_tokens      = 4096
usable_input_budget        = 27904
auto_compact_trigger       = 75% of usable budget
post_compact_target        = 45% of usable budget
summary_max_tokens         = 2048
keep_recent_turns          = 12
max_low_gain_attempts      = 2
low_gain_threshold         = 15%
```

可用输入预算始终为 `model_context_tokens - output_reserve_tokens`。

## Checkpoint 与 compaction

`/compact [focus]` 在 idle 的稳定 turn 边界运行。自动 compaction 在一个 turn 成功提交后、下一个 FIFO follow-up 开始前运行；它们都使用同一个配置模型的**原始无工具** `BaseChatModel.Generate`，绝不经过 ReAct agent。

checkpoint 必须是严格 JSON，并包含：

- `task_goal`；
- `constraints`、`confirmed_facts`；
- `decisions` 和理由；
- `attempts_and_results`；
- `files_or_artifacts`；
- `open_questions`、`next_actions`；
- 有序且有界（最多 32 个）的 `source_event_ids` 证据锚点、`source_hash` 和 source range；
- 每条高风险结论的 `observed`、`inferred` 或 `unknown` 置信度。

生成候选前会冻结 `{base revision, active checkpoint ID, source refs, source hash, focus}`。候选通过 schema/source 校验、`summary_max_tokens` 上限和活动 prompt 可安装性检查后才尝试 `context.compacted` CAS；中间有新 turn 或新 checkpoint 时结果被丢弃并报告过期。取消或 deadline 会原样终止 compaction，绝不以 fallback 提交 checkpoint。

完整来源不写入热 checkpoint JSON：每个 `context.compacted` 只持久化**本次新覆盖**的 direct event ID，并以 `ParentID` 链接先前 checkpoint。恢复或规划时沿 lineage 展开全部 direct ID，以决定哪些 raw group 已覆盖；因此来源仍可审计，而第 N 次 compact 的 payload 不会复制前 N-1 次来源或线性膨胀。

当 source 超出 compactor 输入预算时，只能在完整 group 边界分块：先生成 source-linked 中间 checkpoint，再递归 merge。最终 checkpoint 保留原始 source 的有界证据锚点，完整覆盖范围仍由冷路径 lineage 保存，而不只引用中间摘要。模型返回错误、非 JSON、schema/source hash 不匹配或低收益时，最后才安装显式的确定性 fallback checkpoint；fallback 不虚构细节，并要求后续 agent 重读来源。

`keep_recent_turns` 是优先保留的热窗口，而非硬性禁止压缩线。若某个最近完整 group 已因预算被 planner 标记为省略，它会升级为 compaction candidate，避免“模型看不到、压缩器也拒绝处理”的静默丢失。

若自动 compaction 连续两次释放量低于 15%，thread 的 `auto_compaction_paused` 会置为 true；`/context` 显示原因，手动 `/compact` 不受影响。

## Resume、分页与命令语义

| 操作 | 行为 |
| --- | --- |
| `resume <id>` / `/resume <id>` | 读取 state、活动 checkpoint 和最近 50 个可见 turn；不把完整账本水合进模型 prompt。若 thread 有活动 turn，普通 resume 拒绝接管；CLI 的 `resume <id> --recover` 是显式恢复入口。 |
| 向上滚动 | 以稳定 message page 从 event ledger 读取更早 transcript；只扩展 UI 回放缓存。 |
| `/new [title]` | 创建独立 thread，旧 thread 保留。 |
| `/clear` | 与 `/new` 相同地创建并切换到新 thread，同时清空旧队列；不重写旧 thread。 |
| `/compact [focus]` | 创建或合并 checkpoint；raw turn/artifact 不删除。 |
| `/context` | 展示预算、checkpoint、热/省略 group、fallback、低收益和自动暂停状态。 |
| `/queue clear` | 即刻删除尚未开始的 follow-up，不取消当前 turn 或 compact。 |
| `/delete <id>` | 唯一物理删除原始事件、checkpoint 和 artifact 的命令；不能删除当前 thread。 |

生成或 compaction 中，`/help`、`/context`、`/status`、`/sessions`、`/queue` 立即运行。`/compact`、`/clear`、`/new`、`/resume`、`/title`、`/delete`、`/exit` 不会被排队，因此 queued prompt 不会在另一个 thread 或 checkpoint 上执行。

## 相关代码

| 路径 | 职责 |
| --- | --- |
| `internal/store/thread*.go` | 账本、锁、CAS、artifact 范围读取、checkpoint、恢复和 transcript page。 |
| `internal/contextbuild/` | 完整 group planner、结构化 checkpoint、无工具 compactor、递归合并。 |
| `internal/chat/session.go` | 生命周期 recorder、prompt projection、compaction transaction、resume tail。 |
| `internal/agent/react.go` | 发送未截断的工具事件；不承担显示限制。 |
| `internal/tui/` | `/compact`、`/context`、自动压缩 barrier、队列隔离和分页回放。 |
| `cmd/eino-assistant/run_tui.go` | 共享 base model，并分别创建 ReAct model 与 no-tools compactor。 |

所有 `context.*` 数值配置中的 `0` 都表示采用产品默认值，并非禁用开关。例如 `keep_recent_turns: 0` 使用默认 12，`low_gain_threshold_percent: 0` 使用默认 15%。
