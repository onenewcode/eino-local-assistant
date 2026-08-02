# 架构概览

本文描述本仓库（eino-local-assistant）的包边界、数据面与控制面，便于改功能时找对位置。

更细的专题文档：

| 主题 | 文档 |
| --- | --- |
| 会话账本 / resume / compaction | [session-persistence.md](./session-persistence.md) |
| 项目指令与持久记忆 | [memory.md](./memory.md) |
| 命令硬权限 | [command-policy.md](./command-policy.md) |
| 迭代记录 | [iterations/](./iterations/) |
| 行业调研（非实现规格） | [research/](./research/) |

---

## 1. 进程与入口

```text
cmd/eino-assistant/
  main / CLI ──► run_tui.go
                    │
                    ├─ config.Load
                    ├─ provider.NewChatModel
                    ├─ tools.DefaultWithOptions (+ sandbox)
                    ├─ agent.NewReActModel
                    ├─ memory.Open
                    ├─ chat.NewSession / OpenSession
                    └─ tui.Run
```

TUI 是主交互面；配置、workspace、session store、memory store 在启动时接线。

---

## 2. 包地图（`internal/`）

| 包 | 职责 | 不负责 |
| --- | --- | --- |
| **agent** | ReAct 循环、persona/tool policy、**加载 AGENTS.md**、拼装 system prompt 层（只收字符串层，不依赖 `memory.Store`） | 会话账本、记忆落盘 |
| **memory** | 项目级语义记忆：store（flock）、summary、candidate 抽取、consolidator | session resume、硬权限 |
| **chat** | 单会话生命周期、turn、与 store/compactor 协作 | 跨会话记忆 |
| **store** | v2 thread journal / checkpoint / artifact | 语义记忆文件 |
| **contextbuild** | prompt 规划、compaction | 工具执行 |
| **tools** | shell / apply_patch / time / artifact / memory_* | 策略 UI |
| **sandbox** | OS worker 边界 | 业务 prompt |
| **config** | TOML 与校验 | 运行时状态 |
| **tui** | 交互、斜杠命令、审批桥 | 模型协议细节 |
| **provider** | OpenAI / Anthropic 适配 | 产品语义 |
| **runtimeguard** | 整轮超时、工具次数 | 权限规则内容 |
| **usage** | token/费用估算与展示辅助 | 计费账单 |

### 2.1 为什么没有 `internal/rules`

早期曾把 `AGENTS.md` 加载拆成独立 `internal/rules` 包。体量过薄且与「permissions 规则」撞名，已 **收进 `internal/agent`**：

- `project_instructions.go` — `LoadProjectInstructions` / `FormatProjectInstructionsBlock`
- `compose_layers.go` — 把 AGENTS + memory summary 叠进 system prompt
- `prompt.go` — persona + 内置 tool guidelines

配置项仍叫 `[rules]`（产品语义：项目软指令开关），实现不在单独 rules 包。

---

## 3. 三条「持久」面（勿混）

```text
┌─────────────────────┐
│ 硬权限 / sandbox    │  permissions + sandbox + approval
│ （模型绕不过）       │
└─────────────────────┘

┌─────────────────────┐
│ 会话账本            │  store: journal / checkpoint / resume
│ （同任务可继续）     │  见 session-persistence.md
└─────────────────────┘

┌─────────────────────┐     ┌─────────────────────┐
│ 项目软指令          │     │ 语义记忆            │
│ AGENTS.md           │     │ .eino/memory/       │
│ agent 加载注入      │     │ memory 包 + /memory │
└─────────────────────┘     └─────────────────────┘
```

| 面 | 数据位置 | 注入方式 | 典型命令 |
| --- | --- | --- | --- |
| 硬权限 | config + 运行时 allowlist | 不进 prompt；工具前强制 | `/permissions` |
| 会话 | `~/.eino-assistant/sessions/` | checkpoint + 热 tail | `/resume` `/compact` |
| 项目指令 | workspace `AGENTS.md` | durable system 冻快照 | `/new` `/clear` 重载；编辑文件 alone 不热更 |
| 语义记忆 | workspace `.eino/memory/` | 有界 summary + 只读工具 | `/memory` 落盘；summary 注入同上边界 |

**`/resume` ≠ 长期记忆。** 长期事实走 memory；任务续聊走 session。

---

## 4. System prompt 组装

```text
ComposeWithLayers(persona, LayerOptions)
  = persona
  + ToolUsagePolicy          (agent/prompt.go，产品固定)
  + Project instructions     (AGENTS.md，可选)
  + Persistent memory block  (summary.md，可选；candidate 标 unverified)
```

预算：`[rules].max_tokens`（默认 8k）、`[memory].max_summary_tokens`（默认 2.5k）。  
不可变指令过预算时优先保住 persona + tool policy（由 session/planner 侧约束）。

**冻结与刷新**（prefix cache）：创建与 `/new`/`/clear` 写入 durable system；`/resume`、`/memory`、`/compact` **均不**改 system 前缀。Effective system 始终等于 ledger。详见 [memory.md §7.1](./memory.md)。

---

## 5. 工具面

Codex 子集 + 产品辅助：

| 工具 | 包 |
| --- | --- |
| `shell` / `apply_patch` | tools + sandbox |
| `get_current_time` | tools |
| `read_artifact` | tools（thread 作用域） |
| `memory_list` / `search` / `read` | tools → memory.Store（只读） |

写入记忆：仅 **`/memory`** 与 **consolidator**，agent 无 `memory_write`。

---

## 6. 依赖方向（应保持）

```text
cmd ──► tui, chat, agent, tools, memory, config, store, provider
tui ──► chat, store, memory, tools
chat ──► store, contextbuild, usage
agent ──► usage (token 估算)；不 import memory
tools ──► memory, sandbox, permissions
memory ──► store (consolidator 只读 journal), usage
cmd/tui 负责：Summary → FormatMemoryBlock → ComposeWithLayers
```

避免：

- `agent` 依赖 `memory.Store`（只收已渲染的 prompt 段）
- `memory` 依赖 `tui` / `agent` 循环
- `store` 依赖 `memory`（会话与记忆文件分离）
- 把硬权限写进 AGENTS.md / memory 文本当唯一执行手段
- candidate 覆盖 user 信任条目；抽取失败 mark processed
---

## 7. 配置面

| 段 | 作用 |
| --- | --- |
| `[model]` / pricing / context | 模型与压缩预算 |
| `[assistant]` | persona |
| `[workspace]` | 路径钳制根 |
| `[permissions]` / `approval_policy` | 硬权限 |
| `[sandbox]` | worker 边界；内建保护含 `.eino` |
| `[rules]` | 是否加载 AGENTS.md、token 上限 |
| `[memory]` | 注入 / 自动抽取 / idle 等 |
| `[storage]` | session data_dir |
| `[runtime]` / `[tools.*]` / `[ui]` | 轮次预算、工具限流、UI |

样例：`config.example.toml`。

---

## 8. 扩展指南（改哪里）

| 想做的事 | 优先改 |
| --- | --- |
| 新 slash 命令 | `internal/tui/slash.go` + `model.go` |
| 新工具 | `internal/tools` + registry + prompt Tool Guidelines |
| 多级 AGENTS / 更多指令文件 | `internal/agent/project_instructions.go` |
| 记忆 schema / 巩固策略 | `internal/memory` |
| 会话事件 / resume 行为 | `internal/store` + `chat` + session-persistence 文档 |
| 权限语义 | `internal/tools` permissions + command-policy 文档 |
| 包边界或分层变化 | **先更新本文**，再改代码 |

---

## 9. 文档与代码的关系

- `docs/research/*`：行业观察，**不是**本仓库实现合同  
- `docs/iterations/*`：某次交付的决策与验收  
- `docs/memory.md` / `session-persistence.md` / `command-policy.md`：用户/运维可依赖的行为说明  
- 本文：开发者地图；与实现漂移时以代码为准并回写本文
