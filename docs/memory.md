# 项目规则与持久记忆

本文描述**跨会话语义记忆**与项目软规则，不是会话恢复。

| 概念 | 文档 |
| --- | --- |
| 包地图与分层 | [architecture.md](./architecture.md) |
| 会话账本、`/resume`、checkpoint、compaction | [session-persistence.md](./session-persistence.md) |
| 本功能：`AGENTS.md`、`.eino/memory/`、`/memory` | 本文 |
| 迭代记录 | [iterations/2026-07-21-persistent-memory.md](./iterations/2026-07-21-persistent-memory.md) |
| 行业调研 | [research/persistent-memory-systems-research.md](./research/persistent-memory-systems-research.md) |

`AGENTS.md` 由 **`internal/agent`**（`LoadProjectInstructions`）加载，不是独立 `internal/rules` 包；语义记忆在 **`internal/memory`**。

## 1. 是什么 / 不是什么

**是**：

1. **持久指令**：workspace 根目录 `AGENTS.md`（软规则，有界注入；见下方生命周期）
2. **显式记忆**：用户通过 `/memory add` 写入的项目偏好/事实
3. **自动候选**：空闲 session journal 异步抽取的 *candidate*（低信任）

**不是**：

- `/resume` 或 transcript 回放
- compaction / checkpoint 续接
- 硬权限或 sandbox（仍见 [command-policy.md](./command-policy.md)）
- 跨机器同步或向量数据库

## 2. 三层模型

```text
AGENTS.md          人工维护 · 团队可提交 git
entries (user)     用户显式确认 · 高信任
entries (candidate) 自动抽取 · 注入时标 unverified
summary.md         有界派生视图 · 供 system 注入
```

规则与学习记忆分目录、分配置、分控制面，对齐 Claude Code / Codex 的主流拆法。

## 3. 目录

默认相对 **workspace root**（`[workspace].root` 或进程 cwd）：

```text
<workspace>/
  AGENTS.md                 # 可选；项目指令
  .eino/
    .gitignore              # 默认忽略 memory/
    memory/
      meta.json
      entries.jsonl         # 权威条目（含 tombstone）
      summary.md            # 可重建的注入摘要
```

记忆默认 **不提交 git**。团队共识应写在 `AGENTS.md`，不要依赖个人 candidate。

## 4. 配置

见 `config.example.toml`：

```toml
[rules]
enabled = true
# max_tokens = 8000

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
| `rules.enabled` | true | 是否加载 `AGENTS.md` |
| `rules.max_tokens` | 8000 | 规则注入 token 估算上限 |
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
| `/memory delete <id\|key>` | tombstone 删除 |
| `/memory accept <id>` | candidate → user 信任 |
| `/memory on` / `off` | 开/关注入与读工具 |
| `/memory generate on` / `off` | 开/关自动抽取 |
| `/memory status` | 路径、计数、上次 consolidate |
| `/memory rebuild` | 重建 `summary.md` |

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
2. `AGENTS.md` 块（≤ rules 预算）
3. 记忆 summary 块（≤ memory 预算；candidate 标 *unverified*）

超预算时：**先压 memory summary，再压 AGENTS 尾部**。

### 7.1 生命周期（冻快照保 prefix cache）

Effective system prompt **始终等于** durable thread system（`thread.created` 写入的快照）。Live 层不做可被 `applyThreadState` 冲掉的“幽灵替换”。

| 时机 | 行为 |
| --- | --- |
| `/new`、`/clear`、新建 session | 从磁盘 recompose AGENTS + memory summary，写入 **新** thread 的 durable system |
| **普通 turn** | 使用创建时冻结的 system；**不**每轮重读 `AGENTS.md` |
| **只编辑磁盘上的 `AGENTS.md`** | 当前 session **不生效**（不改请求前缀 → 不砸 prompt cache） |
| **`/resume`** | 沿用该 thread **创建时** durable system；**不**用当前磁盘热更 |
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
- 删除：`status=deleted` tombstone；list/search/summary 均不可见
- `.eino` 为 sandbox **内建保护路径**，worker 不可直接改写记忆权威文件

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
| 规则层级 | 仅根 `AGENTS.md` | Codex/Claude 多级 |

偏离理由见迭代文档。

## 12. 局限与后续

- 无向量检索；检索为关键词/子串
- 无全局 `~` 级记忆层
- 无多级 `AGENTS.md` 合并
- 自动巩固为单阶段 extract，非 Codex 完整两阶段 git consolidate

后续可演进：规则层级、全局偏好 scope、可选向量、更细费用展示。
