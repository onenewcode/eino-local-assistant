# 项目规则与持久记忆

本文描述**跨会话语义记忆**与项目软规则，不是会话恢复。

| 概念 | 文档 |
| --- | --- |
| 包地图与分层 | [architecture.md](./architecture.md) |
| 会话账本、`/resume`、checkpoint、compaction | [session-persistence.md](./session-persistence.md) |
| 本功能：AGENTS 指令、`.eino/memory/`、`/memory` | 本文 |
| 迭代记录 | [iterations/2026-07-21-persistent-memory.md](./iterations/2026-07-21-persistent-memory.md) |
| 用户级 instructions 迭代 | [iterations/2026-08-04-user-global-instructions.md](./iterations/2026-08-04-user-global-instructions.md) |
| 行业调研 | [research/persistent-memory-systems-research.md](./research/persistent-memory-systems-research.md) |

用户目录 `~/.eino-assistant/` 与 workspace 的 `AGENTS.override.md` / `AGENTS.md` 均由 **`internal/agent`**（用户/项目 loader）选择加载，不是独立 `internal/rules` 包；语义记忆在 **`internal/memory`**。

## 1. 是什么 / 不是什么

**是**：

1. **项目指令**：workspace 根目录 `AGENTS.override.md` 或 `AGENTS.md`（软规则，有界注入；见下方选择和生命周期）
2. **用户指令**：用户 home 下 `~/.eino-assistant/AGENTS.override.md` 或 `AGENTS.md`（home scope、软规则、独立预算）
3. **显式记忆**：用户通过 `/memory add` 写入的项目偏好/事实
4. **自动候选**：空闲 session journal 异步抽取的 *candidate*（低信任）

**不是**：

- `/resume` 或 transcript 回放
- compaction / checkpoint 续接
- 硬权限或 sandbox（仍见 [command-policy.md](./command-policy.md)）
- 跨机器同步或向量数据库

## 2. 三层模型

```text
AGENTS.override.md / AGENTS.md  人工维护 · 团队文件或本地覆盖
entries (user)     用户显式确认 · 高信任
entries (candidate) 自动抽取 · 注入时标 unverified
summary.md         有界派生视图 · 供 system 注入
```

规则与学习记忆分目录、分配置、分控制面，对齐 Claude Code / Codex 的主流拆法。

## 3. 目录

项目目录默认相对 **workspace root**（`[workspace].root` 或进程 cwd）；用户目录固定在 home 下：

```text
~/.eino-assistant/
  AGENTS.override.md        # 可选；有效时替代同目录 AGENTS.md
  AGENTS.md                 # 可选；用户级软指令

<workspace>/
  AGENTS.override.md        # 可选；有效时替代同目录 AGENTS.md
  AGENTS.md                 # 可选；共享项目指令
  .eino/
    .gitignore              # 默认忽略 memory/
    memory/
      meta.json
      entries.jsonl         # 权威条目（含 tombstone）
      summary.md            # 可重建的注入摘要
```

语义记忆目录默认 **不提交 git**。团队共识应写在 `AGENTS.md`，不要依赖个人 candidate；仅本地需要的项目指令可写 `AGENTS.override.md` 并由项目自行决定是否 gitignore。

同一 workspace 根目录至多选择一个指令文件，候选顺序为 `AGENTS.override.md`、`AGENTS.md`。有效候选必须在解析符号链接后指向普通文件，且内容去掉开头的 UTF-8 BOM 后不能仅含空白；目录、FIFO、空白内容等会跳过。符号链接目标不要求位于 workspace 内，因此应把链接目标也视为会发送给模型的可信项目配置。

候选顺序和“同目录至多一个”对齐 Codex；Codex 固定源码也明确允许符号链接。本项目进一步把“空白 override 回退 base”定义为稳定合同，而 Codex 的公开文档与项目级源码在这个边界上仍有差异。Claude Code 则会把 `CLAUDE.local.md` 追加在 `CLAUDE.md` 后，而不是替换 base。

## 4. 配置

见 `config.example.toml`：

```toml
[rules]
enabled = true
# max_tokens = 8000
# global_max_tokens = 4000

[memory]
enabled = true              # 注入 summary + memory_* 读工具
generate = true             # 空闲自动抽取
# max_summary_tokens = 2500
# idle_after = "6h"
# max_rollouts_per_scan = 2
# scan_max_age = "10d"
```

| 字段 | 默认 | 含义 |
| --- | --- | --- |
| `rules.enabled` | true | 是否选择并加载 workspace 根 AGENTS 指令 |
| `rules.max_tokens` | 8000 | 规则注入 token 估算上限 |
| `rules.global_max_tokens` | 4000 | 用户 home 指令注入 token 估算上限；独立于项目规则预算 |
| `memory.enabled` | true | 是否注入 summary / 暴露读工具 |
| `memory.generate` | true | 是否后台自动抽取 |
| `memory.max_summary_tokens` | 2500 | summary 预算 |
| `memory.idle_after` | 6h | session 空闲多久才可抽取 |
| `memory.max_rollouts_per_scan` | 2 | 每轮扫描最多处理几个 thread |
| `memory.scan_max_age` | 10d | 忽略过旧 session |

运行时也可用 `/memory off` 与 `/memory generate off` 覆盖开关（写入 `meta.json`）。

## 5. 斜杠命令

| 命令 | 作用 |
| --- | --- |
| `/memory` / `/memory list` | 列出 active 条目 |
| `/memory add <text>` | 用户写入（高信任） |
| `/memory add key=slug <text>` | 指定 key |
| `/memory update <id\|key> <text>` | 纠正 active 条目；`edit` / `correct` 为别名 |
| `/memory delete <id\|key>` | tombstone 删除 |
| `/memory accept <id>` | candidate → user 信任 |
| `/memory on` / `off` | 开/关注入与读工具 |
| `/memory generate on` / `off` | 开/关自动抽取 |
| `/memory status` | 路径、计数、上次 consolidate |
| `/memory rebuild` | 重建 `summary.md` |
| `/memory reset --confirm` | 清空当前项目语义记忆与抽取元数据；保留 session/thread 和开关 |

## 6. 只读工具

| 工具 | 作用 |
| --- | --- |
| `memory_list` | 列出 active（可 filter trust） |
| `memory_search` | 子串检索 key/claim |
| `memory_read` | 按 id 或 key 读取 |

**写入**不经 agent 工具：仅 `/memory` 与 consolidator。这降低提示注入污染长期记忆的面。

## 7. 注入与预算

system prompt 组装顺序：

1. 用户 persona + 产品 tool policy（优先保留）
2. 用户 home 的 AGENTS 指令块（≤ `rules.global_max_tokens`）
3. workspace 到 startup cwd 的 AGENTS 指令块（≤ `rules.max_tokens`）
4. 记忆 summary 块（≤ memory 预算；candidate 标 *unverified*）

用户和项目指令预算独立计算；每一层使用 rune-safe 截断，不把用户块计入项目预算。

### 7.1 生命周期（冻快照保 prefix cache）

Effective system prompt **始终等于** durable thread system（`thread.created` 写入的快照）。Live 层不做可被 `applyThreadState` 冲掉的“幽灵替换”。

| 时机 | 行为 |
| --- | --- |
| `/new`、`/clear`、新建 session | 从磁盘重新选择 home 与项目 AGENTS 指令并组合 memory summary，写入 **新** thread 的 durable system |
| **普通 turn** | 使用创建时冻结的 system；**不**每轮重读 AGENTS 指令 |
| **只编辑磁盘上的 AGENTS 指令文件** | 当前 session **不生效**；下一次 fresh/new/clear 会重读 |
| **`/resume`** | 沿用该 thread **创建时** durable system；不重组当前 home 或项目文件 |
| **`/memory` 写入** | 落盘立即生效；`memory_*` 工具可见；**system 注入**等到 `/new` 或 `/clear` |
| **`/compact`** | 只压缩对话历史；**不**重载 AGENTS/memory 进 system（与 Claude compact 重读 CLAUDE.md 不同：本仓库 ledger 无 durable system 修订事件） |

边界刷新点：**仅** `/new`、`/clear`（及进程新建 session）。  
动机：改 system 前缀会打穿 provider prefix cache；无 durable 热更路径时，宁可不刷也不做 ephemeral live rewrite。

## 8. 自动巩固

- 进程运行期间后台定时扫描 `storage.data_dir/sessions`
- 仅处理：已空闲 `idle_after`、未超 `scan_max_age`、尚未 processed、非当前活动 thread 的会话
- 用现有 chat model 做严格 JSON 抽取 → `trust=candidate`
- 失败写入 `meta.last_error`；可用 `/memory status` 查看
- 关闭：`memory.generate = false` 或 `/memory generate off`

## 9. 信任、冲突、删除

| trust | 含义 |
| --- | --- |
| `user` | 用户显式或 accept |
| `candidate` | 自动抽取；summary 中标 unverified |

- 同 `key`：**user 层 LWW**；**candidate 不得覆盖 active user**（会拒绝写入）；user 写入可 supersede candidate
- 纠正：`/memory update` 按 id/key 原子定位 active 条目，保留原 key，写入新的 user 版本，并把旧版本标为 `superseded`；旧值立即退出 list/search/summary
- 删除：`status=deleted` tombstone；list/search/summary 均不可见
- `.eino` 为 sandbox **内建保护路径**，worker 不可直接改写记忆权威文件

### 9.1 全量重置

`/memory reset` 是高影响命令，必须在 idle 状态显式运行 `/memory reset --confirm`。不带 `--confirm` 只显示确认用法，不修改磁盘。

确认后，命令会：

- 清空当前 workspace 的全部语义记忆版本（包括 active、superseded 与 tombstone），并重建空 `summary.md`
- 清空 processed/claimed thread、上次 consolidate 和错误等抽取元数据，使状态回到可重新抽取的起点
- 保留 `/memory on|off` 与 `/memory generate on|off` 的当前值
- 保留 session/thread journal；`/resume` 与会话审计不受影响

reset 落盘后，`memory_*` 读工具立即看不到旧条目；但当前 thread 的 system prompt 是创建时的冻结快照，已经注入其中的旧 summary 不会热替换，需 `/new` 或 `/clear` 才会得到空的 system memory 层。

这不是历史来源的物理擦除。由于 session journal 被保留且 processed/claimed 标记被清空，若 `memory.generate` 仍开启，符合年龄与空闲条件的旧 session 以后可能再次被抽取。需要保持空记忆时，先运行 `/memory generate off`，再执行 reset；需要删除会话来源时使用会话自己的删除/保留策略，不能用 memory reset 代替。

## 10. 安全注意

- 记忆文本是 **data / soft context**，不是权限系统
- candidate 可能含模型幻觉；高风险动作前应核实或 `/memory accept`
- 抽取输入做 best-effort secret redaction，**不构成**完整脱敏保证
- 间接提示注入：外部内容若进入 transcript，可能被抽成 candidate——保持 generate 可控、定期 `/memory list`

## 11. 与主流对照（摘要）

| 点 | 本产品 | 主流 |
| --- | --- | --- |
| 规则与记忆分离 | 是 | Claude / Codex 一致 |
| 有界 summary + 按需读 | 是 | Codex 读路径；Claude 主题文件 |
| 自动默认 | 开（偏 Claude） | Codex 默认关 |
| 落盘 | 项目 `.eino/memory` | Claude 项目本地；Codex 常在用户 home |
| agent 写工具 | 无（只读） | 部分产品允许写 |
| 规则层级 | 仅 workspace 根 override/base 二选一 | Codex/Claude 多级 |

偏离理由见迭代文档。

## 12. 局限与后续

- 无向量检索；检索为关键词/子串
- 无全局 `~` 级记忆层
- 用户层只做 `~/.eino-assistant` 的 override/base 二选一；项目层支持 workspace root 到 startup cwd 的祖先链；尚无子目录 lazy load、reload 命令或权限语义
- 自动巩固为单阶段 extract，非 Codex 完整两阶段 git consolidate

后续可演进：用户全局与目录级规则层级、全局偏好 scope、可选向量、更细费用展示。
