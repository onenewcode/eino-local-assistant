# 迭代：持久化记忆（规则 + 显式 + 自动候选）

| 字段 | 值 |
| --- | --- |
| 日期 | 2026-07-21 |
| 状态 | **已交付** |
| 调研依据 | [persistent-memory-systems-research.md](../research/persistent-memory-systems-research.md)；规则层参考 [cli-rules-research.md](../research/cli-rules-research.md) |
| 产品说明 | [memory.md](../memory.md) |

## 1. 目标

为编码 Agent 增加**跨会话语义记忆**能力：

1. 加载 workspace 根 `AGENTS.md` 为软项目指令  
2. 用户 `/memory` 显式记忆与治理  
3. 空闲 session 异步抽取 **candidate**（低信任）并有界注入  

**非目标**：session resume、checkpoint/compaction 当记忆、向量库、多级 AGENTS、agent 可写 memory 工具、跨机器同步。

## 2. 产品决策（grill 锁定）

| 项 | 选择 |
| --- | --- |
| 范围 | 规则 + 显式 + 自动巩固 |
| 交互 | `/memory` 斜杠为主 |
| 落盘 | `<workspace>/.eino/memory/`，默认 gitignore |
| 自动 | 默认开；idle 异步；证据 = journal 已完成 turn |
| 信任 | 自动 = candidate；summary 可注入但标 unverified |
| 注入 | summary ≤ 2.5k + 只读 list/search/read |
| 规则 | 仅根 `AGENTS.md` ≤ 8k tokens |
| 冲突 | 同 key LWW + version |
| 抽取 | 现有 chat model + 严格 JSON |
| 配置 | `[rules]` + `[memory]` + `/memory on\|off` |

## 3. 主流对齐与有意偏离

### 对齐

- 规则 ≠ 自动记忆（Claude / Codex）  
- 会话账本 ≠ 语义记忆  
- 有界 summary + 按需读工具（Codex 读路径）  
- 用户治理面 `/memory`（Claude `/memory`、Codex `/memories`）  
- 删除走读路径；memory 不当硬权限  

### 偏离

| 偏离 | 理由 |
| --- | --- |
| 自动默认开（非 Codex 默认关） | 体验偏 Claude；candidate + 可关 + scan 上限控风险 |
| jsonl 结构化 + 派生 summary（非纯 MEMORY.md 树） | LWW / tombstone / trust 治理 |
| 项目级 `.eino/memory`（非 `~/.codex/memories`） | coding 项目约定优先 |
| 仅根 AGENTS.md | v1 最小可用 |
| agent 只读 memory 工具 | 降注入写污染面 |
| 单阶段 extract | 避免 Codex 两阶段 sub-agent 复杂度 |

## 4. 交付内容

### 4.1 代码

| 包/文件 | 职责 |
| --- | --- |
| `internal/agent` | `LoadProjectInstructions`（AGENTS.md）、`ComposeFullSystemPrompt` / layers；**无**独立 `internal/rules` 包 |
| `internal/memory` | store / summary / extract / consolidator |
| `internal/tools/memory.go` | `memory_list` / `search` / `read` |
| `internal/tui` | `/memory` 命令 |
| `internal/config` | `[rules]` / `[memory]`；`.eino` 保护路径 |
| `cmd/eino-assistant/run_tui.go` | 接线与后台 consolidate loop |

### 4.2 文档

| 文档 | 角色 |
| --- | --- |
| `docs/memory.md` | 用户/运维说明 |
| 本文 | 迭代记录 |
| `README.md` | 入口 |
| `docs/session-persistence.md` | 划界交叉引用 |
| `config.example.toml` | 配置样例 |

## 5. 验收

- [x] 无 `AGENTS.md` 时行为兼容；有则注入并可截断  
- [x] `/memory add` 后 summary 含 claim；delete 后不可见  
- [x] candidate 标 unverified；accept 升格 user  
- [x] `memory off` 不注入；generate 可关  
- [x] `.eino` 内建 protected  
- [x] `docs/memory.md` + 本迭代 + README 入口  
- [x] `go test` / `go build` / 风格检查（交付前跑通）

## 6. 后续

- 多级 AGENTS（全局 / 子目录）  
- 全局用户偏好 scope  
- 可选向量检索  
- consolidator 费用在 `/usage` 更显式展示  
- Codex 风格两阶段 consolidate（若需要更高质量归并）
