# 持久化记忆系统：行业实践

> 状态：研究笔记，不是实施计划。
>
> 调研日期：2026-07-31。CLI、API、默认开关和数据保留策略变化很快；采用任一结论前应重新核验引用。
>
> 范围：Agent 与对话产品的**跨会话语义记忆**——持久指令/规则、按用户自动学习与巩固、按需检索的长期事实/偏好、纠正与冲突、删除与安全边界。
>
> 不在范围：
>
> - **会话恢复**（`resume` / `--continue`、transcript 回放、checkpoint、compact carrier、thread 工作状态续接）
> - 本仓库当前实现、具体存储选型、模型提示词细节、向量数据库性能基准，以及任何本仓库的迁移计划
>
> 会话恢复与长期记忆常被混称“持久化”，但二者生命周期与治理完全不同；下文仅在边界处点明差异，不展开恢复机制本身。

## 1. 总结

- **推断**：跨会话“记忆”至少应分成三层——每轮装载的持久指令、按需召回的长期事实/偏好、以及作为抽取来源但不默认进 prompt 的原始证据。若把它们合并为一个向量库或一份聊天历史，就会失去各自的写入权限、有效期和删除边界。[1][2][3][4]
- **观察 + 推断**：成熟编码 Agent 已把**规则**与**自动学习**拆开：Claude Code 区分 `CLAUDE.md` 与 auto memory；Codex 区分 `AGENTS.md` 与默认关闭、异步生成的 local memories。规则是人工/团队维护的稳定约束；自动记忆是从对话归纳、可关闭、可审阅的候选知识。[1][11][15]
- **推断**：自动写入需要单独看待。用户明确要求“记住”可在热路径处理；从多轮对话提取、合并和去重更适合后台任务。后者降低交互延迟并便于治理，但需要显示写入状态并能恢复失败。[1][2][4]
- **推断**：高价值记忆宜带有最小治理元数据：归属/作用域、来源事件、观察时间、有效期、置信度、版本及被替代关系。检索先做权限和时间过滤，再用关键词、向量或图排序；相似度不是授权，也不是事实正确性的证明。[3][4][7]
- **推断**：安全和遗忘是设计的一等约束：外部文件、工具输出和历史消息都可能携带间接提示注入；删除一条“记忆”也未必删除索引、摘要、巩固产物或缓存。持久记忆必须验证“删除后不可召回/不可注入”，不能只验证 UI 不再显示。[8][9][16]
- **观察 + 推断**：面向用户的自动记忆正在从“离散 saved memories”转向“持续更新的用户画像/历史综合”。ChatGPT 公开提供自动更新的 memory summary、响应级来源解释和就地纠正；Gemini 从账户历史聊天中个性化，并允许用户直接在对话中纠正。两者都明确暴露一个边界：**纠正当前认识**与**彻底删除所有来源**是两种操作，后者必须覆盖聊天、文件、摘要和连接应用等来源。[32][33]
- **推断**：按用户自动化的第一控制点不是 embedding，而是可信身份作用域。`tenant/user/project/agent` 必须由认证与运行时传入，模型只能决定候选内容，不能决定记忆属于谁；纠正应生成新版本并使旧版本失效，避免把历史证据静默改写掉。[3][4][7][32][33]

## 2. 问题边界

### 2.1 什么是本笔记中的“持久化记忆”

| 层 | 保存的对象 | 典型目的 | 不等于 |
| --- | --- | --- | --- |
| 持久指令 | 组织/用户/项目规则、工具约束、流程（如 `AGENTS.md`、`CLAUDE.md`、Continue rules） | 每次启动给出稳定约束 | 强制安全控制；仍需 hooks/权限/校验 |
| 学习型长期记忆 | 偏好、已确认事实、稳定经验、项目洞见 | 跨 thread / session 的按需帮助 | 完整 transcript；也不是“会话还能接着聊” |
| 检索索引 | embedding、关键词、知识图、摘要索引 | 找到候选原文或事实 | 权威来源、访问控制或删除本身 |
| 证据（只作来源） | 原始消息、工具结果、文件/网页快照 | 审计与重新抽取/核验 | 每轮默认进入模型上下文 |

**明确排除（仅作对照，本笔记不展开）：**

| 常被误称为“记忆”的层 | 实际用途 | 与本主题的关系 |
| --- | --- | --- |
| 原始事件/transcript | 审计、重放 | 可为自动学习提供输入，本身不是语义记忆库 |
| 线程工作状态 / resume / compaction | 同一任务续接 | **会话恢复问题**，不是跨任务知识管理 |

例如，Codex 将 rollout 存为 durable replay format、Claude/Aider 保存 session 文件——这些说明“对话可重放”，**不能**据此推断产品已具备冲突处理、时效与可删除的用户知识库。[5][10][12] LangGraph 也把 thread-scoped checkpoint 与 cross-thread Store 分成两套机制；本笔记只关心后者一类能力。[3][4]

### 2.2 本笔记中“可靠”的含义

可靠不表示模型永远准确，而是系统能回答这些操作性问题：

- 这条信息由谁、何时、从哪段原始证据得出，适用于谁和什么范围？
- 新事实与旧事实冲突时，是覆盖、并存、失效还是要求人工确认？
- 当前 prompt 为什么收到了它；它是否仍在授权范围和有效时间内？
- 用户查看、修正、导出或删除后，派生摘要、索引、缓存和后台巩固任务会怎样变化？
- 自动抽取失败、重复写入、并发巩固之后，状态是否仍可解释和修复？

这比“有更多可检索文本”或“上次对话还能打开”更接近持久记忆系统的实际质量边界。

## 3. 行业机制

### 3.1 面向账户的自动记忆：用户画像、来源与纠正

以下是已上线产品公开承诺的用户行为；厂商没有公开完整的抽取模型、排序算法或内部数据表，不能由 UI 反推这些实现。

| 产品 | 自动形成记忆 | 用户如何发现与纠正 | 删除边界 |
| --- | --- | --- | --- |
| ChatGPT | 启用后，从聊天、文件和连接应用中自动记住有用上下文；新的 memory summary 会随对话持续更新。summary 是高层综合，不保证展示系统记住的全部细节。[32] | 用户可在 summary 输入修改要求、选中文本定点纠正，也可从回答下方的来源入口查看某条记忆为何被使用并发起纠正。[32] | 完整删除须删除信息出现的每个来源，包括普通/归档聊天、文件、memory summary，并断开仍含该信息的连接应用。只删除 summary 并关闭 memory 不会删除历史聊天；重新开启后仍可能从旧聊天再次形成记忆。[32] |
| Gemini Apps | 在个人 Google 账户、Memory 与 Keep Activity 开启等条件下，从过去 Gemini 聊天学习用户及其情境；并非所有账户、入口或功能都可用。[33] | 用户可询问某次回答是否用了过去聊天；若认识有误，官方路径是保持 Memory 开启并在聊天中直接纠正。[33] | 要删除某项认识，需删除所有含该信息的历史聊天，生效可能短暂延迟。若来源是连接应用，还要同时删相关聊天并断开应用；应用内原数据更新后，Gemini 体验可能数日后才变化。[33] |

这两种产品形态有一个重要差异。ChatGPT 暴露了一个可编辑的**派生综合视图**，Gemini 的公开控制面更接近**历史来源驱动**；但共同点是账户身份、开关和来源决定可用范围，且“改正当前回答”“更新长期认识”“清除原始来源”没有被当成同一个动作。[32][33]

ChatGPT 还保留 legacy saved memories：用户可明确说“记住”，系统也可能自动保存有未来价值的细节；这些离散项可被自动更新、合并或按要求删除。官方同时说明 saved memories 与聊天历史分开保存，删除聊天并不会自动删除由它形成的 saved memory，删除日志可能为安全和调试保留最多 30 天。[32] 这直接说明持久化系统至少存在原始来源、派生记忆和保留日志三个生命周期。

### 3.2 成熟编码 Agent：规则与自动记忆分别处理

| 产品 | 公开可观察的持久记忆机制 | 重要边界 |
| --- | --- | --- |
| Codex CLI | `AGENTS.md` 从 `CODEX_HOME` 与 project 目录形成层级指导；local memories 默认关闭，启用后异步从合格历史聊天生成 summaries、durable entries 等，存放在 `~/.codex/memories/`，并提供 `/memories`、开关及 external-context 禁用项。当前公开源码还提供 `add_ad_hoc_note`：仅在用户明确要求记住、忘记或更新时写入 append-only note，留待后续 consolidation。[11][15][34] | 历史/线程文件不是语义记忆。自动记忆是后台、可关、有预算与污染隔离的路径，不是“有 transcript 就等于会记住”。ad-hoc note 被明确视为不可信数据而非可执行指令；公开 TUI 提供带二次确认的整体 reset，清 memory 文件、rollout summaries 和 memory rows，但保留既有 thread 及其 memory mode；未公开单条编辑 UI。[11][21][34] |
| Claude Code | auto memory 默认启用，会根据用户纠正与偏好自行写 notes；repo 级 `MEMORY.md` 启动时最多加载前 200 行或 25 KB，主题文件按需读取。机器本地、同仓库 worktree 共享、跨机器/云不共享；可由 `/memory`、设置或环境变量查看、编辑、删除和关闭。带 YAML frontmatter 的 memory 文件在新版写入时还会记录 `modified` 时间。[1] | `CLAUDE.md`（managed/user/project/local）是 context 级指导，不是不可绕过的配置；与 auto memory 分目录、分控制面。官方允许随时删除 plain Markdown memory 文件，并另行记录可恢复的 session transcript，但截至调研日没有公开“一键重置全部 auto memory”的合同，也没有说明删除 memory 文件会删除 transcript。自动判断“值得记住”的精确算法同样未公开。[1][15] |
| Continue | `.continue/rules/*.md` 可用 `alwaysApply`、glob、regex 与描述决定何时注入；覆盖 workspace 与用户级 rules。[6] | 这是规则/指令加载面，而非自动学习型 memory。规则进 system message 时，scope、信任与禁用策略是运行时行为的一部分。[6] |
| Aider | 默认将聊天追加到 `.aider.chat.history.md`；不自动形成跨任务语义事实库。[5][13] | 代表“只持久 transcript、不自动抽取长期事实”的保守路径；与本主题的对照是：有磁盘记录 ≠ 有学习型记忆。[5][14] |

Codex 与 Claude Code 的共同模式：**长期指导（人工维护）** 与 **自动学习（机器归纳、可治理）** 分离。官方还建议对必须执行的团队规则使用 hooks、linters 或 typechecks，而不是只依赖记忆文本。[1][11][15]

### 3.3 框架与专门记忆系统：跨线程事实层

| 系统 | 分层机制 | 可观察的取舍/限制 |
| --- | --- | --- |
| LangGraph Store | 跨 thread 的 key-value facts、preferences 与 shared knowledge；namespace + key，可选 semantic search。[3][4] | Store 的 prefix namespace 非精确匹配、结果超过 `limit` 会静默截断，不同后端排序不同。`put` 语义是 overwrite，不是领域级 merge。[3][4][25] |
| Letta / MemGPT | 常驻 context 的 memory blocks、按工具查询的 archival memory；MemFS 把 memory 写成 git-backed Markdown，`system/` 每轮载入、其余按需读取。[2][17][18] | 每次 MemFS 编辑有 Git commit；background dreaming 可在累计消息后归并经验。版本历史 ≠ 自动解决语义冲突。[2][18] |
| Graphiti | 原始 episodes 是来源；派生实体/关系可带 `valid_at`、`invalid_at`、`expired_at` 与 episode IDs；检索组合语义、关键词与图遍历。[7] | “使旧事实失效而非立即抹除”利于时间查询与审计，却与 erasure 请求有张力；小模型可能不守 JSON schema 导致抽取失败。[7] |
| Mem0 | `add` 时用 top-k 候选辅助抽取，再 hash 去重、向量写入与 history；`search` 融合 semantic / keyword / entity，读路径过滤 expiration。[26] | top-k 是写入时的候选上下文，不是事实验证器；框架提供存取 API，是否注入 prompt 仍是调用方责任。[26] |
| MemGPT 论文 | 有限 context 作主存，更大记忆分层存放，由 agent 主动搬运。[19] | 解释热/冷层动机，不能替代来源、冲突、授权与删除治理。 |

这些材料支持“指令层 / 事实层 / 索引层分离 + 生命周期管理”，并不支持统一内部格式或固定 token 阈值。

### 3.4 公开源码中的实际实现：巩固、检索与失效

本节是**固定公开源码快照的观察**，用于说明主流实现的控制流；它不是稳定 API 合同。

#### Codex local memories：异步两阶段抽取，再以受限 agent 巩固

当前公开代码把写入和读取分成独立路径。记忆流水线仅在 root session 启动、非 ephemeral、功能启用、非 sub-agent 且 state DB 可用时后台启动。[21]

```text
eligible historical idle rollout
  -> Phase 1: DB claim + lease -> filtered response items
  -> strict-schema model extraction -> secret redaction -> stage-1 DB row
  -> Phase 2: one global lock -> selected raw memories -> git-backed workspace diff
  -> consolidation sub-agent (policy derived from parent) -> MEMORY.md / memory_summary.md / skills
  -> read path: bounded summary in developer context + targeted list/search/read tools
```

- **Phase 1（每个 rollout）**：按允许的 interactive source、`memory_mode='enabled'`、排除当前 thread、最大年龄、空闲时长、scan/claim 上限和 lease 领取历史 rollout；以并发上限提取 `raw_memory`、`rollout_summary` 与可选 slug，输出受严格 JSON schema 约束，再做 secret redaction。失败走带退避的重试，而不是热循环。[21][22][29]
- **Phase 2（全局巩固）**：单一全局锁；选择考虑 `usage_count`、时间与 `max_unused_days`；同步到 `$CODEX_HOME/memories/` 下 Git 工作区并生成 diff，有变化才启动 consolidation agent。提交前验证工件与 lease；文件系统基线与 SQLite success 不是单一原子事务。[21][23][29]
- **读取**：feature flag 与 `use_memories` 同时开启时，将 `memory_summary.md` 截断到约 2,500 tokens 注入 developer-policy fragment；更细内容由受限的 `list` / `search` / `read` 按需读取。[24]
- **双层开关与有界工作量**：stable `MemoryTool` 默认关闭；开启后 `generate_memories` / `use_memories` 等默认开启。每次启动默认有限扫描窗口、idle 门槛、rate-limit 余量、Phase 2 条数上限与未使用淘汰；这些是成本/吞吐保护，不是事实质量阈值。[27][29]
- **外部上下文隔离**：若 `disable_on_external_context` 启用，web search、被标为 external/polluting 的工具或 MCP 输出会污染 source thread；stage-1 只接收 `enabled` mode，已纳入 Phase 2 的内容可走遗忘/重整路径。[28]

截至 2026-07-31，Codex 当前公开源码又增加了一条显式用户纠正路径：`add_ad_hoc_note` 的工具描述限定为用户明确要求“remember, forget, or update”时使用；后端以 `create_new` 写入带时间戳的 append-only Markdown note，不覆盖旧 note。consolidation prompt 要求吸收新增或修改内容、不得删除 note，并把 note 当不可信信息而非行动指令；TUI 的 memories 设置则提供 use/generate 开关与带二次确认的“Reset all memories”。reset handler 清 memory database rows 和 memory roots 内容；公开测试验证 memory 文件、rollout summaries 与 rows 被清，原 thread 及其 `enabled` memory mode 保留。[34] 这是一种**追加纠正事件 → 后台重整当前视图**的实现，不等于对旧事实做原地修改或物理擦除。

被使用且较新的 memories 更易进入下一轮 consolidation，长期未用的 stage-1 输出可被 prune——这是**可用性/活跃度信号，不是真值证明**。Phase 1 schema 缺少原文 citation、置信度、冲突与时间有效期字段；secret sanitizer 是 best-effort regex。summary 作为 developer-policy fragment 注入，说明 provenance 与持久化提示注入仍须单独验证。[22][24][29]

#### LangGraph Postgres Store：数据库原子性 ≠ 语义冲突解决

跨 thread item 存为 `(prefix, key, value JSONB, created_at, updated_at)`，embedding 在带外键的 `store_vectors` 表；vector 随 store row 删除级联。[25]

- `put` 为 `INSERT ... ON CONFLICT DO UPDATE`：同一 key 由事务串行化，但语义仍是 overwrite，无 domain merge 或 CAS。[25]
- TTL sweeper 须显式启动；在物理清扫前，若未启用 `omit_expired`，过期记录仍可能被 `get`/`search` 返回。**有效性必须在读路径再次检查**。[25]

#### Mem0：候选召回辅助抽取，而非“向量命中即为记忆”

`Memory.add()` 规范化 scope/metadata，保留近期消息并对新输入做 top-k 候选检索；模型在候选 ID 上下文中抽取事实，再 hash 去重、批量向量写入、SQLite history 与 entity link。[26]

- top-k 是写入时的候选上下文，不是事实验证器；hash 去重不能裁决语义冲突。[26]
- `search()` 供调用方取候选，不自动塞入 prompt；要求至少一种 scope filter；读时过滤 expiration。[26]
- `update` 重 embedding 并记历史；`delete` 删 vector、写 tombstone 并解除 entity link。expiration 更接近“搜索时隐藏”，不保证物理擦除。[26]

#### Letta MemFS：Git 提供审计与同步边界，不提供事实事务

每个 agent 的 memory 在 Git working tree 中；受控 tool 要求 clean 工作区，变更后 stage/commit；同步优先 fast-forward，冲突可暴露给调用方。[30]

- commit SHA 是 revision 边界，不是字段级 CAS 或语义 merge。[30]
- `memory delete` 是 working tree 删除加 commit，不会在该路径上重写 Git history、远端备份或外部索引；逻辑删除 ≠ 合规彻底遗忘。[30]
- memory blocks 作为 editable system-prompt segments；`read_only` 只限制工具写入，不构成 provenance 或 taint 策略。[30]

#### Graphiti：时间字段表示事实失效，而不是立即删除

`EntityEdge` 关联 source episodes，并保存 `valid_at`、`invalid_at`、`expired_at` 等，适合 current-vs-historical 查询。[31] 这些是**时间语义**而非删除协议；temporal invalidation 与 privacy erasure 必须作为两套独立可测试的生命周期。[31]

## 4. 高效且合理的通用模式

以下结论是对上述资料的**设计推断**，不是某个供应商承诺的行为。

### 4.1 记忆宫殿：带地址、权限和失效语义的状态空间

这里的“记忆宫殿”不是无限大的向量库，而是让每条信息有固定**房间**、明确**进入条件**、有限**读取路径**和可验证**退出/失效**条件的状态模型。

| 房间 | 放什么 | 谁可写 | 何时读 | 有效性边界 |
| --- | --- | --- | --- | --- |
| 宪章室（policy） | 组织安全规则、权限、必经检查 | 受授权的人/配置系统 | 每轮固定加载 | 只以权威配置为准；模型和自动记忆不得改写 |
| 身份门厅（scope） | tenant、用户、项目、agent、权限标签 | 身份/鉴权系统 | 检索前 | 不可由模型推断或向量相似度替代 |
| 证据库（evidence） | 原始消息、工具结果、文件版本、网页快照 | 事件记录器 | 按引用重读 / 供抽取 | append-only 或版本化；是重验来源，非默认 prompt 内容 |
| 事实柜（verified facts） | 用户确认的偏好、已确认决策、稳定项目事实 | 用户、人工审核或满足规则的 verifier | 通过 scope/time 过滤后按需 | 每项有 source、时间、状态、版本和 supersedes |
| 经验室（candidate lessons） | 模型归纳的经验、待确认模式、后台 consolidation 候选 | 自动抽取器 | 默认不作为高信任结论 | 可过期、可复核；无来源/确认不能升级到事实柜 |
| 档案室（cold index） | embeddings、关键词索引、旧摘要、知识图 | 索引器 | 仅用于召回候选 | 索引不是权威来源也不是授权机制 |
| 隔离室（quarantine） | 网页、外部仓库、未知工具输出中的不可信指令性文本 | 受限摄取路径 | 只以 data 形式、受审查地读 | 不得自动进入 policy 或 verified facts |

这与 Claude/Codex 将规则与自动记忆分开、LangGraph Store 管跨线程事实、Letta 区分 always-in-context 与 on-demand 的方向一致。[1][2][3][4][11] 外来内容进隔离室对应 OWASP 间接提示注入风险。[8]

### 4.2 让每层只承担一种责任

```text
immutable policy / durable guidance ----> 每轮装载，不能由学习记忆覆盖
raw event + artifact ------------------> 可审计真相源（供抽取/核验）
       |
       +--> candidate-memory extraction (async preferred)
                   |
user-confirmed or reviewed write ------+--> versioned long-term facts
                                              |
new task + identity + permissions + time --> filter -> rank -> bounded prompt view
```

- mandatory policy 与 learnable fact 使用不同命名空间、写入权限和 prompt 位置；持久指令不替代执行层权限与确定性检查。[1][11][15]
- 原始事件是追溯和重新抽取的依据；摘要、embedding、graph edge 都是派生物。
- **不要**用“会话能否 resume”来衡量长期知识是否可靠——那是另一条产品线。

### 4.3 有效性：六个可检查的门

```text
usable(memory, request) =
  source_complete
  AND authorized(scope, request.identity)
  AND temporally_valid(now)
  AND not_deleted
  AND not_superseded_or_conflict_resolved
  AND retrieval_is_relevant
```

最后一项只表示“这次问题可能需要它”，不能替前五项背书。语义相似不能证明 source、权限、时效或当前真值。

| 校验门 | 最小证据 | 不通过时的行为 |
| --- | --- | --- |
| 来源 | `source_event_id` / artifact URI、内容 hash、抽取器版本 | 保持 candidate；重读来源或拒绝写入 |
| 授权与作用域 | tenant/user/project/agent、数据类别、read/write policy | 不检索、不注入，记录拒绝原因 |
| 时间 | `observed_at`、`valid_from`、`valid_to`、last-verified | 标为 stale；重验、降级或不使用 |
| 冲突 | `version`、`supersedes`、并发 revision 或显式 conflict | 不静默覆盖；保留候选或要求确认 |
| 删除 | tombstone / deletion job 状态及其派生对象状态 | 在所有检索和 prompt 路径拒绝 |
| 使用 | 当前问题、最小证据锚点、必要时原文重读 | 不因 top-k 命中就执行高风险动作 |

有效期应按信息种类定义，而非统一 TTL：

| 记忆类型 | 可信起点 | 失效/复验触发 | 可用方式 |
| --- | --- | --- | --- |
| 用户明确偏好 | 用户确认事件 | 用户改口、删除或 scope 改变 | 在对应用户 scope 内使用 |
| 项目构建命令/规则 | 文件路径、commit/worktree hash | 文件变更或执行失败 | 重新读取权威文件，不信旧摘要 |
| 测试/命令结果 | 命令、exit code、环境与 artifact | worktree/依赖/时间变化 | 短寿命 evidence，必要时重跑 |
| 网页/API 事实 | URL、获取时间、响应版本 | TTL 到期或高风险行动前 | 重新查询；勿升为永久规则 |
| 模型归纳的经验 | 至少一个带来源的 candidate | 无 corroboration、反例或后续失败 | 低信任/短 TTL，或要求用户确认 |
| 安全/合规 policy | 受管理的配置或人工 owner | owner 发布新版本 | 固定权威装载，不接受自动学习覆盖 |

LongMemEval 将 temporal reasoning、knowledge update 与 abstention 独立评估；“可检索到”不等于“现在仍正确”，也不等于缺证时会拒答。[20]

### 4.4 写入：明确意图优先，自动归并可延后

| 写入路径 | 适合内容 | 需要的保护 |
| --- | --- | --- |
| 同步/热路径 | 用户明确的低风险偏好，或下一 turn 必须立即使用的事实 | 明确确认、schema 校验、作用域选择、幂等 event ID 和写入结果回显 |
| 异步/后台 | 多轮经验归并、去重、过期检查、主题文件整理 | 可见状态、重试/失败告警、版本差异、可撤销提交、不得悄然提升权限 |
| 人工维护 | 团队规则、安全约束、长期项目约定 | 可审阅文本、版本控制、明确 owner；强制要求放 hook/权限/CI |

Claude Code 的自动记忆小索引、Codex 的异步 memories、Letta 的 dreaming 都表明：后台整理是实际产品选择；异步意味着刚结束的对话不一定立即对新对话生效。[1][2][11]

### 4.5 按用户自动记忆：身份先于抽取

以下是跨产品证据支持的**抽象控制流**，不是任何厂商公开的内部实现：

```text
authenticated event
  {tenant_id, user_id, project_id?, agent_id?, source_id, privacy_mode}
        |
        +-- temporary/private/off/tainted? --> 不进入长期学习
        |
        v
candidate extraction
  explicit remember | correction | stable preference | repeated fact | transient fact
        |
        v
scope + trust + time + source validation
        |
        +-- low confidence / sensitive / conflict --> 待确认或短期 candidate
        |
        v
versioned user memory --> indexes --> authorized retrieval --> bounded prompt view
```

关键控制点：

1. `tenant_id`、`user_id` 与项目归属来自认证/会话系统，不允许模型从文本猜测；模型只提取“记什么”，不决定“记给谁”。ChatGPT 与 Gemini 均以登录账户、功能开关和产品入口限定个性化范围；Claude Code 则按 Git repository 派生项目目录，所有 worktree 共享该目录。[1][32][33]
2. 写入前过滤 Temporary Chat、memory off、隐私模式、未授权连接应用和外部污染源。关闭学习与关闭读取应是两个显式状态；否则用户无法判断“本轮没写入”还是“旧记忆仍在使用”。[11][28][32][33]
3. 将“用户明确要求记住”“用户纠正旧认识”“系统推断稳定偏好”分成不同 trust。前两者可优先进入热路径；推断项宜先成为带来源、可过期的 candidate。
4. 作用域不应只有 `user_id`。编码 Agent 通常还需要 `project/repository`，企业产品需要 `tenant/org`，专用 sub-agent 可能需要独立 `agent_id`；扩大到全局用户偏好必须是显式升级，而不是因为多个项目文本相似。[1][3][4]
5. 每次回答只生成有预算的 memory view，并提供“为何使用”的可观测信息。ChatGPT 的 response sources 与 Gemini 的“是否使用过去聊天”询问入口，说明来源解释已成为实际产品控制面，但二者都不保证公开全部内部影响因素。[32][33]

### 4.6 纠正记忆：替代、删除与重新学习必须分开

用户说“不是 npm，是 pnpm”时，合理语义不是原地改一个字符串，而是一次有来源的状态迁移：

```text
old memory M1 (active)
  + correction event E2
  -> new memory M2 (active, supersedes=M1, source=E2)
  -> M1 (superseded, no longer eligible for current prompt)
  -> invalidate/rebuild derived summary, keyword/vector/graph indexes and caches
```

四类动作需要不同结果：

| 用户意图 | 正确状态变化 | 不应承诺的事情 |
| --- | --- | --- |
| “你说错了，正确是 B” | 写入 correction event；B 成为当前版本，A 被 supersede；保留历史来源供审计 | 不等于已物理删除 A 的所有痕迹 |
| “不要再用 A” | A 立即从所有读取/prompt 路径失效，派生索引同步失效 | 不等于底层日志已完成合规擦除 |
| “忘掉 A” | 建立 deletion job，级联原始来源、事实、summary、embedding、graph、cache 与后台任务，并可验证完成 | 不应只从 UI 列表隐藏 |
| “关闭记忆” | 停止新的学习；产品需明确旧记忆是否仍读取、是否保留 | 不等于删除已有聊天和记忆 |

ChatGPT 允许直接编辑综合视图，但明确要求完整删除所有来源；Gemini 允许聊天中纠正，却要求删除所有相关聊天才能删除认识；Claude Code 的 plain Markdown 则允许用户直接审阅、编辑或删除文件。[1][32][33] **推断**：行业产品普遍提供了纠正入口，但没有公开证明其内部实现都具备版本化 `supersedes` 或原子级索引失效。因此，版本链是推荐的通用模型，不是对这些产品内部结构的断言。

“重置全部记忆”的产品合同也不能跨产品类推。Codex 当前公开 UI 与测试明确把 reset 限定为 memory 文件、rollout summaries 和 memory rows，并保留 thread 与其 memory mode；Claude Code 只公开 plain Markdown 文件的人工删除能力，未公开等价的一键 reset；ChatGPT 的 “Delete and turn off memory” 明确保留 past chats，重新开启后可能从仍在 chat history 中的旧聊天生成新记忆。[1][32][34] **推断**：清空派生记忆、关闭后续生成、删除历史来源是三个独立控制面；若来源仍保留且生成仍开启，不能承诺“重置后永不重新学回”。

纠正后的验收不能只问一次模型“现在记得什么”，至少要覆盖：精确查询、语义近义查询、新会话启动、后台巩固完成后、缓存未命中/命中、以及旧来源仍存在时是否会把错误值重新学回来。ChatGPT 明确说明重新开启 memory 后可能从未删除的旧聊天重新生成记忆，Gemini 也提示删除后的个性化停止存在延迟。[32][33]

### 4.7 检索：先授权过滤，再做相关性排序

1. 按 tenant/user、agent、repository/project、数据类型、读取权限过滤。
2. 过滤无效、已删除、已 supersede 或超出时间范围的记录；“最近”与“目前仍为真”应是不同字段。
3. 使用精确 key、metadata、关键词、向量或图关系找候选；向量命中仅是候选，不是授权判定。
4. 对每条候选保留 source reference、写入/更新时间和 trust level，以有预算的形式进入 prompt。
5. 高风险结论、外部副作用或互斥事实发生时，重读原始来源或要求确认。

LangGraph 对 namespace prefix、limit 截断和后端排序的说明表明：存储 API 默认行为本身足以导致漏召回或跨 scope 误取。[4]

### 4.8 冲突、时间与并发：记录替代关系而不是静默覆盖

适合长期事实的最小记录可包含：

```json
{
  "id": "mem_...",
  "scope": {"owner": "...", "project": "...", "kind": "preference"},
  "claim": "...",
  "source_event_ids": ["evt_..."],
  "observed_at": "...",
  "valid_from": "...",
  "valid_to": null,
  "trust": "user-confirmed | observed | inferred",
  "status": "active | superseded | deleted",
  "supersedes": "mem_...",
  "version": 3
}
```

LangGraph 的 `put` 是 store-or-overwrite，Letta 的 memory update 也存在 last-write-wins 类边界；若需保留竞争写入，须在其上增加版本比较、冲突事件或显式 merge。[4][17] Graphiti 以 valid/invalid 时间表达新旧事实并存，适合需要“现在的真值”与“历史时点真值”同时回答的场景。[7]

### 4.9 治理：可见、可编辑、可删除、可测量

- **可见性**：列出当前加载了哪些指令/记忆、为什么被选中、占用多少预算、最后何时更新。
- **可编辑性**：按 scope 浏览、改写、禁用自动学习与撤销单条记忆；Claude Code `/memory` 与 Codex `/memories` 提供了相近控制面。[1][11]
- **删除性**：定义并验证事实记录、embedding、摘要、缓存、trace、后台队列的级联或独立删除语义；不可把“UI 列表里消失”称为完成数据清除。[9][16]
- **可观测性**：记录 write candidate、accept/reject 理由、检索候选、最终注入项、token/延迟、失败与重试——定位“没有记住”“记错”“不该记住”和“已删仍出现”。

## 5. 常见陷阱

| 反模式 | 为什么会失败 | 可从资料看到的边界 |
| --- | --- | --- |
| 把完整 transcript 当长期知识库 | 噪声多、无有效期/冲突语义；能重放 ≠ 能回答新任务 | 产品常有 session 文件，但与 auto memory 机制分离。[1][5][11] |
| 把会话恢复当成“有记忆了” | resume 解决的是同一线程续聊，不解决跨会话事实治理 | 边界对照，见 §2.1；本笔记不展开恢复实现 |
| 只用向量检索 | 漏召回、陈旧项、相似但越权项、静默 top-k 截断 | LangGraph Store 暴露 prefix/limit/order 边界。[4] |
| 自动抽取后无约束写入 | 误解、幻觉、重复与恶意文本会在未来 session 被放大 | Graphiti 抽取失败与 LongMemEval 更新/拒答维度。[7][20] |
| 用 memory 文本执行安全策略 | context 不是可靠执行器；冲突或注入可影响遵从 | Claude/Codex 将指导与 hook/linters/typechecks 区分。[1][15] |
| 只删除“记忆条目”UI 对象 | 派生摘要、向量、巩固产物、缓存和 trace 可能仍在 | 删除与级联须分路径验证。[9][16][26] |
| 让不可信内容成为高信任指令 | 网页、文件、RAG 片段可间接注入，并跨 session 复发 | OWASP：RAG 不能消除 prompt injection。[8] |

### 5.1 持久化提示注入的特殊风险

OWASP 将来自网站或文件的恶意内容列为 indirect prompt injection，并指出 RAG 和 fine-tuning 不能完全缓解。[8] 当该内容被提取、摘要或写入 memory 时，攻击从单次输入变成跨 session 的复发状态。

合理防线是组合措施：保存来源与信任等级；限制可写字段和长度；把记忆作为 data 而非 developer/system policy；高权限工具前重新审批；对读回 prompt 做结构化/隔离处理；测试恶意记忆不能改变 policy、绕过审批或跨 tenant 泄漏。[8]

## 6. 评估与开放问题

LongMemEval 将长期交互记忆拆为 information extraction、multi-session reasoning、temporal reasoning、knowledge update 与 abstention，并报告现有系统在持续记忆任务上仍有显著退化。[20] 任何持久记忆系统至少应额外验证：

- **更新**：偏好或事实被更正后，当前问题采用新值；必要时历史时点仍可答旧值。
- **冲突/并发**：两个写入者的冲突能被记录、比较或要求确认，而非无声 last-write-wins。
- **隔离**：相同关键词、近似 embedding、prefix namespace 下不会跨用户/项目读取。
- **删除**：精确查找、语义检索、prompt assembly、缓存和后台 consolidation 都不能返回已删除数据。
- **拒答**：没有足够证据时不编造“记忆”；过期或低信任记录不会被伪装为当前事实。
- **巩固完整性**：抽取失败、lease 过期、Phase 2 中断后，不会半写入或重复生效。

仍需在实际采用前回答的问题包括：哪些事实允许自动写入、何时需要用户确认；什么算 project/user/org 的正确 scope；时间有效性由谁定义；派生 embedding/summary 的删除是否可证明完成；以及不同模型、工具权限和上下文预算下的成本/质量阈值。这些是产品和合规选择，不能由某个 memory API 的默认行为替代。

## 参考资料

资料 [1]-[31] 初次于 2026-07-21 访问，其中 [1] 于 2026-07-31 重新核验；新增资料 [32]-[34] 于 2026-07-31 访问。除论文、标准组织材料和公开源码外均为供应商官方文档。

1. Anthropic, [How Claude remembers your project](https://code.claude.com/docs/en/memory)；[Sessions](https://code.claude.com/docs/en/sessions) 仅作「session 文件 ≠ 语义记忆」对照。
2. Letta, [Memory & dreaming](https://docs.letta.com/configuration/memory/) and [MemFS](https://docs.letta.com/concepts/memfs/).
3. LangGraph, [Persistence](https://docs.langchain.com/oss/python/langgraph/persistence)（checkpoint 仅作与 Store 分层的对照）。
4. LangGraph, [Stores](https://docs.langchain.com/oss/python/langgraph/stores).
5. Aider, [Options](https://aider.chat/docs/config/options.html) and public source: [chat history path](https://github.com/Aider-AI/aider/blob/4e77720c6f96d4960b61ef19f32a2ee12218bf96/aider/coders/base_coder.py#L519-L523)（对照：有 transcript ≠ 自动语义记忆）。
6. Continue, [Rules deep dive](https://github.com/continuedev/continue/blob/bd36638ee0cf9fb90c314647bab2f1c6897aa6fe/docs/customize/deep-dives/rules.mdx) and public source: [load Markdown rules](https://github.com/continuedev/continue/blob/cc2a6c3f166c235ce1ec1ac930873d31e9d40126/core/config/markdown/loadMarkdownRules.ts), [system-message assembly](https://github.com/continuedev/continue/blob/c858aee55973d6ece243713dd251c8fc3985a330/core/llm/rules/getSystemMessageWithRules.ts).
7. Zep, Graphiti public source: [README](https://github.com/getzep/graphiti/blob/main/README.md) and [`EntityEdge` temporal fields](https://github.com/getzep/graphiti/blob/main/graphiti_core/edges.py).
8. OWASP Gen AI Security Project, [LLM01:2025 Prompt Injection](https://genai.owasp.org/llmrisk/llm01-prompt-injection/) (modified 2025-04-17).
9. OpenAI, [Data controls in the OpenAI platform](https://developers.openai.com/api/docs/guides/your-data) and [Delete a conversation](https://developers.openai.com/api/reference/resources/conversations/methods/delete)（删除边界的一般警示）。
10. OpenAI Codex public source, [`LocalThreadStore`](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/thread-store/src/local/mod.rs#L60-L72), commit `1836ae0612052137d0cabaff7807ff8314cee940`（对照：durable replay ≠ 语义记忆）。
11. OpenAI, [Codex memories](https://developers.openai.com/codex/customization/memories) and [AGENTS.md](https://developers.openai.com/codex/agent-configuration/agents-md).
12. Anthropic, [Sessions](https://code.claude.com/docs/en/sessions)（仅对照）。
13. Aider public source, [append chat history](https://github.com/Aider-AI/aider/blob/14af218ea281854c0900ebbfcf8ca453aa3c41aa/aider/io.py#L1117-L1136).
14. Aider public source, [`/clear`](https://github.com/Aider-AI/aider/blob/edfe0c801b494b69455b7de4a26843b2033cebb4/aider/commands.py#L411-L443).
15. OpenAI, [Codex customization](https://developers.openai.com/codex/customization).
16. OpenAI, [Delete a conversation item](https://developers.openai.com/api/reference/resources/conversations/subresources/items/methods/delete).
17. Letta, [Memory blocks](https://docs.letta.com/v1-sdk/memory/memory-blocks/) and [Archival memory](https://docs.letta.com/v1-sdk/memory/archival-memory/).
18. Letta, [Context hierarchy](https://docs.letta.com/v1-sdk/memory/context-hierarchy/).
19. Packer et al., [MemGPT: Towards LLMs as Operating Systems](https://arxiv.org/abs/2310.08560), v2, 2024-02-12.
20. Wu et al., [LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory](https://arxiv.org/abs/2410.10813), ICLR 2025.
21. OpenAI Codex public source, [memory pipeline README](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/README.md#L29-L157), commit `1836ae0612052137d0cabaff7807ff8314cee940`.
22. OpenAI Codex public source, [Phase 1 extraction](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase1.rs#L50-L186) and [strict sampling/redaction](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase1.rs#L230-L335), same commit.
23. OpenAI Codex public source, [Phase 2 global consolidation](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase2.rs#L50-L210), same commit.
24. OpenAI Codex public source, [memory extension injection](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/ext/memories/src/extension.rs#L40-L114), [bounded summary rendering](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/ext/memories/src/prompts.rs#L23-L51), and [limits/tools](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/ext/memories/src/lib.rs#L11-L22), same commit.
25. LangGraph public source, [`PostgresStore` schema/TTL/vector cascade](https://github.com/langchain-ai/langgraph/blob/9578140336b01a748d16ea154ae6278e155983f3/libs/checkpoint-postgres/langgraph/store/postgres/base.py#L64-L111), [upsert](https://github.com/langchain-ai/langgraph/blob/9578140336b01a748d16ea154ae6278e155983f3/libs/checkpoint-postgres/langgraph/store/postgres/base.py#L401), [transactions](https://github.com/langchain-ai/langgraph/blob/9578140336b01a748d16ea154ae6278e155983f3/libs/checkpoint-postgres/langgraph/store/postgres/base.py#L968-L1004), and [TTL sweeper/read filtering](https://github.com/langchain-ai/langgraph/blob/9578140336b01a748d16ea154ae6278e155983f3/libs/checkpoint-postgres/langgraph/store/postgres/base.py#L735-L895), commit `9578140336b01a748d16ea154ae6278e155983f3`.
26. Mem0 public source, [`Memory` add/extract/search/update/delete paths](https://github.com/mem0ai/mem0/blob/fec2fe6a2ce3451cb00ad670b12b88816ed4f7db/mem0/memory/main.py#L721-L833), [candidate extraction](https://github.com/mem0ai/mem0/blob/fec2fe6a2ce3451cb00ad670b12b88816ed4f7db/mem0/memory/main.py#L875-L1161), [search/ranking](https://github.com/mem0ai/mem0/blob/fec2fe6a2ce3451cb00ad670b12b88816ed4f7db/mem0/memory/main.py#L1335-L1687), and [mutation/history](https://github.com/mem0ai/mem0/blob/fec2fe6a2ce3451cb00ad670b12b88816ed4f7db/mem0/memory/main.py#L1771-L2079), commit `fec2fe6a2ce3451cb00ad670b12b88816ed4f7db`.
27. OpenAI Codex public source, [MemoryTool feature gate](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/features/src/lib.rs#L937), [memories defaults and bounds](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/config/src/types.rs#L47) and [startup gate/orchestration](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/start.rs#L23-L76), commit `1836ae0612052137d0cabaff7807ff8314cee940`.
28. OpenAI Codex public source, [external-context flag from streamed search](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/core/src/stream_events_utils.rs#L131), [tool output](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/core/src/tools/registry.rs#L703), [MCP output](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/core/src/mcp_tool_call.rs#L787), [state mode/query](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/state/src/runtime/memories.rs#L133), and [protocol enum](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/protocol/src/protocol.rs#L687), same commit.
29. OpenAI Codex public source, [generate/use semantics](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/config/src/types.rs#L296-L340), [eligible-rollout query and state transition](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/state/src/runtime/memories.rs#L133-L218), [input filtering and redaction](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase1.rs#L314-L440), [best-effort sanitizer](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/secrets/src/sanitizer.rs#L4-L42), [Phase 2 ownership/commit](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase2.rs#L409-L470), and [parent-derived consolidation permissions](https://github.com/openai/codex/blob/1836ae0612052137d0cabaff7807ff8314cee940/codex-rs/memories/write/src/phase2.rs#L337-L379), same commit.
30. Letta public source, [`memory-git.ts` synchronization and commits](https://github.com/letta-ai/letta-code/blob/eae673af0aab574c5c50add48e4d32c4ff02b83d/src/agent/memory-git.ts#L1222-L1245), [pull/rebase](https://github.com/letta-ai/letta-code/blob/eae673af0aab574c5c50add48e4d32c4ff02b83d/src/agent/memory-git.ts#L1701-L1780), [post-turn push conflict handling](https://github.com/letta-ai/letta-code/blob/eae673af0aab574c5c50add48e4d32c4ff02b83d/src/agent/memory-git.ts#L2026-L2120), [memory tool writes/deletes](https://github.com/letta-ai/letta-code/blob/eae673af0aab574c5c50add48e4d32c4ff02b83d/src/tools/impl/memory.ts#L106-L273), and [memory prompt/load semantics](https://github.com/letta-ai/letta-code/blob/eae673af0aab574c5c50add48e4d32c4ff02b83d/src/agent/prompts/letta.md#L8-L76), commit `eae673af0aab574c5c50add48e4d32c4ff02b83d`.
31. Graphiti public source, [`EntityEdge` temporal/provenance fields](https://github.com/getzep/graphiti/blob/ca4d5e9d8c5d25d45917427b63daec17603a0d3a/graphiti_core/edges.py#L265-L340), commit `ca4d5e9d8c5d25d45917427b63daec17603a0d3a`.
32. OpenAI Help Center, [Memory FAQ](https://help.openai.com/en/articles/8590148-memory-faq)（2026-07-31 页面显示当日更新；覆盖新的 memory summary、自动更新、来源解释、纠正、完整删除和 legacy saved memories 边界）。
33. Google Gemini Apps Help, [Get personalization with memory of your past Gemini chats](https://support.google.com/gemini/answer/16598469?hl=en) and [Get personalization in Gemini Apps](https://support.google.com/gemini/answer/16598623?hl=en)（账户/开关条件、历史聊天个性化、纠正与删除边界）。
34. OpenAI Codex public source, [`add_ad_hoc_note` tool contract](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/ext/memories/src/tools/ad_hoc_note.rs), [append-only local write](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/ext/memories/src/local/ad_hoc_note.rs), [ad-hoc consolidation instructions](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/memories/write/templates/extensions/ad_hoc/instructions.md), [TUI reset confirmation](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/tui/src/bottom_pane/memories_settings_view.rs#L70-L130), [`memory/reset` handler](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/app-server/src/request_processors/thread_processor.rs#L1689-L1712), and [reset integration test](https://github.com/openai/codex/blob/f0c30e528a54bdf0fa9a4d52ff74b34383434811/codex-rs/app-server/tests/suite/v2/memory_reset.rs#L23-L68), commit `f0c30e528a54bdf0fa9a4d52ff74b34383434811`.
