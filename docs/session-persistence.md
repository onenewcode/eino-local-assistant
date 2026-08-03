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
| `meta.json` | 列表和 CLI 展示用投影：标题、时间、消息数、API usage、最近 context 快照和本地费用估算。 |
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
| `resume <id> [--recover]` / `/resume <id> [--recover]` | **存储层**：读取 state、活动 checkpoint 和最近可见 transcript。**模型层**：下一请求使用活动 checkpoint + 未覆盖热 groups + 当前 tools + 新消息，不把完整账本水合进 prompt。产品承诺是任务可继续，不是全文语义等价。若活动 checkpoint 是可识别 v1，先 CAS reset 指针并保留 raw ledger；若 thread 有活动 turn 或 pending compaction，普通 resume 仍拒绝接管；精确指定 `--recover` 才会显式恢复该目标。 |
| 向上滚动 | 以稳定 message page 从 event ledger 读取更早 transcript；只扩展 UI 回放缓存，不改变模型工作集。 |
| `/new [title]` | 创建独立 thread，旧 thread 保留。 |
| `/clear` | 与 `/new` 相同地创建并切换到新 thread，同时清空旧队列；不重写旧 thread。 |
| `/compact [focus]` | 创建或合并派生 checkpoint；raw turn/artifact 不删除。 |
| `/context` | 展示预算、checkpoint、热/省略 group、planner fallback、最后 compaction outcome/reason、low-gain streak、自动暂停原因及 operation 关联的 provider usage；cache-read 仅在 provider 明确报告时显示。 |
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

`model.context` 的压缩策略数值设为 `0` 时采用产品默认值，并非禁用开关。例如 `keep_recent_turns: 0` 使用默认 12，`low_gain_threshold_percent: 0` 使用默认 15%，`max_low_gain_attempts: 0` 使用默认 2。窗口与输出上限必须明确配置。

system prompt 在 `thread.created` 时写入并冻结；resume / compact 不会从磁盘重读项目规则。tools 与权限策略跟随**当前进程配置**。
