# 会话账本、上下文压缩与恢复

本文描述当前的 v2 thread 存储和模型工作上下文。它们是两个不同对象：

- 原始事件账本是可审计、可恢复的事实来源；不会因 compact 被改写。
- checkpoint、热 turn 和 artifact digest 组成下一次模型调用的派生工作视图。

持久化数据只写入 `<data_dir>/sessions/` 中的 v2 session 账本。

**边界**：本文是会话恢复与压缩，**不是**跨会话语义记忆。项目规则（`AGENTS.md`）、`/memory` 与自动 candidate 见 [memory.md](./memory.md)。

## 设计边界

1. 每个 session 只有一个 revision 链；文件锁和 CAS 阻止无声交错写入。
2. 每个 session 同时只允许一个活动 turn；文件锁、CAS 与生命周期状态机拒绝交错的 tool/commit 事件。
3. user 输入、工具生命周期、完成、失败和取消都可审计；只有已完成 turn 才会进入后续 prompt。
4. checkpoint 不是 assistant 回复，也不覆盖 raw turn；v2 热 JSON 将本次 direct source 与持久父 checkpoint 分离，完整覆盖范围由冷路径 lineage 校验。
5. 大型工具输出优先落为 artifact；TUI 的显示截断不能污染持久化原文。
6. resume 默认只水合活动 checkpoint 与最近 50 个可见 turn；更早 transcript 在向上滚动时分页读取。
7. `/clear` 创建并切换到新 session，不重写当前 session；旧队列不��跨 session 执行。
8. 自主复杂任务将图、proof 接受状态和中断状态写为 `task.state.updated`；`/resume` 恢复该投影，但恢复时仍为 `working` 的节点先转为 `needs_replan`，不会自动重放操作。普通后续输入延续原始需求；只有显式以当前用户原文替换 `user-request` 才改变任务范围并使相关 proof 失效。
9. `ThreadStore.ForkThread` 是 `internal/store` 的 V1 source-preserving ledger primitive；`chat.Session.Fork`、TUI `/fork` 与 idle 两阶段 `Esc` backtrack 在其上提供 child session 接入。backtrack 的可见 committed user prompt 包括首个 prompt；首个 prompt 通过显式 `ThreadForkBeforeFirstRepository` / `ForkThreadBeforeFirstTurn` / `Session.ForkBeforeFirstTurn` 创建只含 `thread.created` 的 child，普通 `ForkThread` / `Fork` 的空 `lastTurnID` 仍表示 latest，两种语义不能混用。backtrack 不实现 destructive rewind 或 workspace、Git、外部副作用回滚。

fresh `exec --ephemeral` 使用同一个 v2 `ThreadStore` API，但根目录是本次进程创建的空临时目录，runtime 关闭时整体删除。`exec resume <id> --ephemeral` 和 `exec resume --last --ephemeral` 则在 durable source thread 锁下复制选定 session 的 authoritative journal、materialized state/meta、checkpoints 与 artifacts 到该临时根目录，再由临时 store 承担整个 resumed turn；source session 不接收 turn、journal、state、checkpoint 或 artifact 写入，source `locks/` 也不复制。ephemeral `--last` 使用不修复 durable projections 的只读 session 列表；普通 durable `--last` 仍使用既有 `ListThreads` 路径。该能力只保证 session ledger 的本次运行生命周期，不宣称 fork 语义；工具已经造成的工作区、网络或其他外部副作用不会回滚，项目级 semantic memory 也不会被清除。

Headless `exec --output-schema FILE` 在打开/创建 session 前读取并编译本地 JSON Schema；文件不存在、不可读、JSON 无效或 schema 无效都是 input error，不会调用模型。有效 schema 只用于最终 assistant Content：内容必须是 JSON 实例且匹配 schema，校验发生在 `turn.committed` 前。失败会沿用 turn failed lifecycle，不写入 assistant message，但已记录的 provider usage 保留；`--output-last-message` 也只在校验成功并提交后写入。该能力是本地最终响应校验，不是 provider-enforced structured output，不会把 `response_format` 注入 ReAct loop；相对 `$ref` 按 schema 文件所在路径解析。

## V1 source-preserving thread fork（store、chat 与 TUI）

`9f785eb` 在 `internal/store` 新增可选的 `ThreadForkRepository` 扩展和 `ThreadStore.ForkThread(ctx, sourceID, childID, lastTurnID)`；`89fffbc` 增加 `chat.Session.Fork`，`53c98db` 将它接入 TUI `/fork`。store primitive 仍负责 source 一致性、边界拒绝、child journal 重建与原子发布；chat 负责继承 source 的模型、frozen system prompt、context、pricing、compactor、validator 等配置并打开 child；TUI 提供当前用户可见的 idle-only、无参数命令，cmd 层不单独解析同名 CLI 命令。该实现不宣称与 Codex 或 Claude 的 fork/rewind 行为等价。

TUI `/fork` 传入空 child ID 和空边界，因此由 store 选择 source 最新完整 `turn.committed` 并自动生成 child ID。source 在 child 成功打开前保持 active 且 source ledger 不变；打开失败时仍留在 source。成功后 TUI 切换 active session，沿用 source title 与 frozen system prompt，重载 child transcript，并清理旧 queue、sideLines、tool/reasoning cards、task pane 和相关 turn UI；`/fork` 不会在 busy、compacting 或 pending approval 时排队。

成功调用的合同如下：

- **Child identity**：`childID` 可由调用方提供，或在为空时生成新的 thread ID；它必须不同于 `sourceID`，且目标目录必须不存在。源 thread 不会被改名或替换。
- **Committed prefix and before-first boundary**：普通 `ForkThread` / `Session.Fork` 的 `lastTurnID` 为空时仍选择 source 中最新的 `turn.committed`；否则必须精确匹配一个完整的 committed turn。可见首个 committed user prompt 是合法的 backtrack target，但 TUI 对它使用显式 `ThreadForkBeforeFirstRepository` / `ForkThreadBeforeFirstTurn` / `Session.ForkBeforeFirstTurn`，child 只重建 `thread.created`。该 child 的 boundary turn 与 `ForkBoundaryTurnID` 为空，`ParentID` / `ForkSourceHash` 仍保留，且 child 没有任何 turn group。普通 empty-`lastTurnID` latest fork 与显式 before-first fork 是两个不能混用的语义；普通 fork 的 child 仍包含从 `thread.created` 到选定 commit（含）的完整 journal 事件前缀，不能从半个 turn 或任意消息位置切开。
- **Journal rebuild**：child 不复用 source 的事件 envelope。每个 child event 生成新的 `event_id` 和 `thread_id`，`seq`、revision、`expected_revision` 从 child 重新编号，`payload_hash`、`previous_hash` 和 `hash` 重新计算。source boundary event 的原始 hash 只作为 provenance 保存，不是 child hash chain 的前置 hash。
- **Parent provenance**：child 的 `thread.created` / `meta` 写入 `ParentID`、`ForkBoundaryTurnID` 和 `ForkSourceHash`；后者是 source 在边界事件上的 journal hash。其余前缀 payload 通过重放生成 child 的 state/meta，而不是直接把 source projection 当作 child 真相。
- **Artifacts**：只复制边界前 `tool.completed` 事件实际引用的 content-addressed artifacts；非截断 artifact 同时复制 metadata `.json` 和原始 `.blob`，截断 artifact 不会伪造 blob。source 的 `locks/` 不复制，V1 也不复制 checkpoint 文件；child 的 checkpoint 目录保持为空。
- **Source stability**：源 ledger 只读；已有写锁时使用共享读锁，没有写锁时用有界 fingerprint/retry 检查稳定性。源在读取或发布前发生变化会失败，不会把移动中的 source 静默分叉。child 先在 staging 目录完整重建并校验，再原子发布。

V1 在发布 child 前，无论是普通 fork 还是显式 before-first fork，都拒绝以下状态：source 有活动 turn、pending compaction、active checkpoint 或任何 checkpoint/compaction-derived journal、文件或 usage 状态；存在 `task.state.updated` 等 task-derived state；以及 journal、payload 或 artifact 引用不一致。普通 `ForkThread` 另外要求存在完整 committed turn，且 `lastTurnID` 必须是完整的 `turn.committed` 边界；显式 before-first fork 不把空 boundary 当作普通 fork 的 latest sentinel。目标冲突、source 变化或校验失败同样不会发布部分 child，且不会修改 source。该拒绝集合是 V1 的安全边界，不是完整的 backtrack / rewind 产品语义。

`ForkThread` 与 `/fork` 只操作 session ledger 和 TUI session 选择：普通空 `lastTurnID` 仍选择 latest。TUI backtrack 对包括首个 prompt 在内的可见 committed user prompt 创建 child；首个 prompt 走显式 `ForkThreadBeforeFirstTurn` / `Session.ForkBeforeFirstTurn`，只产生 `thread.created`，后续 prompt 才按 committed prefix 复制。before-first child 的 `ParentID` / `ForkSourceHash` 保留，boundary 为空且没有 turn groups；source unchanged。选中的 prompt 会放回 composer，不会预写入 child transcript。“有了 child ledger”不等于“回到了某个历史 workspace”：这些路径都不快照或回滚 workspace 文件、Git working tree/index、进程、网络请求、项目 semantic memory 或其他外部系统状态。

## TUI Esc backtrack V1

backtrack 是一个 idle-only、source-preserving 的历史分支入口，不是 destructive rewind。它与
`/fork` 共用 `Session.Fork` / `ThreadStore.ForkThread` 的 ledger 合同，但提供更细的 prompt 边界；首个 prompt 的 boundary 由显式 `Session.ForkBeforeFirstTurn` / `ThreadStore.ForkThreadBeforeFirstTurn` 表示：

1. idle 且 composer 为空时第一次 `Esc` 进入 armed 状态；第二次 `Esc` 从 source ledger 加载历史 prompt selector。backtrack requires an empty composer；普通非空草稿按 `Esc` 保持不变，既不 arm 也不打开 selector，slash menu 仍先由 `Esc` 关闭。
2. selector 列出所有可见的 committed user prompt，包括首个 prompt。首个 prompt 是合法
   backtrack target，不需要把普通 fork 的空 `lastTurnID` 改解释为空 prefix。
3. `Up` / `Down` 或 `j` / `k` 移动选择，`Enter` 在选中 prompt 之前发布 source-preserving child。
   选中首个 prompt 时走显式 before-first fork，child 只含 `thread.created` 且没有 turn group；
   选中后续 prompt 时，child 接收选中 prompt 之前的 committed prefix。source ledger 保持不变。
4. fork 成功后切换 child，并把选中的 prompt 回填到 composer；该 prompt 不会预写入 child
   transcript，用户可以先编辑再提交。
5. fork 失败时 source 仍保持 active，selector 关闭，选中的 prompt 保留在 composer，并显示
   可见错误，方便重试或修改。

busy、compacting、pending approval 或 side question in-flight 时拒绝 backtrack；busy 时的
`Esc` 仍是中断当前 turn，approval 中的 `Esc` 仍是 deny，slash menu 中的 `Esc` 仍是关闭菜单。
backtrack 不恢复或回滚 workspace 文件、Git working tree/index、进程、网络请求、provider usage、
权限、semantic memory 或其他外部状态，因此它不是 OpenCode 的 Git undo/redo，也不是 Gemini CLI
的 shadow-Git checkpoint/restore。

## 目录布局

默认根目录为 `~/.eino-assistant`，也可用 `storage.data_dir` 覆盖：

```text
<data_dir>/
  sessions/
    <session-id>/
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

`session-id` 为 UTC 时间排序 ID：`YYYYMMDD-HHMMSS-<6 hex>`。只允许字母、数字、`-` 和 `_`，避免路径穿越。

| 文件 | 角色 |
| --- | --- |
| `journal.jsonl` | 权威 append-only 事件链；恢复时优先重放它。 |
| `state.json` | revision、活动 checkpoint、任务图投影、自动压缩熔断和 meta 的物化投影。 |
| `meta.json` | 列表和 CLI 展示用投影：标题、时间、消息数、API usage、最近 context 快照、本地费用估算，以及 source-preserving child 的 parent/boundary/hash provenance。 |
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
| `thread.created` | 创建 thread，包含 meta 和 system prompt；source-preserving child 还包含 parent、boundary turn 和 source hash provenance。 |
| `turn.started` | 接受一个 user 输入、开始 agent 生命周期。 |
| `tool.started` / `tool.completed` | 以稳定 tool call ID 保存工具参数、结束状态和 artifact 引用；同名并发调用不会混淆。 |
| `turn.committed` | 原子写入完整可见 user/assistant 消息。 |
| `usage.recorded` | 记录一个已完成模型调用的服务商 usage、调用类型和可用性；调用 ID 幂等，ReAct 中间调用与 compaction 调用均单独记账。 |
| `task.state.updated` | 任务图、节点状态、accepted proof 引用和控制状态的紧凑投影；成功状态转换先持久化再发布，写入失败也不得把内存放宽为可交付。`task_complete` 要等所属 `turn.committed` 才是最终批准；恢复会比对其后的 shell/patch 生命周期。完整 tool output 仍只由 `tool.completed` / artifact 保存。 |
| `context.compaction.started` | 在 compactor provider 调用前记录 operation ID；直到成功或失败事件到达前，该操作保持 pending，防止 crash/retry 重复计费。 |
| `turn.cancelled` / `turn.failed` | 保留未提交 turn 的终止原因，不将其送回后续模型。 |
| `context.compacted` | 安装一个活动 checkpoint，并记录父 checkpoint、仅本次新增的直接来源、来源 hash、token 前后值和熔断状态。 |
| `context.compaction.failed` | 已开始的 compaction 未能安全安装 checkpoint；保留活动 checkpoint、raw transcript 与完整 lineage，并记录失败/取消、operation ID 和自动暂停状态。 |
| `context.checkpoint.reset` | resume 发现活动 v1 checkpoint 后清除活动指针；旧 checkpoint 事件/文件及 raw history 保留，用 raw ledger 重建 Prompt View。 |
| `title.changed` | revisioned 标题变更。 |

## API usage、context 与费用

会话 API usage 只使用服务商实际返回的 usage。每个完成的模型请求都作为独立 `usage.recorded` 事件累加，因此它包含一个 user turn 内的所有 ReAct 调用，以及无工具 checkpoint compaction 调用；它不能从最后一条 assistant 消息反推。

一次 compaction 在开始前生成 operation ID。该 ID 关联其所有 compactor `usage.recorded`、成功的 `context.compacted` 或失败的 `context.compaction.failed`，供 `/context` 聚合显示。`cached_tokens` 只表示 provider 实际报告的 cache-read input tokens；本地 planner 的 token 估算、hash 或 artifact 命中都不能推断为 provider cache 命中。

`meta.json` 的 usage 状态为 `exact` 或 `incomplete`：`exact` 表示每次已完成调用均返回 usage；任一调用未返回 usage 即为 `incomplete`，不会以本地估算补齐。没有逐调用 `usage.recorded` 事件的旧会话在展示层标为 `unavailable`，旧累计值不再作为 API usage 使用。调用 ID 去重使重放、重试和恢复不会重复累计。

最近 context 快照是最后一次主对话模型请求的真实 prompt token 数，独立于会话累计 API usage。没有服务商 usage、首次请求前或成功 compaction 后均为未知。planner 的 token 数仅用于本地热窗口裁剪和自动压缩，不会写入 API usage 或费用。

`cost~` 由本地 `model.pricing.input_per_million` / `output_per_million` 配置推导，延后到展示时舍入；它是便于比较的配置费率估算，并非服务商账单。

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
- 新 session 先在 `sessions/.creating-*` 临时目录写入并 fsync 首个 `thread.created`，再原子发布到最终 ID；未发布临时目录不参与列表。
- 已写入但没有被 journal 引用的 artifact/checkpoint 是 orphan，不会成为活动上下文。当前运行时不执行 TTL、自动 GC、加密或 secret scanning；既有保留行为保持不变。
- journal 已有 checkpoint 事件而 checkpoint 文件缺失时，用事件 payload 重建文件。
- 每个 v2 checkpoint 的 direct event IDs 与 source hash 都会重新从 immutable raw turn/artifact ledger 计算；payload 与 checkpoint metadata 彼此自洽但不对应 raw ledger 时，resume 显式失败，不会用它遮蔽 raw group。
- 普通 `resume` 发现仅有 `turn.started` 而没有 terminal 事件时会拒绝接管，避免误伤暂时安静的其他进程；只有用户确认旧进程已退出后显式使用 `resume <id> --recover`，才会以已读取的 revision CAS 写入 `turn.failed`。若期间有其他 writer 更新 revision，则恢复显式失败，不会终止更新后的 turn。
- 对已写入 `context.compaction.started` 但没有 terminal event 的 compaction，普通 resume 同样拒绝接管；`resume <id> --recover` 将其记为 cancelled 并保留已记录的 provider usage。自动 compaction 在 pending 期间保持暂停，避免 crash 后重复花费。
- 末尾 torn journal record 会被恢复到最后一个完整、哈希有效的事件；中间记录、序号、hash 或 thread ID 不一致会显式报 `ErrJournalCorrupt`，不静默丢历史。
- 多 writer 使用 advisory file lock 和 revision CAS。CAS 失败返回 `ErrRevisionConflict`；compactor 丢弃过期候选，而不是覆盖新 turn。

## 工具 artifact

工具回调先把完整参数/结果送到 session recorder，再让任务控制器记录观察；TUI 仅在 `formatToolCard` 渲染时限制字符和行数。任务完成或中断后才到达、且可能已修改工作区的工具结果会要求创建新计划，不能复用旧 proof。

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
model.context.window_tokens              = 32000
model.context.max_output_tokens          = 4096
usable_input_budget                      = 27904
auto_compact_trigger                     = 75% of usable budget
post_compact_target                      = 45% of usable budget
summary_max_tokens                       = 2048
keep_recent_turns                        = 12
low_gain_threshold                       = 15%
max_low_gain_attempts                    = 2
```

可用输入预算始终为 `model.context.window_tokens - model.context.max_output_tokens`。

## Checkpoint 与 compaction

`/compact [focus]` 在 idle 的稳定 turn 边界运行。自动 compaction 在一个 turn 成功提交后、下一个 FIFO follow-up 开始前运行；它们都使用同一个配置模型的**原始无工具** `BaseChatModel.Generate`，绝不经过 ReAct agent。

checkpoint 必须是严格 JSON，并包含：

- `schema_version: 2`；
- `task_goal`；
- `constraints`、`confirmed_facts`；
- `decisions` 和理由；
- `attempts_and_results`；
- `files_or_artifacts`；
- `open_questions`、`next_actions`；
- `provenance.direct_source`：有序且有界（最多 32 个）的 event anchors、`from`、`to` 与仅本次 direct raw groups 的 `content_hash`；
- 可空的 `provenance.parent`：上一持久 checkpoint 的 ID、store hash 与 lineage hash；根 checkpoint 的 parent 为 `null`；
- `provenance.lineage_hash`：将 direct source hash 与精确 parent binding 绑定；
- 每项 claim 的 typed `source_refs`：`kind: "event"` 仅可引用本次 compactor 输入中实际可见、并属于 cold direct manifest 的 event；原始 source group 暴露其完整 direct IDs，递归 merge 仅暴露 child checkpoint 的 anchors 与 child 已序列化的 event refs。`kind: "checkpoint"` 仅可引用当前 parent checkpoint；
- 每条高风险结论的 `observed`、`inferred` 或 `unknown` 置信度。

生成候选前会冻结 `{base revision, active checkpoint ID, direct source refs/hash, parent binding, focus}`。候选通过 schema/provenance 校验、`model.context.summary_max_tokens` 上限和活动 prompt 可安装性检查后才尝试 `context.compacted` CAS；中间有新 turn 或新 checkpoint 时结果被丢弃并报告过期。加载活动 checkpoint 时，store hash 会注入其非模型可见的 `StorageHash`，使下一个 checkpoint 只能绑定到真实持久父项。

完整来源不写入热 checkpoint JSON：每个 `context.compacted` 只持久化**本次新覆盖**的完整 direct event ID，并以 `ParentID` 链接先前 checkpoint。恢复或规划时沿 lineage 展开全部 direct ID，以决定哪些 raw group 已覆盖；因此来源仍可审计，而第 N 次 compact 的 payload 不会复制前 N-1 次来源或线性膨胀。

当 source 超出 compactor 输入预算时，只能在完整 group 边界分块：先生成 source-linked 中间 checkpoint，再递归 merge。最终 checkpoint 恢复根 direct source 的有界证据锚点和 parent binding，完整覆盖范围仍由冷路径 lineage 保存，而不只引用中间摘要。若一次 merge-of-checkpoints 自身仍放不进 provider 预算，显式失败而不做第二层无法验证的 merge。provider 错误、非 JSON、schema/provenance 不匹配、意外 tool call、超预算、低收益、递归上限、取消或不可安装候选都绝不安装 fallback checkpoint。

`keep_recent_turns` 是优先保留的热窗口，而非硬性禁止压缩线。若某个最近完整 group 已因预算被 planner 标记为省略，它会升级为 compaction candidate，避免“模型看不到、压缩器也拒绝处理”的静默丢失。

一次 compaction operation 已开始后若不能安全安装候选，追加终态事件；它绝不改写活动 checkpoint 或 raw history。

自动 compaction 的熔断规则：

| 结果 | 行为 |
| --- | --- |
| 连续低收益（`low_gain`） | store 在写锁内根据当前 streak 计算绝对 `resulting_low_gain_streak` 与是否 pause；达到 `max_low_gain_attempts`（默认 2，TOML 中 `0` 表示默认）时置 `auto_compaction_paused=true`；未达阈值则不 pause |
| 硬失败（provider/schema/size/install/取消等） | 立即 pause，并将 `resulting_low_gain_streak` 置 0 |
| 过期 CAS（`cancelled/stale`） | 关闭已开始的 operation（保留已记录 usage），**不** pause、不改 streak，也不立即重试；下一次稳定 turn 边界再重新规划 |

成功安装的 checkpoint **始终**清零 `low_gain_streak`；checkpoint 上的历史 `low_gain` 字段不再参与投影。

手动失败保留既有 pause 状态；后续成功的手动 `/compact` 写入成功 checkpoint 并清除 pause。内部递归深度使用固定安全上限，与 `max_low_gain_attempts` 无关。`/context` 显示最后 outcome、reason、pause reason、low-gain streak 和 operation usage。

活动 v1 checkpoint 与 v2 claim/provenance 语义不兼容。`resume` 仅在可识别的 `schema_version: 1` 时 CAS 追加 `context.checkpoint.reset`，清空活动指针、重置低收益状态并从 raw ledger 重建 Prompt View；原 checkpoint 文件/事件和 raw artifacts 不删除。损坏 JSON、未知版本或无效 v2 checkpoint 仍是显式错误，绝不静默 reset。

## Resume、分页与命令语义

| 操作 | 行为 |
| --- | --- |
| `resume <id> [--recover]` / `/resume <id> [--recover]` / `exec resume <id> [PROMPT] [--recover]` / `exec resume --last [PROMPT] [--recover]` | **存储层**：读取 state、活动 checkpoint 和最近可见 transcript。**模型层**：下一请求使用活动 checkpoint + 未覆盖热 groups + 当前 tools + 新消息，不把完整账本水合进 prompt。产品承诺是任务可继续，不是全文语义等价。headless `exec resume` 必须给精确、不透明 ID，除非显式使用 `--last`；`--ephemeral` 会把选定 source session 快照到临时 ledger，后续只由该临时 ledger 接收 turn lifecycle 写入。它只发送调用中提供的一条新 prompt，并且只通过 `chat.OpenSession` 恢复，不解析账本文件。若活动 checkpoint 是可识别 v1，先 CAS reset 指针并保留 raw ledger；若 thread 有活动 turn 或 pending compaction，普通 resume 仍拒绝接管；精确指定 `--recover` 才会显式恢复该目标，而且 ephemeral 恢复只写临时 ledger。`--output-format stream-json` 不读取或导出账本：它只在成功打开后写 session.started，工具 activity 也只在对应生命周期已经写入账本后投影，最终 result 则在 turn.committed 之后写出。 |
| 向上滚动 | 以稳定 message page 从 event ledger 读取更早 transcript；只扩展 UI 回放缓存，不改变模型工作集。 |
| `/new [title]` | 创建独立 thread，旧 thread 保留。 |
| `ThreadStore.ForkThread(...)`（内部 V1） | 在 `internal/store` 发布带新 ID 的 source-preserving child ledger：空 `lastTurnID` 选择 latest，非空值复制完整 committed event prefix，重建 journal hash/seq、记录 parent provenance 并复制前缀 artifacts；拒绝 active/pending compaction/checkpoint/task-derived 状态，不回滚 source/workspace/git/外部副作用。显式 before-first 边界由 `ForkThreadBeforeFirstTurn` 单独提供。 |
| `Session.Fork(...)` / `/fork` | `chat` 打开继承 source frozen system prompt 与配置的 child；TUI `/fork` 仅 idle、无参数，自动使用最新完整 committed turn。`Session.ForkBeforeFirstTurn` 打开只含 `thread.created`、无 turn groups 的 child；child 打开成功后才切换 active 并清理旧 queue/side/tool/task UI。 |
| idle 两阶段 `Esc` backtrack | 加载所有可见 committed user prompt 的历史 prompt selector，包括首个 prompt；确认后在选中 prompt 前创建 child，首个走显式 before-first 边界，后续复制 committed prefix，成功后回填 composer 而不写入 child transcript；busy、compacting、pending approval 和 side question in-flight 仍按 V1 边界拒绝。 |
| `/clear` | 与 `/new` 相同地创建并切换到新 thread，同时清空旧队列；不重写旧 thread。 |
| `/compact [focus]` | 创建或合并派生 checkpoint；raw turn/artifact 不删除。 |
| `/context` | 展示预算、checkpoint、热/省略 group、planner fallback、最后 compaction outcome/reason、low-gain streak、自动暂停原因及 operation 关联的 provider usage；cache-read 仅在 provider 明确报告时显示。 |
| `/queue clear` | 即刻删除尚未开始的 follow-up，不取消当前 turn 或 compact。 |
| `/delete <id>` | 唯一物理删除原始事件、checkpoint 和 artifact 的命令；不能删除当前 thread。 |

生成或 compaction 中，`/help`、`/context`、`/status`、`/sessions`、`/queue` 立即运行。`/compact`、`/clear`、`/new`、`/resume`、`/fork`、`/title`、`/delete`、`/exit` 不会被排队，因此 queued prompt 不会在另一个 thread 或 checkpoint 上执行；`/fork` 只在 idle 且无参数时执行。

## 相关代码

| 路径 | 职责 |
| --- | --- |
| `internal/store/thread*.go` | 账本、锁、CAS、artifact 范围读取、checkpoint、恢复和 transcript page。 |
| `internal/store/thread_fork.go` | V1 source-preserving child ledger：边界校验、journal envelope/hash/seq 重建、parent provenance、前缀 artifact 复制和原子发布；不承担产品命令或 workspace rollback。 |
| `internal/contextbuild/` | 完整 group planner、结构化 checkpoint、无工具 compactor、递归合并。 |
| `internal/chat/session.go` | 生命周期 recorder、prompt projection、compaction transaction、resume tail，以及 `Session.Fork` child session 打开。 |
| `internal/agent/react.go` | 发送未截断的工具事件；不承担显示限制。 |
| `internal/tui/` | `/compact`、`/context`、`/fork`、Esc backtrack、自动压缩 barrier、队列隔离、session 切换和分页回放。 |
| `cmd/eino-assistant/run_tui.go` | 共享 base model，并分别创建 ReAct model 与 no-tools compactor。 |

`model.context` 的压缩策略数值设为 `0` 时采用产品默认值，并非禁用开关。例如 `keep_recent_turns: 0` 使用默认 12，`low_gain_threshold_percent: 0` 使用默认 15%，`max_low_gain_attempts: 0` 使用默认 2。窗口与输出上限必须明确配置。

system prompt 在 `thread.created` 时写入并冻结；resume / compact 不会从磁盘重读项目规则。tools 与权限策略跟随**当前进程配置**。headless `exec resume` 也不会继承此前进程的 session allow/deny 决策，且没有交互 approver；需要审批的工具调用仍由当前进程的权限策略 fail closed。
