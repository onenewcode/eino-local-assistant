# 架构地图

本文件只回答“改什么该去哪里”。行为细节见专题文档：
[会话](./session-persistence.md)、[记忆](./memory.md)、[命令与沙箱](./command-policy.md)、[行业调研](./research/)。

## 入口与主流

```text
cmd/eino-assistant
  -> config + provider + sandbox + tool registry
  -> agent.ReActModel
  -> chat.Session <-> store.ThreadStore
  -> tui.Run (interactive chat) | exec (single durable or ephemeral non-interactive turn)
```

`cmd/eino-assistant/runtime.go` 只负责共享接线；`run_tui.go` 与 `run_exec.go` 分别提供交互和单轮非交互入口。产品行为应归属到对应的 `internal` 包。`exec --output-schema FILE` 由 cmd 预加载 JSON Schema，并通过 `internal/chat` 的可选 final-response validator 在最终 assistant Content 生成后、durable commit 前做本地校验；它不向 provider 注入 `response_format`，也不校验 ReAct 中间响应。

`exec [PROMPT]` 创建 durable session；fresh `exec --ephemeral [PROMPT]` 则创建一个明确的空临时 `ThreadStore`，只在本次进程内运行，并在 runtime `Close` 时删除临时根目录。ephemeral 只保证 session ledger 不持久化，不回滚工具副作用，也不清除或回滚项目级 semantic memory。`exec resume <id> [PROMPT] [--recover]` 经由 `chat.OpenSession` 打开显式 ID 并追加一个新 turn，`exec resume --last [PROMPT] [--recover]` 则显式选择当前配置 `storage.data_dir` 中 `ListThreads` 排序后的最近 session 再走同一打开路径；二者都可加 `--ephemeral`，此时在源 thread 锁下把选定 durable session 的 journal、materialized state/meta、checkpoints 和 artifacts 快照到临时 `ThreadStore`，再只对临时账本调用 `chat.OpenSession`，源 session 不接收 turn/journal/state/checkpoint/artifact 写入，源 locks 目录也不复制。ephemeral `--last` 使用不修复 durable projections 的只读列表选择；普通 durable `--last` 的既有 `ListThreads` 语义不变。ephemeral resume 不宣称 fork 语义，只保证本次运行不持久化 session ledger。`--last` 不做 cwd/project 过滤、不排除活动会话，也不隐式恢复；显式 ID 仍是稳定身份，当前实现没有额外的并发 single-writer 合同，也不直接读取 thread 文件。两种形式在创建或打开 runtime 前读取最多 10 MiB stdin；显式 prompt 和 stdin 同时出现时，stdin 作为标注为不可信 reference data 的 JSON envelope 追加。该标注不是 prompt-injection 或权限边界，硬工具权限仍由 `permissions`、`tools` 与 `sandbox` 强制。resume 的普通打开绝不接管活动 turn 或 pending compaction，只有 `--recover` 将该授权传给 `chat.OpenSession` 的 CAS 恢复；ephemeral resume 的恢复也只发生在临时账本。既有 session 的冻结 system prompt 仍然权威，tools、权限、sandbox 和空的 headless session allow/deny 决策使用当前进程接线，并且没有交互 approver。durable session 成功创建或打开后，`exec` 将稳定的 session ID 和 `eino-assistant exec resume <id>` hint 写到 stderr；ephemeral 不输出可恢复 session ID 或 hint。默认 `--output-format text` 只把成功提交的最终回复写到 stdout；`--output-last-message FILE` 只在 Ask 成功提交后用目标同目录临时文件写入、Sync 并 rename 原子替换，失败/取消/文件写入失败不覆盖旧目标；显式 `--output-format json` 在 stdout 写恰好一个、带 `contract_version: 1` 的最终结果对象（成功、可恢复失败和取消均适用），ephemeral 的 `session` 固定为 `persistent: false, id: null`。`--output-format stream-json` 是 cmd 私有、版本化的 JSONL 投影：唯一 stdout writer 为每条记录分配单调 `sequence`，成功创建/打开后先写不透明的 `session.started`，并仅在这条首记录广告固定顺序的 `capabilities: ["session_started_v1", "activity_v1", "terminal_result_v1", "usage_v1"]`；这些值描述当前公开的 session-started、activity、terminal-result 和 usage 能力，是 capability advertisement 而不是版本协商。消费者应检测已知 capability 并忽略未知值，旧消费者可以忽略新增字段。可选 `activity` 仅表示已持久化工具的 started/completed/failed 状态，`result` 只携带安全 final JSON v1 子集；二者都不重复 `capabilities`。ephemeral 的 `session.started` 与 `result` 也固定为 `persistent: false, id: null`。它绝不序列化原始 `chat.TurnEvent`、assistant delta、reasoning 或 tool I/O；管道断开会取消 active turn 且允许 terminal 缺失。JSON 错误详情按稳定的公开 code 映射，不能包含原始 provider/tool/store/context cause；完整诊断仍在既有 stderr/进程错误路径。两种 machine-output 适配可选地投影既有 durable provider usage，但不改变 session/store schema，也不暴露成本、原始 provider/model/tool/context 数据。headless 成功退出 `0`，普通输入/启动/运行/取消失败退出非零；明确由 `SIGTERM` 导致的取消退出 `143`。进程退出状态独立于 JSON/JSONL 的 `status`，不增加 `exit_code` 字段，也不复制 Gemini 的细分退出码；TUI 的 `Ctrl+C` 语义由 `run_tui.go` 单独负责。

Headless `exec` accepts `-o FILE` as an alias for the existing `--output-last-message FILE` option on fresh and resume (including `--last`, and therefore fresh `--ephemeral`). The two spellings share exactly the existing post-commit, schema-successful output path: a same-directory temporary file is written, synced, and renamed; failed, cancelled, schema-invalid, or output-write-failed turns do not replace an existing target. Supplying both spellings is a stable input error checked before opening a session or calling the model. This documents this repository's contract only; it does not claim identical path or atomicity implementation to another product.

Headless `exec` also accepts `-m, --model MODEL` on fresh and resume paths, including `--last` and ephemeral runs. The override is applied before provider startup for the current invocation; it changes neither the session identity nor prior transcript/source-snapshot content.

## 包边界

| 位置 | 负责 | 不负责 |
| --- | --- | --- |
| `internal/agent` | ReAct、system prompt、用户/项目 AGENTS 指令选择、任务图与 completion controller | 自己管理账本文件或记忆落盘 |
| `internal/chat` | turn 生命周期、流式事件、completion gate、上下文协作 | 任务业务正确性 |
| `internal/store` | thread journal、checkpoint、artifact、resume | 项目记忆 |
| `internal/contextbuild` | prompt 规划与 compaction | 工具执行 |
| `internal/memory` | 项目级语义记忆与 consolidation | `/resume`、权限 |
| `internal/tools` | shell、apply_patch、artifact、memory 只读工具 | TUI 策略展示 |
| `internal/sandbox` | 工具的 OS 隔离边界 | prompt 规则 |
| `internal/config` | TOML 读取、默认值与校验 | 运行时状态 |
| `internal/tui` | 交互、slash、队列、审批桥、状态栏与只读任务进度 | 模型协议 |
| `cmd/eino-assistant` headless output | `exec` 的 text/json/stream-json 投影、`--output-schema` 文件预加载与最终响应 delivery | provider structured-output 请求、ReAct 中间响应 schema、session/store schema |
| `internal/provider` | OpenAI / Anthropic 模型适配 | 产品流程 |
| `internal/runtimeguard` / `usage` | 单轮预算；token/费用投影 | 权限或账单真相 |

AGENTS 指令加载在 `agent`：用户 home `~/.eino-assistant` 与 workspace 每层都按 `AGENTS.override.md`、`AGENTS.md` 顺序选择首个有效候选；符号链接会跟随到普通文件目标，去掉 UTF-8 BOM 后仅含空白的候选会跳过。用户块与项目块使用独立预算。不要新建或恢复 `internal/rules`；`[rules]` 只是配置名称。

## 状态与安全边界

| 面 | 所有者 / 位置 | 生命周期 | 关键边界 |
| --- | --- | --- | --- |
| 硬权限 | `permissions` + `tools` + `sandbox` | 进程 / 每次工具调用 | 不依赖 AGENTS.md 或 memory 文本执行 |
| 会话账本 | `store` + `chat` | 可 `/resume` | journal、artifact 与 checkpoint 是真相 |
| 用户/项目指令 | `agent` 选择 home 与 workspace AGENTS 指令 | `/new` / `/clear` 刷新 | system prefix 的软指导；override 与 base 不拼接；用户和项目预算独立 |
| 语义记忆 | `memory`，`.eino/memory/` | 跨会话 | 不等于 `/resume`；agent 只读 |
| 复杂任务图 | `agent.TaskController` + `store` | 当前会话，可 `/resume` | `task.state.updated` 是图的恢复投影；工具/artifact 仍是证据真相 |

System prompt 由 `agent.ComposeWithLayers` 组装：persona、工具/任务 policy、用户 instructions、项目 AGENTS 指令和记忆摘要。创建 session 后冻结；`/resume`、`/memory`、`/compact` 不重写前缀。用户 root 在 runtime 构造时固定为 `os.UserHomeDir()/.eino-assistant`，不随 `storage.data_dir` 改变；resume 继续使用 thread 创建时冻结的 system prompt。

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

Headless `exec --json` 是 Codex-compatible 的 CLI 别名，实际映射到同一 `--output-format stream-json` JSONL 投影。它覆盖 fresh、resume、`--last` 和 ephemeral fresh，不能改变 `stream_version`、`sequence`、`session.started`、activity、最终 `result` 或错误/退出 contract；`--json` 与显式 `--output-format stream-json` 等价，与显式 `json` 或 `text` 是 input error。
