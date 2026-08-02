# 上下文压缩与缓存命中：业界关系调研

> Status: research note, not an implementation plan.
>
> Research date: 2026-07-16. Re-verify before adopting; vendor behavior changes.
>
> Scope: 上下文压缩（compaction / summarization / history replacement）如何影响 provider **prompt cache / prefix cache** 的命中率；主流 coding agent 与 API 如何把两者协同设计；什么操作会“省窗口却砸缓存”。
>
> Out of scope: 本仓库实现映射；向量长期记忆检索质量；单纯 tokenizer 估算；通用 prompt engineering 教程。

## 1. Summary

- **压缩与缓存是两个正交旋钮，但共享同一约束：请求前缀的字节/token 级稳定性。** 压缩改的是“模型可见历史有多长”，缓存赚的是“与上一次请求共享多长的相同前缀”。改历史结构几乎总会改前缀 → 至少会砸掉 **conversation 层** 缓存。
- **业界收敛模式不是“压缩 vs 缓存二选一”，而是分层：**
  1. 稳定前缀（system / tools / project rules）尽量永不重排、不重写；
  2. 日常 turn 只在尾部追加，最大化 **incremental prefix hit**；
  3. 接近窗口时做 **cache-safe fork compaction**（摘要请求与父会话共享同一 system/tools/history 前缀）；
  4. 摘要安装后接受 conversation 层 miss，但保留 system/project 层 hit，并用更短历史重建 conversation 缓存。
- **最大成本陷阱是 naive compaction：** 用另一个 system prompt、去掉 tools 的独立“summarize this”请求去消化整段历史。前缀从第一个 token 就分叉 → 长历史全价 uncached 输入，恰恰在会话最贵时爆炸。[1]
- **第二大陷阱是“为了省 token 而中途改 tools/model/system”。** tools 与 model 都是缓存键的一部分；中途增删工具、切模型，会让已有长前缀整段失效，往往比继续用原模型更贵。[1][2]
- **OpenAI 与 Anthropic 语义不同但结论同构：** 都依赖 exact/longest prefix match；compaction 产生新的稳定短前缀（Anthropic 的 `compaction` block / OpenAI 的 encrypted compaction item），后续 hit 取决于安装后的前缀是否稳定复用。[3][4][5]
- **好的产品把 cache hit rate 当 uptime 指标，而不是成本事后报表。** Claude Code 团队对 hit rate 告警并当 SEV 处理；压缩只是其中一个会系统性砸 conversation 层的生命周期事件。[1][2]

## 2. Problem boundary

| 术语 | 含义 | 不应混同为 |
| --- | --- | --- |
| Prompt / prefix cache | Provider 侧对请求前缀（到 cache breakpoint 为止）的 KV/计算复用；命中需前缀一致。 | 应用层 HTTP 缓存、本地 transcript 磁盘缓存、embedding 向量库。 |
| Cache hit / miss | 本次请求中有多少 input tokens 来自已缓存前缀 vs 新写入/未缓存。 | 模型是否“记住”了用户意图（语义记忆）。 |
| Compaction | 用更短表示替换工作视图中的旧历史（摘要、opaque item、replacement history）。 | 传输层 gzip、日志压缩、仅截断 tool stdout。 |
| Cache-safe fork | 侧路请求（摘要/skill/子计算）与父会话共享 system、tools、历史前缀，只在末尾追加新指令。 | 另开一个不同 system 的独立 summarize job。 |
| Conversation-layer miss | 历史被替换后，旧 message 前缀不再匹配；system/tools 仍可能命中。 | “缓存彻底坏了 / 全部重算”。 |
| Thrashing | 压缩后立刻再胀满再压缩，反复 miss + 反复摘要。 | 单次手动 `/compact`。 |

**常被混淆的两件事：**

1. **“窗口装得下”≠“缓存能命中”。** 1M 窗口仍可能每 turn 全量 uncached，如果前缀每轮被改写。
2. **“压缩成功缩短了 prompt”≠“下一 turn 更便宜”。** 若压缩调用本身 uncached，或安装摘要后 system/tools 也被一起打乱，总成本可能上升。

## 3. Industry mechanisms

### 3.1 共同物理：prefix match

Anthropic 与 OpenAI 的公开机制都可概括为：

```text
request = [stable prefix ...][growing / dynamic tail]
cache key ≈ model (+ effort/route) + exact prefix up to breakpoint(s)
hit  = longest previously written matching prefix
miss = any earlier byte/token change invalidates everything after it
```

关键推论：

- **顺序即策略。** static first, dynamic last。[1]
- **任何“好心的前缀整理”（重排 tools、刷新时间戳进 system、按权限裁工具集）都是 cache break。**[1]
- **压缩改的是 conversation 段的结构**；若摘要请求或安装后的请求没有保护更早的稳定层，miss 会向上蔓延到 tools/system。

### 3.2 Anthropic / Claude Code：分层缓存 + cache-safe compaction

#### 请求分层（Claude Code 公开实践）

Claude Code 把 prompt 排成多层，使尽可能多的会话共享前缀：[1][2]

| 层 | 内容 | 变化频率 | 缓存意图 |
| --- | --- | --- | --- |
| System + Tools | 核心指令、工具 schema、output style | 升级 / 工具定义变更 | 全局或长生命周期 hit |
| Project context | `CLAUDE.md`、auto memory、unscoped rules | 会话开始、`/clear`、`/compact` 后重注入 | 项目内 hit |
| Session context | env、MCP 呈现、部分会话态 | 会话级 | 会话内 hit |
| Conversation | messages、tool results | 每 turn 增长 | 增量前缀 hit |

API 侧：`cache_control` breakpoint（自动或显式，通常最多约 4 个）标记可写缓存的前缀边界；默认 ephemeral TTL 常为约 5 分钟，部分路径支持更长 TTL；cache read 显著便宜、write 略贵。[2][6]

#### 压缩如何碰到缓存

Claude Code / Anthropic 工程叙述把 compaction 拆成两段，**命中行为完全不同**：[1][2]

```text
A. 摘要请求（fork）
   与父会话相同的 system / tools / project / 完整历史
   + 末尾追加 compaction 指令
   → 应大量 cache read 父前缀；主要成本在 summary 输出

B. 安装摘要后的下一轮
   system / tools 尽量不变；project 从磁盘重注入
   conversation = summary (+ 可能的 rehydrate) + 新用户输入
   → conversation 层 intentionally miss
   → system/project 层应仍 hit（若磁盘内容未变）
   → 随后在更短历史上重建 conversation 缓存
```

官方/产品文档还强调：

- 摘要请求本身应复用当前缓存前缀，而不是另起炉灶。[2]
- `/compact` 会有意砸掉 conversation 层；在任务自然边界执行比任务中途更划算。[2]
- 若只是想丢弃错误路径，**`/rewind` 回到已缓存前缀** 通常优于摘要重建。[2]
- Compaction 后从磁盘重注入的 root `CLAUDE.md` / memory 可再进缓存；path-scoped rules、nested `CLAUDE.md` 等若只活在历史里，可能直到再次触发才回来。[7]

#### 明确的 anti-pattern（Anthropic 原话级结论）

Naive compaction：[1]

```text
main agent request:  system_A + tools_T + history_H   ← cached
summarize request:   system_summarizer + (no tools) + history_H
                     ↑ 前缀从第一个 token 就不同
→ 整段 H 按 uncached input 计费
→ 会话越长（越需要压缩）这次调用越贵
```

Cache-safe fork：[1]

```text
compact request: system_A + tools_T + history_H + user("compact...")
                 ↑ 与父请求共享前缀
→ cache hit on H；只为 compact 指令与 summary 输出付新成本
→ 需要预留 compaction buffer（指令 + summary output tokens）
```

Anthropic 后续把 compaction 能力直接做进 API/平台（beta compact edits / `compaction` content block 等路径；Bedrock 文档亦描述可在 compaction block 上挂 `cache_control`，并注明新 compaction 可能导致后续 miss）。[1][6]

#### 为缓存而改产品形态（不只是“调 API”）

Claude Code 的若干功能直接被缓存约束塑造：[1]

| 功能直觉 | 缓存不友好做法 | 缓存友好做法 |
| --- | --- | --- |
| Plan mode 只读 | 热切换为只读 tool set | 工具集不变；`EnterPlanMode`/`ExitPlanMode` 工具 + message 态 |
| 少给工具省 token | 中途 add/remove tools | 固定 stubs + `defer_loading` / tool search，按需展开 schema |
| 简单问题换小模型 | 同会话切 Haiku | 子 agent + handoff；父前缀不动 |
| 时间/文件变了 | 改 system 里的 timestamp | `<system-reminder>` 塞进下一条 user/tool result |

这些不是“优化小技巧”，而是 **把状态机建模成 message/tool，而不是前缀突变**。

### 3.3 OpenAI Responses：prefix cache + compact item

OpenAI prompt caching（公开文档与社区核验）要点：[3][8]

- **Exact prefix match**；静态内容靠前，动态靠后。
- 常见门槛：足够长的前缀（文档常见叙述为 ≥1024 tokens，并按固定步长扩展；以当前模型文档为准）。
- `prompt_cache_key`（及路由/哈希）用于提高共享长前缀的命中稳定性。
- 观测字段：`usage.input_tokens_details.cached_tokens`（及较新模型上的 cache write 计量）。
- 社区持续报告：部分模型/负载下“长公共前缀 + 变化后缀”命中不稳定；工程上仍应按 exact prefix 设计，并以实测 `cached_tokens` 为准，不能假设“逻辑上相同”就会 hit。[8]

Compaction（Responses）：[4]

- **Server-side**：`context_management` + threshold，超阈后服务端插入 compaction item 并裁剪。
- **Standalone** `POST /responses/compact`：客户端提交当前窗口，拿回 **canonical compacted window**，原样作为后续 `input`；不要自行再剪 compact 结果。
- 语义上常见描述：用户消息更倾向保留；assistant/tool/reasoning 等被 **opaque/encrypted compaction item** 替换（具体保留集以当前官方 guide 为准）。
- 与 `previous_response_id` 链式会话的关系：链式方便，但**不会 magically 免去历史计费**；窗口膨胀时仍需 compact 降 token。

**与缓存的耦合：**

```text
compact 之前：长历史前缀可能已有 cache
compact 调用：本身有模型成本；是否 hit 取决于是否复用同一静态前缀/key
compact 之后：新窗口 = 稳定 instructions/tools + compaction item + retained tail
             若此后每 turn 保持该结构稳定，短前缀可重新积累 cached_tokens
             若 compact 后重排 tools / 改 instructions，则连新前缀也写不稳
```

OpenAI 侧公开材料较少像 Anthropic 那样把 “cache-safe fork summarizer” 写成产品哲学，但 **mechanics 要求相同**：compact 输出必须成为新的稳定前缀锚点，而不是每轮重新生成不同 summary 文本。

### 3.4 其他 agent / 框架的交叉证据

| 系统/资料 | 与“压缩×缓存”相关的可观察点 | 启示 |
| --- | --- | --- |
| Codex remote compaction | 专用 compact 路径 / replacement history / compaction item；工具结果进入下一轮前缀利于 cache；中途改 tools/权限可能打断缓存。[9][10] | 把 compaction 做成 **显式历史替换事件**，而不是静默改写；工具结果 append-only 有利于增量 hit。 |
| LangChain short-term memory | trim / summary memory 改变送入模型的 message 列表；Anthropic 集成可打 `cache_control`。[11] | 摘要节点若每轮改写 summary 字符串，会持续 conversation miss；应 **稳定 checkpoint + 仅在阈值触发时重写**。 |
| Aider 等轻量 coding agent | 倾向小而稳的 repo map / 文件只读加载，强调 cache-friendly 的小前缀。[12] | 有时 **少塞历史** 比 **先塞满再压缩** 更有利于稳定 hit。 |
| Subagent / context isolation | 子 agent 自有窗口与缓存；父会话只回收成果卡。[1][7] | 用隔离替代“把探索日志堆进父历史再 compact”，减少父 conversation 膨胀与 miss 频率。 |

### 3.5 一张关系总表

| 事件 | 对窗口的影响 | 对 cache 的典型影响 | 备注 |
| --- | --- | --- | --- |
| 正常 append user/assistant/tool | 变长 | 长公共前缀 hit，仅尾部 uncached | 健康路径 |
| 改 system 中的动态字段 | 几乎不变 | 高层 miss，其后全失效 | 应改为 message 提醒 |
| 中途 add/remove tools | 可能略变 | 前缀早期 miss | 用 defer/stub/权限消息代替 |
| 同会话切模型 | 不变或变 | 几乎必 miss（cache per model） | 用 subagent handoff |
| Cache-safe compact fork | 临时需要 buffer | 摘要请求应 hit 父前缀 | 关键成本控制点 |
| 安装 summary / compaction item | **显著变短** | conversation 层 miss；system/tools 应 hit | 用更短历史重建 |
| Naive summarize call | 临时仍要吃满历史 | **整段 uncached** | 最大反模式 |
| 频繁 auto-compact thrash | 反复缩胀 | 反复 miss + 反复 write | 需要阈值与熔断 |
| `/rewind` 到旧前缀 | 变短 | 可能直接 hit 旧缓存 | 放弃路径优于摘要 |
| 工具输出外置为 artifact | 变短且更稳 | 减少 tail 噪声，间接提高有效 hit | 与 compaction 互补 |

## 4. Efficient / reasonable patterns

### 4.1 把系统建成“缓存优先”，压缩是生命周期事件

合理默认顺序：

1. **前缀宪法**：固定 tools 顺序与 schema、固定 system 静态段、项目规则进稳定层。
2. **状态进 messages**：时间、plan mode、权限、git 脏状态 → reminder / tool result，不改前缀。
3. **增量 turn**：只 append；不要每轮重序列化“美化后的完整 prompt”。
4. **阈值触发 compact**：token 预算、窗口水位、任务边界；保留 compaction buffer。
5. **compact fork 必须 cache-safe**：同一 model、同一 tools、同一 system、同一历史前缀 + 尾部指令。
6. **安装后 rehydrate 磁盘真相**：规则/memory/计划文件从权威存储重读，而不是指望摘要记得住。
7. **观测**：`cache_read` / `cache_write` / uncached input、compact 触发原因、释放 tokens、thrash 计数。

### 4.2 何时该压缩，何时不该

| 更适合压缩 | 更不适合硬压缩 |
| --- | --- |
| 长工具轨迹、已收敛的探索、跨多文件调试后的阶段切换 | 刚写入的关键约束、未落盘的决策、正在进行的多步编辑 |
| 窗口水位高且近期无大量稳定前缀可 hit | 缓存 TTL 刚预热、紧接着还有很多同前缀 turn |
| 任务自然边界（功能做完、准备下一 epic） | 可用 rewind/子 agent 隔离解决的局部膨胀 |
| 有 artifact/provenance，摘要可回指 | 摘要将是唯一真相源 |

### 4.3 与“只截断 / 只检索”的分工

- **截断**：最后的容量安全网；对缓存而言，**从尾部丢弃**通常优于从中部挖洞（中部挖洞直接破坏前缀连续性）。但静默丢早期约束是正确性风险。
- **检索**：把冷证据留在盘/索引，需要时再读；避免把检索结果无预算地拼进前缀中段。
- **压缩**：对“必须留下叙事连续性，但不能留全部 token”的历史使用；**一次重写 conversation 锚点**。
- **缓存**：要求上述三者都不要无谓改写已稳定的左侧前缀。

### 4.4 成本直觉（定性）

在缓存读 ≪ 正常 input、缓存写略贵的价差下（Anthropic 公开叙述量级约为 read ~0.1×、write ~1.25× 量级，以账单为准）：[2]

```text
健康长会话：
  每 turn ≈ 读大前缀 + 写/算小增量

错误 compact：
  一次 uncached 全历史 summarize  ≫  之后若干 turn 的节省

正确 compact：
  一次 cache-hit 的 fork summarize
  + 一次 conversation miss 的短前缀 rewrite
  + 之后在更短历史上的高 hit
  当且仅当“之后还会有足够多 turn”或“不 compact 会溢出/严重降质”时划算
```

因此 compact 是 **资本性支出（capex）**：买的是后续 turn 的空间与注意力，不是当 turn 的折扣券。

## 5. Pitfalls

1. **Naive summarizer 分叉前缀**  
   另写 system、去掉 tools、甚至换模型做摘要 → 最长历史全价计费。[1]

2. **压缩后重排稳定层**  
   摘要装上的同时“顺便”刷新 tool 列表、重写 system、注入新 MCP 全量 schema → system/tools 层本可 hit 的也 miss。

3. **把动态性放进 cache key 左侧**  
   精确到分钟的时间戳、非确定性 tool 排序、每次不同的 agent 可调用集合。[1]

4. **中途改模型“省钱”**  
   长前缀已在 Opus 上缓存时切 Haiku，可能比 Opus 直接答更贵。[1]

5. **权限/只读模式用 tool set 热切换**  
   应用 message 态或 gate，而不是从 prefix 删除写工具。[1]

6. **Thrashing auto-compact**  
   压缩 → 立刻重读大文件/重跑探索 → 再压缩；cache 反复 write/miss，摘要漂移叠加。Claude Code 文档对 auto-compaction thrash 有专门排查路径，说明这是真实产品故障模式。[7]

7. **把 opaque compaction item 当可编辑文本**  
   OpenAI 类 encrypted item / 平台 compaction block 被客户端改写或只抽取 text，会丢掉服务端依赖的状态，并破坏后续前缀稳定性。[4][6]

8. **只盯 window usage，不盯 cache metrics**  
   窗口“健康”但每 turn `cached_tokens≈0`，长期成本仍爆炸。

9. **摘要覆盖磁盘真相**  
   压缩后不重注入 `CLAUDE.md`/计划/权限，模型只靠可能幻觉的 summary；这是正确性问题，也会逼迫后续 turn 塞更多纠正文本，进一步扰乱前缀。

10. **并行会话/worktree 误判共享缓存**  
    工作目录、平台、git 快照等若进入 system，不同 worktree 可能无法共享；并行打满还可能影响路由与 TTL 刷新（产品相关，需按供应商文档验证）。[2]

## 6. Open questions

- **各模型在“部分前缀 hit”上的实际可靠性**：OpenAI 社区对部分 GPT-5.x 负载有“仅全量命中/长后缀时失败”的报告；需按模型连续测 `cached_tokens`，不能只信 guide 示意图。[8]
- **服务端 compaction 与客户端 fork compaction 的计费边界**：哪些 iteration tokens 计入主 turn、是否暴露独立 compaction usage、与 rate limit 的关系（Bedrock/Anthropic beta 文档在演进）。[6]
- **TTL 与 agent 空闲模式**：5 分钟级 ephemeral 下，用户离开再回是否应主动 “cache warm” 空请求，还是直接接受 rewrite；对 subscription vs API key 路径可能不同。[2]
- **多 breakpoint 的最优布置**：system 末、tools 末、summary 块末、最近稳定 message 末——4 个名额如何在“长静态 + 中等半静态 + 增长对话”间分配，缺少跨厂商基准。
- **opaque summary vs 可审计 structured checkpoint**：OpenAI encrypted item 利于窗口与可能的缓存锚点，但弱于用户可读 provenance；产品如何同时保留 ledger 与模型前缀锚点。
- **tool search / deferred tools 与缓存写放大**：stubs 稳定有利于 hit，但首次展开 schema 时写在哪一层、是否应成为新的半静态 breakpoint，仍偏工程经验。

## 7. Design checklist（供后续实现阶段使用，非本仓库方案）

若设计 coding agent 的“压缩 × 缓存”耦合，业界材料支持如下验收问题：

1. 正常 turn 的 `cache_read / input` 是否随历史增长保持高比例？
2. compact 调用是否与父请求共享 model/tools/system/history 前缀？
3. compact 后下一 turn 是否仍 hit system/tools（及项目规则层）？
4. 权限、plan mode、时间是否 **零前缀突变**？
5. 是否禁止“为本次调用临时裁 tools”？
6. 是否有 thrash 熔断（连续 compact、释放 token 过低、延迟/费用异常）？
7. 是否区分 **rewind（回到旧前缀）** 与 **compact（新建摘要前缀）**？
8. 大探索是否默认进子 agent，避免父历史膨胀触发不必要 compact？
9. 压缩结果是否可观测（事件、释放量、失败、是否损失可重读证据）？
10. 是否把 cache hit rate 当成与错误率同级的运行指标？

## References

以下资料于 2026-07-16 调研中使用。供应商文档与产品行为会变；采用前应复核。部分官方 HTML 在本环境存在区域/网络限制，机制描述以可访问的官方博客、交叉文档与既有研究笔记交叉核验；对仅二次来源支持的细节已在正文降级表述。

1. Thariq Shihipar / Anthropic，[Lessons from building Claude Code: Prompt caching is everything](https://claude.com/blog/lessons-from-building-claude-code-prompt-caching-is-everything)，2026-04-30。**主来源**：prefix match、static-first、messages 更新、禁止中途改 tools/model、cache-safe compaction fork、hit rate SEV。
2. Claude Code 文档，[How Claude Code uses prompt caching](https://code.claude.com/docs/en/prompt-caching)（含 compacting the conversation、分层、失效原因、TTL 相关说明）。
3. OpenAI，[Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching) / [platform guide](https://platform.openai.com/docs/guides/prompt-caching)：exact prefix、最小长度、`prompt_cache_key`、`cached_tokens`。
4. OpenAI，[Compaction](https://developers.openai.com/api/docs/guides/compaction)：server-side threshold、`/responses/compact`、canonical compacted window、与长会话状态的关系。
5. OpenAI，[Conversation state](https://developers.openai.com/api/docs/guides/conversation-state)：链式 `previous_response_id` 与历史计费/状态关系（与 compact 配合理解）。
6. Amazon Bedrock，[Claude Messages compaction](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html) 与 [Prompt caching](https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html)：`compact-2026-01-12`、`compaction` block 上的 `cache_control`、触发阈值、usage iterations。
7. Claude Code，[Context window / what survives compaction](https://code.claude.com/docs/en/context-window) 与 auto-compaction thrash 排查（见 env-vars / troubleshooting 文档集）。
8. OpenAI Community 讨论（例如 partial prefix / `cached_tokens=0` 报告）：[gpt-5.6 prompt caching fails on partial prefixes](https://community.openai.com/t/gpt-5-6-prompt-caching-fails-on-partial-prefixes/1386887) 等——**单方观测，标记为弱证据**。
9. OpenAI Developers，[Codex manual](https://developers.openai.com/codex/codex-manual.md) 与公开 compact/app-server 契约（参见既有 `docs/research/context-compaction-research.md` 对 remote compaction 的接口级整理）。
10. 本仓库既有调研，`docs/research/context-compaction-research.md`、`docs/research/tool-call-control-termination-research.md`：仅作问题边界与 Codex 公开契约交叉，不作为“业界权威”。
11. LangChain，[Short-term memory](https://docs.langchain.com/oss/python/langchain/short-term-memory) 与 ChatAnthropic prompt caching 集成文档。
12. 二次综述：[blog.devaubree.fr on Claude Code prompt caching](https://blog.devaubree.fr/en/blog/prompt-caching-claude-code/)（对 [1] 的结构化转述，便于对照，不作独立权威）。
13. Anthropic，[Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)：上下文策展与压缩的上位方法论。
