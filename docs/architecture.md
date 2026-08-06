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

会话模型身份与会话内替换的最小契约由 `store`、`chat` 和上层 runtime 分层承担：`internal/store` 可选提供 `ThreadModelRepository` 扩展，`ThreadStore.SetThreadModel` 以普通 thread mutation 记录 `model.changed`；`internal/chat.Session.ReplaceModel` 只允许在 idle 调用，成功后保留 thread ID、冻结 system prompt、transcript、active checkpoint、context 和累计 usage，active turn 或 pending compaction 时拒绝。生产 runtime 已将这条路径接入 TUI：`/model [name]` 带名称时接受 free-form 名称，省略名称或 `Alt+P` 则使用 `[model]` 下显式配置的 catalog picker；alias 在 provider 构造前解析为 canonical name，picker 的 display label 与声明 capability 只用于选择和展示。所有路径先构造完整的 provider/ReAct/compactor bundle，再调用 `ReplaceModel`，成功后更新同一会话的 runtime snapshot；构造或提交失败时保留旧 session/runtime。provider 实例由上层构造，`store` / `chat` 不负责 provider discovery、catalog refresh 或 health。该契约不重写全局配置。

TUI 的 `/model` 与 `Alt+P` picker 只在 idle、无 pending approval、无运行中的 side question 时执行；busy、compacting 或其他非 idle 情况拒绝且不进入 FIFO。没有显式 catalog 时 picker 会明确提示使用 `/model <name>`，而未知 free-form 名称仍可直接提交。runtime-owned 的 `/resume` 会先读取目标 thread 的 durable model identity，再据此构造 provider bundle；成功打开后才替换 active session 与 runtime snapshot，失败时保留旧 session/runtime。当前 catalog 是本地声明，不提供 live provider discovery、health/entitlement refresh、reasoning effort 二级选择或完整的 Codex/Claude 模型选择对齐；`-m` / `--model` 仍是启动时的本次进程 override。

TUI 会话内权限模式为 `ask`、`auto` 和 `plan`。`plan` 是 TUI 进程级临时 read-only phase，不是配置持久化，也不是完整的 plan artifact workflow；它不生成或持久化任务计划、计划文件或独立 plan 账本。`/permissions` 无参数是只读报告；`/permissions plan` 只在 idle 时进入，`/permissions ask` 与 `/permissions auto` 只在 idle 时退出 plan，busy、compacting 或其他非 idle 状态拒绝且不进入 FIFO，并保留 composer draft。`auto` 只在 `internal/tools` 的实际授权边界自动处理 `DecisionAsk`。plan 下 `apply_patch` 无条件返回结构化 `plan_read_only` 拒绝；shell 只有 `PermissionSet` 原始 `DecisionAllow` 才能进入 enforced OS read-only worker，hard deny 仍按既有策略拒绝，其他非原始 allow 不进入 ask；缺少或无法证明该 sandbox 时 fail-closed，不无 sandbox 执行，也不请求 host escalation。所有模式仍受硬 deny、workspace/path clamp、sandbox 与 host escalation 上限约束。模式不写配置、session ledger 或 resume 数据。

`/plan` 与 `/permissions plan` 进入同一个临时 read-only phase，复用相同的 idle-only admission、状态展示与执行边界；它不新增 artifact、持久化或权限语义。`/plan` 无参数只切换到 plan；`/plan <prompt>` 仅在 idle 时先切到 plan，再通过同一正常 TUI turn 路径执行一次 prompt，并在 turn 结束后保持 plan；精确的 `/plan exit` 与 `/plan ask` 恢复 ask，`/plan auto` 恢复 auto。所有 `/plan` 形式在 busy、compacting 或 pending approval 时立即拒绝、不排队、不取消当前操作并保留 composer draft；prompt 启动失败时不启动 turn，模式仍保持 plan。这仍不是持久 plan workflow。

Headless `exec` 不使用该 TUI 状态，也没有 `/permissions` 交互命令；其 `approval_policy` 继续是启动时静态语义，默认 execute 行为不变。`on-request` 在没有交互 approver 时对需要审批的请求 fail-closed，`never` 仅保留既有的 `DecisionAsk` 自动处理，不能绕过硬权限、路径钳制或 sandbox。该 TUI 切换不改变 headless 行为。

TUI `/btw <question>`（别名 `/side <question>`）由 `internal/tui` 接收并通过 `cmd/eino-assistant` 的 `SideQuestion` callback 发起一次旁路模型请求。它是本仓库的安全子集：主 turn 不被打断，旁路问题不进入 FIFO queue，多个问题可并发。请求使用当前 active session 的 frozen system prompt 和 transcript 作为 reference-only；不调用 tools 或 subagents，不写主 ledger、`usage` 或 `journal`，不修改文件、git state、configuration 或 permissions。结果只进入 TUI 的 side-only display，错误和空回答也可见；没有 callback 的嵌入调用方显示 unavailable。它不是完整持久 fork，也不提供独立 session、ledger 或 resume；Codex/Claude 的行为只作为研究参考，不宣称完全等价。

`internal/store` 提供可选的 `ThreadForkRepository.ForkThread` 与 `ThreadForkBeforeFirstRepository.ForkThreadBeforeFirstTurn` source-preserving primitive；`internal/chat.Session.Fork` 与 `Session.ForkBeforeFirstTurn` 负责用 source 的模型、frozen system prompt 与会话配置发布并打开 child，TUI `/fork` 是当前用户入口。`/fork` 仅允许 idle、无参数，按最新完整 `turn.committed` 自动生成 child ID；child 成功打开前 source 仍是 active session 且 source ledger 不写入，打开成功后才切换到 child，并清理旧 queue、side、tool/reasoning card 与 task UI。child 继承 source title 和 frozen system prompt；打开失败时 source 仍 active（已发布的 child ledger 不回滚）。TUI 另提供 idle 两阶段 `Esc` backtrack：从有可见 user prompt 的 committed history 中选择边界，首个 prompt 通过显式 before-first API 创建空 committed prefix 的 source-preserving child，后续 prompt 仍以之前的 committed turn 为边界，并把 prompt 回填到 child composer，不写入 child transcript。普通 fork 的空边界仍表示 latest。backtrack 与 `/fork` 都不实现 destructive rewind，也不回滚 source、workspace、Git、进程、网络或其他外部副作用，详细边界见 [会话持久化](./session-persistence.md)。

## 包边界

| 位置 | 负责 | 不负责 |
| --- | --- | --- |
| `internal/agent` | ReAct、system prompt、用户/项目 AGENTS 指令选择、任务图与 completion controller | 自己管理账本文件或记忆落盘 |
| `internal/chat` | turn 生命周期、流式事件、completion gate、上下文协作、显式 steer admission/expected-ID 校验与成功 commit 结算、`Session.Fork` / `Session.ForkBeforeFirstTurn` child session 打开，以及 idle-only 的 `Session.ReplaceModel` 模型绑定替换 | 任务业务正确性、provider 构造、catalog/picker、workspace/Git 回滚 |
| `internal/store` | thread journal、checkpoint、artifact、resume，以及可选的 V1 source-preserving `ThreadStore.ForkThread` / `ForkThreadBeforeFirstTurn` ledger primitives；可选的 `ThreadModelRepository` / `ThreadStore.SetThreadModel` 以 `model.changed` 保存 thread 模型身份 | 项目记忆；provider 构造；catalog/picker；全局配置重写；TUI 命令编排；workspace、git 或外部副作用回滚 |
| `internal/contextbuild` | prompt 规划与 compaction | 工具执行 |
| `internal/memory` | 项目级语义记忆与 consolidation | `/resume`、权限 |
| `internal/tools` | shell、apply_patch、artifact、memory 只读工具；权限规则求值及 ask/auto/plan 在工具授权边界的应用 | TUI 策略展示；配置与 TUI 命令编排 |
| `internal/sandbox` | 工具的 OS 隔离边界 | prompt 规则 |
| `internal/config` | TOML 读取、默认值与校验；静态 `approval_policy`；`model.catalog` 的显式 label/alias/lifecycle/capability 声明；可选的 provider/model-specific opaque `model.reasoning_effort` | TUI 会话运行时状态；provider discovery/health；会话内模型切换不重写全局配置 |
| `internal/tui` | 交互、slash、本地 FIFO 队列（列表、`drop`、`edit`、`clear`、`resume`）、显式 `/steer` 命令入口、审批桥、idle-only `/permissions ask|auto|plan` 与 `/plan [<prompt>|exit|ask|auto]` 命令入口、idle-only `/model [name]` 命令入口、显式 catalog picker 与 `Alt+P`、状态栏与权限模式展示、只读任务进度与 `/goal` 投影、side-only 旁路结果展示/并发调度、idle-only `/fork` session 切换，以及 Esc backtrack selector/session 切换 | 模型协议、provider 构造、工具授权真相、旁路请求的模型调用、provider discovery/health/capability enforcement、workspace/Git 回滚；队列不持久化，steer 核心语义由 `chat` / `agent` 负责 |
| `cmd/eino-assistant` runtime / headless output | 共享 runtime 接线（包括 TUI 与 side-effecting tools 的同一会话模式状态）、provider 构造、启动时模型 override、catalog alias 解析、TUI idle model replacement 与 runtime-owned resume 的模型身份选择、picker DTO 接线、旁路问题的一次性只读模型调用，以及 `exec` 的 text/json/stream-json 投影、`--output-schema` 文件预加载与最终响应 delivery | provider structured-output 请求、ReAct 中间响应 schema、session/store schema、provider discovery/health、headless 的动态 TUI 模式 |
| `internal/provider` | OpenAI / Anthropic 模型适配；将规范化后的非空 `model.reasoning_effort` 交给各自 provider 请求字段，空值保留 provider 默认 | 产品流程；跨 provider effort 值校验 |
| `internal/runtimeguard` / `usage` | 单轮预算；token/费用投影 | 权限或账单真相 |

AGENTS 指令加载在 `agent`：用户 home `~/.eino-assistant` 按 `AGENTS.override.md`、`AGENTS.md` 顺序选择首个有效候选；workspace 每层先按相同 canonical 顺序，再按 `[rules].project_doc_fallback_filenames` 配置的 basename 顺序选择首个有效候选。默认 fallback 为空，canonical 行为不变；每个发现目录至多选择一个文件。符号链接会跟随到普通文件目标，去掉 UTF-8 BOM 后仅含空白的候选会跳过。fallback 只属于项目层，用户块与项目块使用独立预算。不要新建或恢复 `internal/rules`；`[rules]` 只是配置名称。

`internal/tui` 的 follow-up queue 是当前进程、当前 session 内的非 durable FIFO，不写 session ledger。active turn busy 时，Enter 对普通 follow-up 只做 FIFO admission，不启动第二个 turn；普通 follow-up 不会隐式 steer。`/queue` 提供列表、`drop`、`edit`、`clear`、`resume`；非取消 turn error 会保留尚未启动的项目并暂停自动 drain，`/queue resume` 仅在 idle 时继续，busy 或 compacting 时 fail-closed。Esc/Ctrl+C 取消在 turn 完成清理后保持既有自动 drain；`/clear`、`/new`、`/resume`、`/fork` 等 session switch 清理 queue 与 pause 状态。

busy regular turn 收到 Esc/Ctrl+C 后，TUI 先追加一次 display-only 的 `interrupt requested; waiting for turn cleanup`，表示取消已请求，但仍在等待 provider、tool 和 turn 清理完成；在该反馈出现至 `AskWithEvents` 完成期间，状态栏显示 `Stopping · waiting for turn cleanup`，并保留既有 elapsed、queue、context suffix。重复取消不会重复追加该反馈；`finishTurn` 后状态栏恢复既有 `Working`/ready 状态，再按既有结果显示 `interrupted` 或 error（如适用）。这些都只是 display-only 反馈，不改变取消传播、tool/approval 处理、FIFO 自动 drain、compaction/backtrack 分支或 durable session 语义。

`/steer <text>` 是与 FIFO 分离的显式路径，只针对正在运行的 regular turn：TUI 仅在 busy 状态尝试 admission，idle、compacting、session switch 或没有可 steer 的 active turn 时拒绝；admission 失败也不会回退到 FIFO。它从当前 `chat.Session` 取得已写入 `turn.started` 的 durable turn ID，并将其作为 `expectedTurnID` 传回；`Session` 在 admission 边界校验 expected ID 仍等于 active turn ID，过期或错 session 的请求失败。成功 admission 不启动第二个 `Ask`、不进入 FIFO、不取消 turn，也不修改正在运行的 tool 或 approval；支持 receipt 的调用方同时取得 mailbox 分配的 sequence 和原文，以便把 admission 与后续消费可靠关联；旧的 `Steer(... ) error` 调用仍保持兼容。`internal/agent` 的 opt-in ReAct 只在下一次 ChatModel 调用前通过 `MessageRewriter` 消费 mailbox 输入。

steer admission 本身不是消费或持久化保证：成功只表示输入进入该 regular turn 的 turn-local mailbox，TUI 立即显示 `steer admitted; awaiting next model call: <text>`，不表示模型已经看到输入。mailbox 在模型安全调用边界实际执行 `TakeTurnSteers` 后，`internal/chat` 才为每个输入发送 display-only 的 `TurnEventSteerConsumed`（带 sequence/content）；TUI 显示 `steer consumed at model boundary (#<sequence>): <text>`。该反馈事件不写 journal、不进入 `TurnCommit`，也不扩展 durable ledger 或 store schema，且不会成为 user transcript。

turn 成功 commit 时，只有实际被 `TakeTurnSteers` 消费的输入才随该 `TurnCommit` 写入 session ledger；TUI 显示 `steer committed: <n> consumed input(s)`。已 admission 但在 turn 完成前未消费的 late/pending 输入不持久化，TUI 显示 `steer discarded: <n> admitted input(s) were not consumed before turn completion`。turn 失败、取消或 commit 失败时，已消费和 pending 的输入全部不持久化，TUI 显示 `steer discarded: <n> admitted input(s); turn not committed`；显式取消完成清理后，TUI 可将有 receipt 但未观察到 `TurnEventSteerConsumed` 的 steer 按 admission 顺序恢复到 composer，作为未提交 draft，不写入 input history、FIFO 或 ledger，也不自动发送。已有 composer draft 保留并与恢复文本合并；普通 queued follow-up 仍按既有规则自动 drain。这些反馈只增加 display-only 状态，不改变普通 follow-up FIFO、工具调用或 approval 的 admission、执行、取消和审批语义；这些行为也不等同于 Codex steer，也不采用 Roo 式隐式 approval。

## 状态与安全边界

| 面 | 所有者 / 位置 | 生命周期 | 关键边界 |
| --- | --- | --- | --- |
| 硬权限 | `permissions` + `tools` + `sandbox` | 进程 / 每次工具调用 | 不依赖 AGENTS.md 或 memory 文本执行；模式不能绕过 deny、workspace/path clamp、sandbox 或不可用 fail-closed |
| TUI 会话权限模式 | `internal/tui` 的命令/idle admission 与展示；`cmd/eino-assistant` runtime 接线；`internal/tools` 在 shell/apply_patch 授权边界执行 | 当前 TUI 进程；不进入 config、ledger 或 resume | `ask`、`auto`、`plan`；plan 是临时 read-only phase，不是完整 plan artifact workflow；apply_patch 无条件结构化拒绝，shell 仅原始 PermissionSet allow 进入 enforced OS read-only worker；hard deny、path clamp、sandbox 与 host escalation 上限仍有效 |
| 会话账本 | `store` + `chat` | 可 `/resume` | journal、artifact 与 checkpoint 是真相 |
| 会话 fork / backtrack | `store` + `chat` + `tui` | `/fork` 仅 idle、无参数；backtrack 为 idle 两阶段 `Esc`，child 成功打开后切换 active session | store 复制 committed 前缀、重建 journal hash/seq、记录 parent provenance 并复制前缀 artifacts；首个 prompt 使用显式 before-first 空 prefix，后续 prompt 在前一 committed turn 前 fork 并只回填 composer；拒绝 active/pending compaction/checkpoint/task-derived 状态；不实现 destructive rewind 或 source/workspace/Git/外部副作用回滚 |
| 会话模型身份 / live 替换 | 可选 `store.ThreadModelRepository` + `chat.Session` + 上层 runtime + TUI `/model [name]` / `Alt+P` | thread 生命周期；替换只在 idle；模型身份可由 `model.changed` 追踪 | runtime 先解析显式 catalog alias（未知值保留 free-form），构造完整 provider bundle，再原地调用 `ReplaceModel`；picker 展示本地声明的 label/canonical/lifecycle/capabilities，但不宣称 provider health；成功不新建 thread 且保留 ID、system prompt、transcript、checkpoint、context、usage；active turn/pending compaction、pending approval 或 side question 拒绝；不改全局配置 |
| 用户/项目指令 | `agent` 选择 home 与 workspace AGENTS 指令 | `/new` / `/clear` 刷新；`/rules` 只读观察 | system prefix 的软指导；override 与 base 不拼接；用户和项目预算独立；`/rules` 不 reload |
| 语义记忆 | `memory`，`.eino/memory/` | 跨会话 | 不等于 `/resume`；agent 只读 |
| 旁路问题 | `cmd/eino-assistant` runtime + `internal/tui` | 单次请求；结果只存在当前 TUI 展示 | 使用 frozen session context 作 reference；不打断/不排队主 turn；不进主 ledger、`usage`、`journal`；不调用 tools/subagents；不修改文件、git、config、permissions；错误/空回答可见；无 callback 时 unavailable |
| 复杂任务图 | `agent.TaskController` + `store` | 当前会话，可 `/resume` | `task.state.updated` 是图的恢复投影；工具/artifact 仍是证据真相 |

System prompt 由 `agent.ComposeWithLayers` 组装：persona、工具/任务 policy、用户 instructions、项目指令（canonical AGENTS 优先，配置 fallback 次之）和记忆摘要。配置的 fallback 只在 fresh session prompt 组合时生效，每个发现目录至多选一个项目文件；创建 session 后冻结；`/resume`、`/memory`、`/compact` 不重写前缀。用户 root 在 runtime 构造时固定为 `os.UserHomeDir()/.eino-assistant`，不随 `storage.data_dir` 改变；resume 继续使用 thread 创建时冻结的 system prompt。`agent.ComposeWithLayersSnapshot` 同步返回不含正文的 source metadata，runtime 将其保留到 active session 生命周期；TUI `/rules` 只读该 snapshot，不调用 loader。resume 若 provenance 未进入持久化账本，会明确报告 source metadata unavailable，而不是用当前磁盘内容冒充 active snapshot。

## 复杂任务运行时

`task_plan`、`task_progress`、`task_complete` 只管理控制状态，没有文件或 shell 权限。

- 当前用户原文被 controller 保留为 `user-request` root requirement；计划必须把它映射到场景。
- proof 必须绑定精确匹配且成功的真实 `shell` 结果；图快照只保留引用和少量恢复上下文，完整证据仍在账本。
- 未计划且实际执行的 `shell` / `apply_patch`，以及任务完成或中断后迟到且可能改动工作区的工具结果，都会开启“必须先建计划”的 gate；模型不能直接交付该改动。
- `task_complete` 只是在当前 turn 内的暂定批准；`chat.Session` 在提交最终消息前复查 gate。取消、失败或恢复发现未提交批准、或快照后的 shell/patch 生命周期时，都会关闭交付并要求重规划。
- 任务图不增加修改、取消或完整 DAG slash 命令；`/goal` 只读显示有限的目标/状态/计数/进度/当前任务/PlanRequired/gaps 投影，状态栏显示紧凑进度，`Ctrl+T` 展开只读任务面板。
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
| 会话模型身份与 idle-only model replacement | 可选 `internal/store.ThreadModelRepository` / `ThreadStore.SetThreadModel` + `internal/chat.Session.ReplaceModel` + 上层 runtime + TUI `/model [name]` / `Alt+P`；`internal/config` 提供显式 `model.catalog` 的 label/alias/lifecycle/capability 声明，未知名称保留 free-form fallback；不新增全局配置重写层或 provider discovery/health 层 |
| source-preserving thread fork V1 | `internal/store` + `internal/chat` + `internal/tui` + [session-persistence.md](./session-persistence.md)；用户命令入口在 TUI，cmd 层不单独解析同名 CLI 命令；普通空边界是 latest，before-first 是独立 API |
| interactive backtrack V1 | `internal/tui/backtrack.go` + `internal/tui` + `internal/chat` + `internal/store`；复用 source-preserving fork，首个 selector target 显式走 before-first，不新增 workspace 回滚层 |
| 新工具或权限语义 | `internal/tools` + `sandbox` + [command-policy.md](./command-policy.md) |
| TUI 会话内 `ask` / `auto` / `plan` 切换 | `internal/tui`（slash、idle gate、展示）+ `cmd/eino-assistant`（共享 runtime 状态接线）+ `internal/tools`（实际授权边界）；不新增 config/store 持久层 |
| 项目记忆 | `internal/memory` + [memory.md](./memory.md) |
| slash、队列、状态展示 | `internal/tui` |
| 配置字段 | `internal/config` + `config.example.toml` |

改动包边界、持久化分层或安全边界时，同步更新本文件和对应专题文档。代码是最终真相；`docs/research/` 是参考，不是实现合同。

Headless `exec --json` 是 Codex-compatible 的 CLI 别名，实际映射到同一 `--output-format stream-json` JSONL 投影。它覆盖 fresh、resume、`--last` 和 ephemeral fresh，不能改变 `stream_version`、`sequence`、`session.started`、activity、最终 `result` 或错误/退出 contract；`--json` 与显式 `--output-format stream-json` 等价，与显式 `json` 或 `text` 是 input error。
