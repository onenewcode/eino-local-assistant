# Agent CLI 会话存储：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-07。产品和实现会演进，采用前应重新核验。
>
> 范围：本地 agent CLI 如何持久化可恢复会话、会话元数据与摘要，并如何为会话列表建立查询视图；包括历史远超模型窗口时的上下文恢复。
>
> 不在范围：未公开的云端同步、各产品的私有数据库 schema、上下文压缩提示词，以及任何本仓库的实现映射。

## 1. 结论

- **跨产品综合：** “摘要是结构化数据”并不推出它应在 JSON 或 SQLite 中。固定、很小且只随会话写入的 metadata 可以在 JSONL 内以首记录或 metadata-update event 表示；需要按多维条件列举、搜索、排序或跨会话关联时，SQLite 更合适。
- **Codex 开源实现快照：** 会话的 canonical rollout 是单个 JSONL；SQLite 读取/回填 rollout 的 metadata 以支持 thread 列表和查询。源码没有显示每个 rollout 旁另写一个同义 `summary.json`。
- **Gemini CLI 开源实现快照：** 一份 session JSONL 同时保存初始 metadata、消息和 `$set` metadata updates；模型生成的一行会话摘要也通过该 `$set` 记录持久化。它不用同义摘要 sidecar 换取快速列举。
- **OpenCode 开源实现快照：** SQLite 可以本身就是 durable event store，并在同一事务内维护 `SessionTable`、message 等 projection。此模式用 schema/migration 和数据库恢复能力交换掉人工可读的逐会话 JSONL。
- **跨产品综合：** 常见且自洽的组合是二选一：`canonical JSONL + 可重建 SQLite index`，或 `SQLite event store + relational projections`。若一份小 JSON 文件既不是 canonical source、也不被读作索引，它只是额外的 cache，不是必要层。

## 2. 已公开的产品与实现证据

### Codex：JSONL rollout 为会话记录，SQLite 为可回填的本地状态

**开源实现快照，非长期产品合同。** `RolloutRecorder` 的模块注释明确说它将 session rollout 持久化为 `.jsonl`，供 replay 或检查；公开示例直接对 rollout JSONL 使用 `jq`。同一实现将 session 的 canonical rollout items 写入该记录。
[Codex rollout recorder](https://github.com/openai/codex/blob/7257826ab22812701fec20dc0cc0eb51c5577d42/codex-rs/rollout/src/recorder.rs)（快照提交 `7257826`；访问于 2026-08-07）。

**开源实现快照，非长期产品合同。** Codex 的 thread-list code 从 rollout 的开头读取有限条 JSONL records，以获得 `SessionMeta` 与列表预览；因此会话 metadata 在 JSONL 内可被提取，不需要一个每会话 metadata sidecar。
[Codex rollout list](https://github.com/openai/codex/blob/7257826ab22812701fec20dc0cc0eb51c5577d42/codex-rs/rollout/src/list.rs)（快照提交 `7257826`；访问于 2026-08-07）。

**开源实现快照，非长期产品合同。** Codex 启动 SQLite state runtime 后扫描 sessions/archived sessions，提取 rollout metadata 并 upsert；源码将这一步称为 backfill。SQLite 里也有少数明确标注为 SQLite-only 的分页 metadata。CLI 的本地数据库损坏提示说明它可从 saved data 重建。
[Codex state-db backfill](https://github.com/openai/codex/blob/7257826ab22812701fec20dc0cc0eb51c5577d42/codex-rs/rollout/src/state_db.rs) 与 [recovery handling](https://github.com/openai/codex/blob/7257826ab22812701fec20dc0cc0eb51c5577d42/codex-rs/cli/src/state_db_recovery.rs)（快照提交 `7257826`；访问于 2026-08-07）。

### Gemini CLI：单个 JSONL 兼作记录与 metadata 容器

**开源实现快照，非长期产品合同。** Gemini CLI 新建会话时先向 `.jsonl` append 初始 metadata；之后通过逐行 append 写入消息和 `$set` metadata update。其 session `summary` 经由 `saveSummary` 调用相同的 `updateMetadata`，即写成 JSONL 的 `$set` 行。会话读取器顺序合并这些 records，支持 `metadataOnly` 读取以用于列表。
[Gemini CLI chat recording service](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/packages/core/src/services/chatRecordingService.ts)（快照提交 `d5c9a97`；访问于 2026-08-07）。

**开源实现快照，非长期产品合同。** Gemini CLI 的摘要服务只产生有界的一行用户意图文本；它并不表明摘要是独立的权威会话状态。摘要写入位置由 chat recording service 决定。
[Gemini CLI session summary service](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/packages/core/src/services/sessionSummaryService.ts)（快照提交 `d5c9a97`；访问于 2026-08-07）。

### OpenCode：SQLite 同时承载 durable events 与查询 projection

**开源实现快照，非长期产品合同。** OpenCode 的 database layer 使用本地 SQLite，开启 WAL、foreign keys 和 migration；数据库路径是其 data directory 下的 `opencode.db`（或 channel-specific database）。
[OpenCode database layer](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/core/src/database/database.ts)（快照提交 `69f2cba`；访问于 2026-08-07）。

**开源实现快照，非长期产品合同。** OpenCode 的 EventV2 将 durable event 以 aggregate sequence 写入 `EventTable`，并在同一数据库 transaction 运行 projectors；session projector 维护 `SessionTable`、消息和 parts 等可查询表。它证明 SQLite 并不只能当 cache，也可作为权威 event log 与 projection 的事务边界。
[OpenCode durable events](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/core/src/event.ts) 与 [session projector](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/core/src/session/projector.ts)（快照提交 `69f2cba`；访问于 2026-08-07）。

## 3. 机制与取舍

| 组合 | 适合的数据 | 优点 | 主要取舍 |
| --- | --- | --- | --- |
| 单一 JSONL | 完整顺序事件、少量 per-session metadata | 可审计、便携、故障后可逐条重放 | 全局筛选与检索需要扫描，或另建 index |
| JSONL + SQLite index | JSONL 为真相；列表、搜索、排序为 projection | 保存可检查原始记录，同时避免全量扫描 | 必须定义重建、陈旧检测与写入失败后的修复路径 |
| SQLite event store + projections | 有多个实体/关系、强查询需求、事务性 projection | event 与 index 可同事务提交，查询自然 | schema migration、文件可读性与单库损坏恢复需要更强工程投入 |
| JSONL + 同义 summary sidecar | 仅在独立工具/离线扫描确实消费 sidecar 时才成立 | 可给不懂 JSONL 的外部 scanner 一个定长入口 | 多一个一致性边界；若 app 不读它，它没有功能收益 |

## 4. 跨产品综合

**跨产品综合，不是统一产品合同。** “会话摘要”有两种常被混淆的含义：

1. **列表 metadata：** 标题、首条 prompt、更新时间、模型、token 计数等，服务于列举和筛选。
2. **模型上下文摘要：** 为压缩历史而生成的文本/结构化 checkpoint，服务于下一次 prompt 组装。

两者都可以是结构化数据，却有不同的读取者与生命周期。第 1 类常被写在 JSONL 的 initial/update record 或 SQLite index；第 2 类应随会话的 history/event 版本化，以便 resume 后能知道它覆盖的输入范围。将二者放进一个未被读取的静态 sidecar，既不能替代数据库索引，也不自动构成有效的 context checkpoint。

从三个实现可得到的较窄结论是：产品会保留“单一权威写入路径”，再视查询需要选择 inline metadata、可重建 SQLite index 或 SQLite 内事务化 projection。没有证据表明“每个 JSONL 会话旁必须有一个 `summary.json`”是主流惯例。

## 5. 风险与证据缺口

- **证据缺口：** Claude Code 的公开文档足以说明 resume/history 产品行为，但本次没有取得可核验的一手公开资料来声明其本地 session 文件的精确 schema；不应凭目录截图或第三方脚本推断其内部格式。
- **综合风险：** 只写 sidecar 却不读取，会让用户以为有两个同等权威来源；读者很容易在它与 JSONL 内容不一致时选择错误的那份。
- **综合风险：** 若 SQLite 是 index，应把“可删除、可从 canonical log 重建”作为明确合同，并在打开时验证新鲜度或按需修复；否则它会演变为未定义的第二真相。
- **综合风险：** 若 SQLite 改为 canonical store，必须把 event append、projection 更新、并发/ownership 和 migration 全部放入明确事务；仅把 JSON summary 换成 SQLite row 不能解决双写一致性。

## 6. 超长会话与恢复

- **Codex 开源实现快照：** 恢复会从最近有效 compaction 的 replacement history 开始，只读取其后的 surviving suffix；旧 transcript 不作为第二份上下文重放。[rollout reconstruction](https://github.com/openai/codex/blob/7257826ab22812701fec20dc0cc0eb51c5577d42/codex-rs/core/src/session/rollout_reconstruction.rs#L112-L186)
- **Gemini CLI 开源实现快照：** 压缩明确切分可压缩 prefix 与保留 tail，新的工作 history 是 summary 加 tail；tail 不交给摘要器，因此没有结构性重复。[chat compression](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/packages/core/src/context/chatCompressionService.ts#L323-L480)
- **OpenCode 开源实现快照：** history 查询以最新 compaction 的 durable sequence 为下界，避免重新加载该 checkpoint 之前的消息。[session history](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/core/src/session/history.ts#L13-L99)
- **Claude Code 产品文档：** 当上下文接近上限时先清理旧工具输出，再压缩对话；单个超大输入反复触发压缩时会停止自动压缩，而不是无限循环。[context window](https://code.claude.com/docs/en/how-claude-code-works#when-context-fills-up)

**跨产品综合：** 完整 JSONL 是审计、展示、fork 和重新压缩的证据，不等于下一次模型请求。可靠的恢复边界是 `fresh static context + latest verified checkpoint + exact uncovered tail + current input`；检查点必须记录它替换的来源范围，且压缩器不能同时看到新的保留 tail。若没有一个能让剩余 tail 落进窗口的有效 checkpoint，产品应停止并要求显式压缩，而非静默截断或自动产生新的模型费用。

## References

- OpenAI Codex rollout recorder, list, state-db and recovery source snapshots，访问于 2026-08-07。
- Google Gemini CLI chat recording and session summary source snapshots，访问于 2026-08-07。
- OpenCode database, durable event and session projector source snapshots，访问于 2026-08-07。
