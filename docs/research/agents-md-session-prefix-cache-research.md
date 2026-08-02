# AGENTS.md / CLAUDE.md mid-session reload vs prefix cache: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-07-21. Re-verify before adopting; vendor behavior changes.
>
> Scope: 同一 session 内修改项目指令文件（`AGENTS.md` / `CLAUDE.md`）是否生效、是否打穿 provider **prompt / prefix cache**；对比 Codex CLI、Claude Code、Grok Build。
> Out of scope: 硬权限/sandbox 策略；语义记忆 store 细节；本仓库迁移方案。

## 1. Summary

- 三家都把 **项目指令** 当作「尽量稳定」的上下文，而不是每 turn 从磁盘热重载。
- **Claude Code 文档最明确**：根/用户级 `CLAUDE.md` **session 开始读一次**；中途改文件 **不生效、也不砸 cache**；要生效需 `/clear`、`/compact` 或重启（后两者会重载 project context）。
- **Codex**：官方说明 instruction chain 在 **每次 run / 每个 TUI session 启动时构建一次**；指令过期时建议 **重启**；没有公开的 mid-session 热更 AGENTS 语义。缓存侧工程原则是 **固定前缀 + 中途变更尽量 append**。
- **Grok Build**：session 启动加载 root→cwd 的 `AGENTS.md`/`CLAUDE.md` 等；**子树规则可 progressive discovery**（访问新路径时再读），更像 Claude 的 nested 路径。已加载根文件是否 mid-session 热更：**官方未写热更**；xAI API 缓存最佳实践明确 **禁止改历史消息、只 append**。
- 共同规律：**“新开 session”不是唯一打 cache 的条件**；只要请求前缀字节变了就会 miss。产品通常用 **冻结快照** 避免“改磁盘 → 改前缀 → 砸整段 cache”。

## 2. Problem boundary

| 概念 | 含义 |
| --- | --- |
| Project instructions | 仓库/用户级 markdown 规则（`AGENTS.md`、`CLAUDE.md` 等） |
| Session snapshot | 启动时（或 compact/clear 时）读入并固定在请求前缀中的版本 |
| Progressive / nested load | 进入子目录或首次 touch 路径时再注入一层规则（常以 **append** 进入 history） |
| Prompt / prefix cache | Provider 对请求**精确前缀**的 KV 复用；前缀任意早位置变化 → 其后全部 miss |
| Hot reload | 同一 session 内改磁盘文件后立刻替换已注入的指令文本 |

易混点：

1. **磁盘改了 ≠ 模型看见了**（多数产品故意冻结）。
2. **cache 没变 ≠ 规则已更新**（Claude 明确：中途改 `CLAUDE.md` 不砸 cache 也不生效）。
3. **新 session 会重载**，但 **同 session 的 `/compact`/`/clear` 也可能重载**（Claude）。

## 3. Industry mechanisms

### 3.1 Claude Code（文档最硬）

来源：[How Claude Code uses prompt caching](https://code.claude.com/docs/en/prompt-caching)（2026）。

请求分层（为 prefix match 排序）：

| Layer | 内容 | 何时变 |
| --- | --- | --- |
| System prompt | 核心指令、tool definitions、output style | tool 集合变化、产品升级等 |
| Project context | `CLAUDE.md`、auto memory、unscoped rules | **session start**，或 **`/clear` / `/compact`** |
| Conversation | 消息、工具结果 | 每 turn append |

**Editing CLAUDE.md mid-session（官方原文语义）：**

- 项目根与用户级 `CLAUDE.md`：**session 开始读一次，驻留内存**。
- 中途编辑：**不 invalidate cache，也不应用**。
- 新内容在下次 **`/clear`、`/compact`、或 restart** 加载。
- **子目录 nested `CLAUDE.md` / path rules**：首次读到匹配文件时再加载；加载前改文件会生效；加载后进入 conversation history，**不会 retroactively 改历史**。

**`/compact` 与规则：**

- compact 故意打掉 conversation layer。
- system prompt layer 可复用；**project context 从磁盘重载**；仅当 `CLAUDE.md`/memory **相对 session 开始未变** 时该层仍可 cache-hit。
- 因此：**想让改过的 `CLAUDE.md` 生效且接受前缀代价 → compact/clear/restart**，不是“继续聊自动热更”。

**缓存友好的中途行为（append，不改前缀）：**

- skills / slash commands 以 user message 注入
- plan mode 指令 append
- 子 agent 独立 cache，不污染 parent 前缀

SDK 侧补充：[Modifying system prompts](https://code.claude.com/docs/en/agent-sdk/modifying-system-prompts) 说明 SDK 可把 `CLAUDE.md` **注入 conversation 而非 system prompt**，从而与 system prompt cache 解耦——产品 CLI 的 layer 表仍把 project context 放在 system 之后的稳定层。

### 3.2 Codex CLI

**指令加载（官方 AGENTS 指南）：**

- 启动时构建 instruction chain（global `~/.codex` + project root→cwd 层级；`AGENTS.override.md` 优先）。
- **“once per run; in the TUI this usually means once per launched session”**。
- 文档排查：*If instructions look stale, restart Codex … rebuilds the instruction chain on every run (and at the start of each TUI session)*。
- 有字节上限（默认 `project_doc_max_bytes` 等），过长截断。

**缓存工程（OpenAI cookbook Prompt Caching 201）：**

- Codex agent loop 把 system / tools / sandbox / environment 做成 **稳定、固定顺序** 的前缀。
- 会话中途 runtime 配置变化（如 cwd、approval mode）：**append 新消息**，**不改已有前缀**，以保住 exact-prefix hit。
- 显式列出：*Changes to instructions or system prompts* 是 cache miss 常见原因。

**推断（文档未逐字写 AGENTS 热更）：**

- 与 Claude 同类：**session 快照 + 重启刷新**；未宣传 mid-session 改 `AGENTS.md` 即生效。
- 若实现把 AGENTS 放在前缀且热替换，会与 cookbook 原则冲突——公开材料更支持 **冻结到下次 session**。

### 3.3 Grok Build

**项目规则（官方 / 用户手册）：**

- 文件名：`AGENTS.md`、`AGENT.md`、`CLAUDE.md` 等；以及 `.grok/rules/*.md`（兼 `.claude/rules`、`.cursor/rules`）。
- 发现顺序：global `~/.grok` →（git 内）repo root→cwd 逐层；更深文件后出现、冲突时优先。
- **Session start 自动加载** root→cwd 链。
- **Progressive discovery**：当 read/list/edit 落到初始集合外的目录时，发现该处指令文件并在适用时再读入（子树作用域）。
- `--rules` / `--append-system-prompt`：单次 session **append 到 system prompt**；`--system-prompt-override` 整段替换。
- 无体积硬 cap（手册写 full load）；建议写短。

**缓存（xAI API best practices，适用于 grok 模型，非 Build 专页）：**

1. 稳定 `x-grok-conv-id` / `prompt_cache_key`
2. **Never modify earlier messages — only append**
3. Front-load static content
4. tool call 链路只要前缀不变即可命中

**推断：**

- 启动加载 + 路径 progressive 再读，机制上对齐 Claude 的 **root 冻结 + nested 按需 append**。
- **未找到** “已加载的根 `AGENTS.md` 中途编辑自动热更” 的官方说明；结合 “only append”，更合理的产品行为是：**已注入前缀不原地改**；新发现文件 **append**；要全量刷新则 **新 session**（`/new`/`/clear` 类）。

社区/逆向材料（如 system prompt 泄漏文）提到 agent 被指示 *“New project instruction files were discovered… MUST read these files now”*——支持 **append-on-discovery**，不是改 system 头。

### 3.4 对照表

| 维度 | Claude Code | Codex CLI | Grok Build |
| --- | --- | --- | --- |
| 主文件 | `CLAUDE.md`（+ nested / rules） | `AGENTS.md` / override | `AGENTS.md` + `CLAUDE.md` 等 + rules 目录 |
| 何时首次加载 | Session start | Run / TUI session start | Session start（root→cwd） |
| Mid-session 改**已加载根文件** | **不生效、不砸 cache** | 文档：stale → **restart**；无热更声明 | 无热更声明；倾向冻结（推断） |
| 如何让改动生效 | `/clear`、`/compact`、restart | 新 run / 新 session | 新 session（推断）；nested 未加载前可改 |
| Nested / 子树 | 首次 touch 时 load；进 history | root→cwd 启动时合并（不跨 cwd 外路径动态扩） | 访问新路径时 progressive discover + read |
| 中途“加规则”友好路径 | skill/command **append** | runtime 配置 **append**（cookbook） | `--rules` append；discovery read append |
| 注入位置（cache 友好） | project context 层稳定；尽量不改 | 前缀稳定；避免改 instructions | API 要求 static front + only append |
| 改规则是否自动砸 prefix cache | **否**（因不应用） | 若热改前缀则是；产品倾向不热改 | 若改已发送前缀则是；产品倾向不热改 |

### 3.5 Provider 层（与产品策略独立）

| Provider | 相关规则 |
| --- | --- |
| Anthropic | 精确前缀 + 可选 cache breakpoint；改 system/tools 砸后段 |
| OpenAI | exact prefix；`prompt_cache_key` 改善路由粘性；instructions 变化列在 miss 原因 |
| xAI Grok | exact prefix；**禁止改历史消息**；conv id 路由 |

**物理规则统一：前缀变了就 miss。** 产品差异在于 **要不要在 session 内改前缀**。

## 4. Efficient / reasonable patterns

行业收敛：

1. **Session 冻结项目指令快照**  
   - 好处：长任务、高 cache 命中、规则变更低频。  
   - 代表：Claude 官方 mid-session 行为；Codex “restart if stale”。

2. **强制刷新点与自然断点绑定**  
   - Claude：`/compact`、`/clear`、restart 时从磁盘重载 project context。  
   - 在“任务边界”付一次 cache 重建费，而不是每 turn 热更。

3. **中途增量用 append，不改前缀**  
   - skills、临时规则、approval/cwd 变化、nested discovery。  
   - Codex cookbook 与 Claude skills/plan mode 一致。

4. **Nested progressive load**  
   - monorepo 不全量预载；touch 子树再注入；注入后冻结。  
   - Claude nested；Grok progressive discovery。

5. **把“易变”从 system 前缀挪走**  
   - SDK 可把 CLAUDE.md 放 conversation；CLI 用 layered cache。  
   - 临时指令用用户消息 / `--rules`，不改团队 `AGENTS.md`。

不适用场景：

- 需要 **同 thread 内规则立即覆盖** 且可接受 **整段 cache miss** → 显式 reload / replace system，并在 UX 上提示代价（Claude 对 effort 变更甚至会确认）。
- 安全硬约束 **不能** 依赖 AGENTS 文本；应走 permissions / sandbox。

## 5. Pitfalls

| 坑 | 现象 | 谁在文档里点过 |
| --- | --- | --- |
| 以为改了 `AGENTS`/`CLAUDE.md` 立刻生效 | 模型仍跟旧规则 | Claude 明确；Codex “restart if stale” |
| 为“立刻生效”原地改 system 前缀 | 下一 turn 全量 re-prefill，长 session 极贵 | OpenAI / xAI / Anthropic 通用 |
| 把临时偏好写进团队 AGENTS | 污染所有 session 前缀体积与行为 | 各家 best practice：写短、稳定 |
| nested 已 load 后改磁盘 | 历史里仍是旧内容 | Claude nested 语义 |
| compact 时期望“零 cache 代价” | conversation 层必 miss；规则若变则 project 层也 miss | Claude compact 段 |
| 混淆 session 持久化与规则热更 | resume 后规则是否刷新产品各异 | Codex/Claude 均需按产品核对 |

## 6. Open questions

- Codex 开源/闭源实现里，TUI 是否在任何 slash 命令下 **重编译** AGENTS 并改 system——公开文档只保证 **session start 构建**。
- Grok Build 对 **已加载根 AGENTS** 中途编辑：是否完全冻结，或工具 `read` 后依赖模型自觉——**官方未写**；需用 instrumented build 验证。
- Claude `/compact` 重载的 “unchanged since session started” 是否 content-hash 比较——文档语义是内容未变才 hit，实现细节未公开。
- 三家 resume 跨版本升级后的 system 变化（Claude 明确：升级后 resume 全量 miss）对 AGENTS 层的交叉影响。

## 7. 对“会不会变前缀缓存”的一句话答案

| 产品 | 同 session 只改磁盘上的项目指令 | 新 session / clear /（Claude）compact |
| --- | --- | --- |
| Claude Code | 不应用 → **不砸 cache** | 重载 project context → **可能砸 project 层及之后** |
| Codex | 文档侧视为 stale 直到 restart → **通常不砸**（因不重注入） | 重建 instruction chain → **新前缀** |
| Grok Build | 启动快照 + progressive append；**无热更文档** → 合理默认 **不砸已有前缀** | 新 session 重扫规则 → **新前缀** |

**不是“只有新 session 才会导致前缀缓存变化”**，而是：

- **只有“请求前缀内容变了”才会变 cache。**
- 成熟产品 **故意让 mid-session 改 AGENTS/CLAUDE 不改请求前缀**，从而 **既不生效也不砸 cache**；
- 要生效时走 **clear / compact / 新 session**，那时 **有意付一次** 前缀重建。

## References

1. Anthropic, [How Claude Code uses prompt caching](https://code.claude.com/docs/en/prompt-caching) — layers, mid-session CLAUDE.md freeze, compact reload (accessed 2026-07-21).
2. Anthropic, [Modifying system prompts (Agent SDK)](https://code.claude.com/docs/en/agent-sdk/modifying-system-prompts) — CLAUDE.md injection path vs system prompt.
3. OpenAI, [Custom instructions with AGENTS.md](https://developers.openai.com/codex/guides/agents-md) / ChatGPT Learn mirror — discovery once per run/session; restart if stale.
4. OpenAI, [Prompt Caching 201](https://developers.openai.com/cookbook/examples/prompt_caching_201) — Codex loop: stable prefix; mid-run config via append; instruction changes as miss cause.
5. xAI, [AGENTS.md / Project rules](https://docs.x.ai/build/features/project-rules) — discovery, session rules, full load (updated 2026-07-04).
6. Grok Build user guide (local ship), `12-project-rules.md` — session-start load + progressive discovery outside initial path set.
7. xAI, [Prompt caching best practices](https://docs.x.ai/developers/advanced-api-usage/prompt-caching/best-practices) — only append; front-load static (updated 2026-05-10).
8. Secondary: granda.org reverse notes on Grok Build progressive AGENTS discovery prompt (2026-05); treat as corroboration, not primary.
