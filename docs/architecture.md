# 架构地图

本文件只回答“改什么该去哪里”。行为细节见专题文档：
[会话](./session-persistence.md)、[记忆](./memory.md)、[命令与沙箱](./command-policy.md)、[行业调研](./research/)。

## 入口与主流

```text
cmd/eino-assistant
  -> config + provider + sandbox + tool registry
  -> agent.ReActModel
  -> chat.Session <-> store.ThreadStore
  -> tui.Run (interactive chat) | exec (single durable non-interactive turn)
```

`cmd/eino-assistant/runtime.go` 只负责共享接线；`run_tui.go` 与 `run_exec.go` 分别提供交互和单轮非交互入口。产品行为应归属到对应的 `internal` 包。

`exec [PROMPT]` 创建 session，`exec resume <id> [PROMPT] [--recover]` 经由 `chat.OpenSession` 打开显式 ID 并追加一个新 turn；不提供最近会话选择器，也不直接读取 thread 文件。两种形式在创建或打开 runtime 前读取最多 10 MiB stdin；显式 prompt 和 stdin 同时出现时，stdin 作为标注为不可信 reference data 的 JSON envelope 追加。该标注不是 prompt-injection 或权限边界，硬工具权限仍由 `permissions`、`tools` 与 `sandbox` 强制。resume 的普通打开绝不接管活动 turn 或 pending compaction，只有 `--recover` 将该授权传给 `chat.OpenSession` 的 CAS 恢复。既有 session 的冻结 system prompt 仍然权威，tools、权限、sandbox 和空的 headless session allow/deny 决策使用当前进程接线，并且没有交互 approver。durable session 成功创建或打开后，`exec` 将稳定的 session ID 和 `eino-assistant exec resume <id>` hint 写到 stderr。默认 `--output-format text` 只把成功提交的最终回复写到 stdout；显式 `--output-format json` 在 stdout 写恰好一个、带 `contract_version: 1` 的最终结果对象（成功、可恢复失败和取消均适用）。`--output-format stream-json` 是 cmd 私有、版本化的 JSONL 投影：唯一 stdout writer 为每条记录分配单调 `sequence`，成功创建/打开后先写不透明的 `session.started`，可选 `activity` 仅表示已持久化工具的 started/completed/failed 状态，最后只写一个携带安全 final JSON v1 子集的 `result`。它绝不序列化原始 `chat.TurnEvent`、assistant delta、reasoning 或 tool I/O；管道断开会取消 active turn 且允许 terminal 缺失。JSON 错误详情按稳定的公开 code 映射，不能包含原始 provider/tool/store/context cause；完整诊断仍在既有 stderr/进程错误路径。两种 machine-output 适配可选地投影既有 durable provider usage，但不改变 session/store schema，也不暴露成本、原始 provider/model/tool/context 数据。

## 包边界

| 位置 | 负责 | 不负责 |
| --- | --- | --- |
| `internal/agent` | ReAct、system prompt、AGENTS 指令选择、任务图与 completion controller | 自己管理账本文件或记忆落盘 |
| `internal/chat` | turn 生命周期、流式事件、completion gate、上下文协作 | 任务业务正确性 |
| `internal/store` | thread journal、checkpoint、artifact、resume | 项目记忆 |
| `internal/contextbuild` | prompt 规划与 compaction | 工具执行 |
| `internal/memory` | 项目级语义记忆与 consolidation | `/resume`、权限 |
| `internal/tools` | shell、apply_patch、artifact、memory 只读工具 | TUI 策略展示 |
| `internal/sandbox` | 工具的 OS 隔离边界 | prompt 规则 |
| `internal/config` | TOML 读取、默认值与校验 | 运行时状态 |
| `internal/tui` | 交互、slash、队列、审批桥、状态栏与只读任务进度 | 模型协议 |
| `internal/provider` | OpenAI / Anthropic 模型适配 | 产品流程 |
| `internal/runtimeguard` / `usage` | 单轮预算；token/费用投影 | 权限或账单真相 |

AGENTS 指令加载在 `agent`：workspace 根按 `AGENTS.override.md`、`AGENTS.md` 顺序选择首个有效候选；符号链接会跟随到普通文件目标，去掉 UTF-8 BOM 后仅含空白的候选会跳过。不要新建或恢复 `internal/rules`；`[rules]` 只是配置名称。

## 状态与安全边界

| 面 | 所有者 / 位置 | 生命周期 | 关键边界 |
| --- | --- | --- | --- |
| 硬权限 | `permissions` + `tools` + `sandbox` | 进程 / 每次工具调用 | 不依赖 AGENTS.md 或 memory 文本执行 |
| 会话账本 | `store` + `chat` | 可 `/resume` | journal、artifact 与 checkpoint 是真相 |
| 项目指令 | `agent` 选择 workspace 根 AGENTS 指令 | `/new` / `/clear` 刷新 | system prefix 的软指导；override 与 base 不拼接 |
| 语义记忆 | `memory`，`.eino/memory/` | 跨会话 | 不等于 `/resume`；agent 只读 |
| 复杂任务图 | `agent.TaskController` + `store` | 当前会话，可 `/resume` | `task.state.updated` 是图的恢复投影；工具/artifact 仍是证据真相 |

System prompt 由 `agent.ComposeWithLayers` 组装：persona、工具/任务 policy、可选 AGENTS 指令和记忆摘要。创建 session 后冻结；`/resume`、`/memory`、`/compact` 不重写前缀。

## 复杂任务运行时

`task_plan`、`task_progress`、`task_complete` 只管理控制状态，没有文件或 shell 权限。

- 当前用户原文被 controller 保留为 `user-request` root requirement；计划必须把它映射到场景。
- proof 必须绑定精确匹配且成功的真实 `shell` 结果；图快照只保留引用和少量恢复上下文，完整证据仍在账本。
- 未计划且实际执行的 `shell` / `apply_patch`，以及任务完成或中断后迟到且可能改动工作区的工具结果，都会开启“必须先建计划”的 gate；模型不能直接交付该改动。
- `task_complete` 只是在当前 turn 内的暂定批准；`chat.Session` 在提交最终消息前复查 gate。取消、失败或恢复发现未提交批准、或快照后的 shell/patch 生命周期时，都会关闭交付并要求重规划。
- 任务图不增加专用 slash 命令；状态栏显示紧凑进度，`Ctrl+T` 展开只读任务面板。
- `Esc` / `Ctrl+C` 留下可恢复且不可交付的 `interrupted` 状态；普通后续输入沿用原始需求和未变范围的 proof。只有 `task_plan` 把当前用户原文显式写为 `user-request` 才替换范围；恢复中的 `working` 节点先转为 `needs_replan`，下一次执行时重新收集其 proof。

## 依赖方向

```text
cmd -> tui, chat, agent, tools, memory, config, store, provider
tui -> chat, store, memory, tools
chat -> store, contextbuild, usage
agent -> chat, usage                 (不 import memory/store)
tools -> memory, sandbox, permissions
memory -> store, usage
```

避免反向依赖；尤其不要让 `agent` 持有 `memory.Store`、让 `store` 依赖 memory，或用 prompt 文本代替硬权限。

## 改动路由

| 需求 | 优先位置 |
| --- | --- |
| ReAct、prompt、AGENTS 指令、任务控制 | `internal/agent` |
| 会话、resume、compaction | `internal/chat` + `internal/store` |
| 新工具或权限语义 | `internal/tools` + `sandbox` + [command-policy.md](./command-policy.md) |
| 项目记忆 | `internal/memory` + [memory.md](./memory.md) |
| slash、队列、状态展示 | `internal/tui` |
| 配置字段 | `internal/config` + `config.example.toml` |

改动包边界、持久化分层或安全边界时，同步更新本文件和对应专题文档。代码是最终真相；`docs/research/` 是参考，不是实现合同。
