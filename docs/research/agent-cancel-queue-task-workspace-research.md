# Agent 对话取消、追加输入与任务目录隔离：业界实践

> 状态：业界调研笔记，不是实现方案，也不审计当前仓库。
>
> 调研日期：2026-07-17；2026-08-04 对 Codex、OpenCode 与 Roo Code 做 current source recheck。CLI、API 与源码行为会演进；采用前应复核引用版本。
>
> 范围：用户连续取消、追加输入或新建对话时，agent runtime 如何保持 turn / task 的顺序、如何传播取消，以及如何隔离和回收任务工作目录。
>
> 不在范围：当前仓库实现、具体 UI 样式、模型提示词、权限策略细表、分布式存储选型。

## 1. 摘要

- 成熟系统把“用户又发来一句话”至少拆成三种语义：**中途纠偏（steer）**、**排队等待下一轮（queue）**、**独立旁路问题 / 新任务**；不能把它们都隐式当成取消或都塞进当前上下文。[C1][A1][O1]
- 取消是一个过程而不是一个布尔值：先持久化 `cancel_requested`，再向模型流、子进程和工具传播，最后等待清理 / 结算确认，才能把 run 标为 `cancelled`。Temporal 要求活动通过 heartbeat 才能收到取消；Codex 源码测试也验证 shell 收到取消后先完成 TERM 清理。[T1][C3]
- 对活动中的 turn 追加输入时，Codex 以 `expected_turn_id` 防止旧 UI 或并发客户端把输入加到错误 turn；不活动、turn 不匹配、或不可 steer 的 turn 都会被显式拒绝。[C1]
- OpenCode 的公开源码将 `steer` 和 FIFO `queue` 作为不同 delivery 类型持久化，并按每个 session 串行 drain；重复唤醒只合并成一个后继执行。这个模型直接避免了“多条新消息同时拉起多个 agent loop”。[O1][O2]
- 可写任务不应共享物理工作目录。Claude Code 用 Git worktree 开隔离的并行会话；Git 本身也把 worktree 路径和管理元数据分离，并提供 lock、repair、prune 等生命周期操作。[A2][G1]
- 核心并发边界是 **task / run / attempt / workspace** 四层身份。目录路径不是并发身份；每个可写 run 需要独占 owner（单机互斥或分布式 lease）和递增 epoch，旧 attempt 的事件或写入必须被拒绝或隔离。

## 2. 问题边界

### 2.1 容易混淆的对象

| 对象 | 含义 | 为什么必须分开 |
| --- | --- | --- |
| `conversation` / session | 用户可继续对话的逻辑历史 | 同一 session 可包含多个已取消、已完成或排队的 run。 |
| task | 可观察、可恢复、可归档的工作单元 | 一个 task 可以被取消后重试、fork 或迁移工作区。 |
| run / turn | 某次实际 agent loop 的执行版本 | 取消和新输入必须指向具体 run，不能只指向“当前任务”。 |
| attempt / owner epoch | 某 worker 持有 run 的一次执行权 | 用来拒绝重连、重试、迁移后旧 worker 的迟到事件。 |
| workspace / task directory | 副作用发生的物理目录或 worktree | 路径会被移动、删除和复用，不能代替 task / run 身份。 |

### 2.2 本文讨论的竞态

1. 用户在流式输出、tool call 或 shell 执行中连续按取消，再追加多条消息。
2. 旧 run 还在清理时，新 run 已开始，旧工具结果或日志迟到。
3. 同一目录被两个可写 agent / 子 agent / retry 同时使用。
4. 进程崩溃、UI 断连或 worker 重启后，任务需要恢复、丢弃或安全回收。
5. 任务目录已手动删掉、移动，或正在被执行中的子进程持有。

### 2.3 关键结论的证据强度

- **官方文档**：Claude Code、Temporal、Git，适合当作公开交互或语义契约。
- **公开源码观察**：Codex 与 OpenCode，精确到本次调研时的 commit；可说明机制，但不应被当作永恒的产品承诺。
- **综合推断**：本文的状态机、不变量和目录所有权模型，是由上述机制归纳而来，明确不是任一产品的原样规范。

## 3. 业界机制对比

| 系统 | 运行中追加输入 | 取消 | 并发 / 工作区处理 | 可复用的机制 |
| --- | --- | --- | --- | --- |
| Codex（公开源码） | `turn/steer` 向活动 regular turn 注入输入；请求携带 `expected_turn_id`，不活动或 ID 不匹配即拒绝。源码注释表明 Enter 语义已从排队转为立即 steer。[C1][C2] | `interrupt_task` 中止当前 task；测试覆盖 shell 收到 TERM 后完成清理再返回。[C3] | 当前 turn 有输入队列和 mailbox；源码还明确区分 regular、review、compact 等可否 steer 的 turn。[C1] | 把“追加”做成有目标 run ID 的操作；把不可安全插入的状态显式拒绝。 |
| Claude Code | `Esc` 停止当前 response 或 tool call，以便 redirect，并保留已经完成的工作；`/btw` 则是可在主 turn 工作时运行的独立、无工具、无历史旁路问题。[A1] | `Ctrl+C` / `Esc` 是用户可见中断；对打开的 dialog，`Esc` 只关闭 dialog，避免把关闭审批框误当取消 agent。[A1] | `--worktree` 为并行 session 建独立 checkout / branch，官方文档明确目标是避免编辑冲突；checkpoint 可随会话恢复并 rewind。[A2][A3] | 将“打断主工作”“旁路查询”“并行新任务”分成不同 UX 和副作用权限。 |
| OpenCode（公开源码） | 输入以 durable admission 记录；`steer` 在当前 run 的安全点提升，`queue` 按 admission sequence FIFO 提升。若已有 steer，优先处理 steer。[O1] | 每个 key 的 coordinator 可中断 active fiber，并等待其 cleanup；中断时清除本次 coalesced wake。[O2] | 同一个 session 串行执行、不同 session 可并发；重复 wake 合并为一个后继 drain。runner 还校验 session 的 directory 与 workspace ID 是否仍属于当前 runtime。[O2][O3] | 将输入记录、提升、实际执行分离；用“单活跃 drain + 合并 wake”压制消息风暴。 |
| OpenAI Responses API | API 提供带 response ID 的取消端点，但官方 OpenAPI 明确仅适用于 `background=true` 的 response。[P1] | 这是远端 model response 的取消能力，不是本地 tool / shell 副作用的事务回滚。[P1] | 不管理本地任务目录。 | provider cancel 是一层控制面；host runtime 仍要单独管理工具、目录和终态。 |
| Temporal | 未讨论聊天输入队列。 | 普通 Activity 必须 heartbeat 并设 heartbeat timeout 才能收到取消；取消在下一可用机会以错误进入 Activity，清理可在 `finally` 做，但要重新抛出才会呈现为 cancelled。[T1] | 工作流事件历史支持从指定点 reset；reset 后的进度会被丢弃且需要记录原因。[T1] | `cancel requested` 与“已停止、已清理”分离；长操作需要 cooperative cancellation。 |
| Git worktree | 不管理聊天输入。 | 不管理 agent cancel。 | linked worktree 是独立 checkout 与管理元数据；删除默认只允许 clean worktree，缺失路径可 prune，lock 防止被自动 prune、移动或删除。[G1] | 目录是有状态资源，需要登记、锁定、修复、回收，而非在取消时盲删。 |

### 3.1 Codex：steer 与 interrupt 是两条不同控制通道

本次检查的 Codex 公共源码（commit `315195492c80fdade38e917c18f9584efd599304`）显示：

- `steer_input` 的职责是“向当前活动 turn 注入额外用户输入”，而非启动一个并发 turn。它要求存在 active turn、要求可选的 `expected_turn_id` 与服务端当前 ID 一致，并拒绝 review / compact 等不可 steer 的 turn。[C1]
- feature 定义的注释写明：Enter 已是立即提交的 steer 行为，而不是排队；这说明“用户新消息一定 FIFO 排队”不是 coding agent 的通用 UX。[C2]
- `interrupt_task` 则中止当前 task。其测试特意检查 shell 收到取消后，运行时等待 TERM trap 的清理完成，说明取消不应直接等同于“立刻删目录或立即启动写入重试”。[C3]

**观察后的边界**：源码中的 steer 是活动 turn 内的纠偏通道；它不代表所有场景都安全。Codex 明确拒绝不匹配或不可 steer 的 turn，正好暴露了需要 run identity 和状态机的原因。

### 3.2 Claude Code：把“打断”“旁路问题”“并行工作”分离

Claude Code 的公开文档给出了很清晰的交互分层：

- `Esc` 能在 mid-turn 停当前 response 或 tool call，让用户 redirect；但已经完成的工作保留。[A1]
- `/btw` 可在主 turn 执行时运行，不打断主 turn；它只读当前上下文、不能调工具、不会进入历史，也不允许 follow-up。这是“用户只是想问一句”的低风险旁路语义。[A1]
- 需要并行写代码时，官方建议用 `--worktree` 启动独立 session，避免编辑碰撞；这不是同一目录内的多 agent 并写。[A2]
- checkpoint 在每个 user prompt 前记录代码状态，且会随 session 保留；恢复时可以分别 restore 代码、对话，或两者一起 restore。[A3]

这类分层避免了一个常见错误：用户“新增一句话”时，系统既不应默认杀死主工作，也不应让该句拥有主工作同等的文件写权限。

### 3.3 OpenCode：把输入、调度、执行做成可观察的独立层

OpenCode 公共源码（commit `3a1c6df9e24672f0761a6ced18e1315d89334baf`）的机制尤其接近此问题：

- `PromptAdmitted` 先把输入持久化并给予 sequence；随后 `promoteSteers` 与 `promoteNextQueued` 分别把 steer 或 FIFO queue 输入提升为可执行消息。[O1]
- `run-coordinator` 按 key 串行执行：若同 key 已执行，新的 wake 只设置 `pendingWake`；结算后最多再跑一次后继 drain。不同 key 仍可并行。[O2]
- coordinator 的 interrupt 会标记 stopping、清除 coalesced wake，并等待 owner fiber 完成；不会只把 UI 变成“已取消”。[O2]
- runner 在运行前校验 session 记录的 `directory`、`workspaceID` 与当前 runtime 仍相同，否则直接 interrupt，防止任务被移交 / 附着点替换后旧 runtime 继续执行。[O3]

**重要限制**：同一源码也标注“单机 active drain”尚未替换为“多节点 durable ownership”，且 cancellation settlement 仍是待完成项。[O3] 因此它支持单机队列化与身份校验的价值，但不能单独证明分布式场景已经安全。

### 3.4 2026-08-04 current source recheck

本节只记录本次对公开来源的当前版本复核，不把旧快照的结论伪装成当前产品承诺。以下标签用于区分证据层级：**Documented fact** 是来源直接显示或明确写出的行为；**Cross-product synthesis** 是由多个产品事实归纳出的研究结论；**Evidence gap** 是当前公开材料无法证明的部分。

#### Documented fact

- **Codex current main**：`steer_input` 只接受 active regular turn；可选的 `expected_turn_id` 不匹配会被显式拒绝，review / compact turn 不可 steer。成功时，`UserInput` 会进入 turn-local pending input，并发出 `Steer` activity。[C5][C7]
- **Codex handlers / feature**：handlers 先尝试 steer；只有 `NoActiveTurn` 才启动普通 regular task，其他错误会发出 error event。`Steer` feature 注释显示 Enter 已即时 steer，相关 flag 仅为兼容保留，行为恒启用。[C6][C8]
- **OpenCode current dev**：`PromptAdmitted` 形成 durable sequence 后，分别执行按 admitted order 的 promotion steer 与 queue promotion；queue 只提取一条 FIFO 输入，再提升为 steer。[O4]
- **OpenCode coordinator / runner**：每个 key 只有一个 active drain；`pendingWake` 合并后继唤醒，interrupt 会清除 pending wake 并等待 owner。runner 会校验 `directory` 与 `workspaceID`；当前注释仍将多节点 durable ownership 与 cancellation settlement 标为未完成。[O5][O6]
- **Roo Code 产品文档**：FIFO queued messages 对用户可见、可编辑、可删除、不可重排；发生错误时消息保留。这是产品文档事实 / 厂商自述，不足以推出其内部 run identity。[R1]

#### Cross-product synthesis（研究结论，不是本仓库实现计划）

- **steer、queue、side question / fork 必须分语义**：Codex 的即时 steer、OpenCode 的 durable steer / FIFO queue 分层，以及既有 Claude Code 旁路问题与并行工作区材料，共同说明“新输入”不是单一投递动作。[C5][C6][O4][A1][A2]
- **steer 的最小安全合同**至少需要 target run / turn identity、active / steerable 状态、显式 mismatch / rejection、单 active drain，以及 cancellation settlement。这里是跨产品研究归纳，不是任一产品公开 API 的完整规范。[C5][C6][O5][O6]
- **不能直接把 FIFO queue 替换成 steer**：queue 保留顺序并等待后继执行，steer 则把输入注入既有活动 turn；两者在目标、时机和失败处理上不同。Roo Code 的可见 FIFO 队列也说明 queue 是独立的用户语义，而不是 steer 的 UI 别名。[C5][O4][R1]

#### Evidence gap

- Claude Code official interactive docs 在本环境的 2026-08-04 访问尝试超时；本次 recheck 不把记忆中的行为写成 current documented fact，仅保留既有 URL 和此前已记录的证据边界。[A5]
- Roo Code 文档没有公开 run identity、steer target 或 cancellation settlement；可见、可编辑的 FIFO 队列不能证明其内部如何防止旧 run 迟到事件。[R1]
- OpenCode 当前源码的注释明确保留多节点 durable ownership 与 cancellation settlement 缺口；因此不能从单机 coordinator 直接推断分布式所有权或完整取消终态已经安全。[O5][O6]
- Codex 当前文件展示了 steer 的目标 turn、拒绝条件和 pending input 路径，但这些文件本身不能证明跨节点 ownership、所有外部工具的取消结算，或工作区回收已经由产品整体承诺。[C5][C6][C7]

## 4. 综合状态模型（推断，不是某产品 API）

把输入控制和执行控制拆开，能够覆盖“取消、连续追加、目录维护”而不互相踩踏：

```text
                 immutable input log
  user action ───────────────────────────► admitted(seq, task_id, intent)
                                              │
                                              ├── steer(target_run_id)
                                              ├── queue
                                              ├── side_question / fork
                                              └── cancel(target_run_id, request_id)

 task T
   └── run R42 (epoch 7, workspace W, owner A)
         RUNNING
           │ steer(R42)              -> append only at declared safe point
           │ cancel(R42)             -> STOP_REQUESTED
           ▼
         STOP_REQUESTED
           │ provider/tool/process acknowledge; settle records; cleanup
           ▼
         CANCELLED (cleanup_complete=true)
           │ promote next queued input
           ▼
         run R43 (epoch 8, workspace W or W')
```

### 4.1 四种用户操作应有明确语义

| 用户意图 | 控制操作 | 是否保留主 run | 工具 / 目录副作用 | 新输入的落点 |
| --- | --- | --- | --- | --- |
| “停下来” | `cancel(target_run_id)` | 否，进入 stopping 后再终态 | 传播取消并等待清理；不可承诺回滚已经完成的副作用 | 不自动绑定到旧 run。 |
| “改成这样做” | `steer(target_run_id, input)` | 是 | 只在定义的安全点读入；若 turn 不可 steer 则显式失败或提示改为 queue / cancel+restart | 当前 run 的追加输入。 |
| “等它做完再说” | `queue(input)` | 是 | 不触碰当前 run | 后续 run，按稳定顺序提升。 |
| “顺便问个问题 / 开另一个活” | `side_question` 或 `fork/new_task` | 是 | side question 应无工具 / 无写入；独立任务应有独立 run 与 workspace | 旁路 overlay 或另一 task。 |

在 `STOP_REQUESTED` 状态收到的普通新消息，最稳妥的语义是**记录为 queue 或新 task，绝不再 steer 到已请求停止的旧 run**。否则旧 run 的迟到事件与新意图会混入同一段上下文和同一目录。

### 4.2 连续取消和连续追加的必要不变量

以下为跨来源综合出的运行时不变量：

1. **输入先入账、执行后提升**：每条输入有不可变 `input_id`、`task_id`、意图、顺序 / 时间和幂等键；重试提交不能生成两条逻辑相同的 prompt。[O1]
2. **取消针对 run，不针对裸 session**：`cancel` 必带目标 `run_id`；若当前 active run 已改变，返回可观察的 mismatch / no-op，而不是误杀新 run。Codex 的 `expected_turn_id` 是同一原则的直接证据。[C1]
3. **一个可写 workspace 同时至多一个 writer owner**：同机用每 task / workspace 的串行 coordinator；多机需要 lease 与递增 fencing epoch。后到的 owner、事件和 tool completion 必须携带 epoch 并被拒绝。
4. **终态以结算为准，不以按钮点击为准**：UI 可立即显示“正在停止”，但 task 只有在模型流、进程树、工具调用和目录 journal 都完成结算后才能显示 `cancelled`。这与 Temporal 的 cooperative cancellation 和 Codex 的 shell cleanup 测试一致。[T1][C3]
5. **事件必须可去重且可拒绝过期**：事件 / tool result 至少带 `(task_id, run_id, attempt_or_epoch, event_seq)`；投影层只接受当前 owner / epoch 的可达事件。OpenCode 的 durable sequence 与目录 / workspace 校验是两个互补例子。[O1][O3]
6. **取消本身幂等**：重复 `cancel(R42)` 应返回“已请求 / 已取消”，不再次清理，也不吞掉之后针对 R43 的取消；使用 `cancel_request_id` 可让网络重试安全。
7. **队列必须有可见策略**：FIFO、最新覆盖、合并为一条 summary，都是可选产品策略；但要显式展示。OpenCode 的 queue 为 FIFO，而 Codex 的当前 source 倾向即时 steer，说明不能假设存在唯一行业默认。[O1][C2]

### 4.3 目录和 task 的所有权模型

目录管理应独立于对话文本的生命周期。一个合理的抽象是：

```text
Task identity       : stable task_id
Run identity        : task_id + run_id
Write authority     : run_id + owner_id + monotonically increasing epoch
Physical workspace  : workspace_id + canonical path + lifecycle state
Tool side effect    : run_id + tool_call_id + settlement state
```

从 Git worktree 与上述 agent runtime 可归纳出下列规则：

- **先登记再使用**：创建 task directory / worktree 时记录其 `workspace_id`、规范化路径、所属 task / run、创建来源和 owner epoch；不要只把路径字符串塞在聊天记录里。[G1][O3]
- **写入隔离优先于锁文件侥幸**：两个有文件写权限的任务使用不同目录 / linked worktree；共享只适合经过明确审计的只读操作。Claude Code 的 worktree 模式正是用隔离 checkout 避免编辑碰撞。[A2]
- **取消后不立即复用路径**：先释放 owner、等待进程树 / tool settlement、保存必要快照与日志，再回收；超时则标记 `orphaned` 并由单独 reconciler 重试。Git 对缺失 worktree 的 `prune`、对异常元数据的 `repair`，说明回收应是可审计过程而不是单次 `rm -rf`。[G1]
- **把保留与删除分开**：取消的任务可能需要保留用于 diff、resume、审计或用户手工恢复。Git 也仅默认删除 clean worktree，并为 lock、force、prune 提供不同路径。[G1]
- **路径复用必须防旧写入**：旧子进程即使没有目录句柄，也可能在目录重新创建后按同一路径写入。因而删除后再创建同名目录不能取代 owner epoch / 子进程回收。

## 5. 取消传播与完成屏障

### 5.1 推荐区分的状态

| 状态 | 含义 | 是否可以启动下一写入 run |
| --- | --- | --- |
| `running` | 当前 owner 正在采样、调用工具或等待审批 | 否。 |
| `stop_requested` | 取消意图已持久化并向所有可取消层传播 | 否；可接收 queue。 |
| `draining` | 等待 provider、子进程、工具、日志 / 快照结算 | 否。 |
| `cancelled` | 旧 run 的终态已持久化，且 cleanup barrier 已满足 | 可以。 |
| `failed` | 非取消错误已结算 | 依策略可重试或等用户输入。 |
| `completed` | 正常结束已结算 | 可以消费 queue 或结束 task。 |

`stop_requested` 和 `cancelled` 的间隔必须可见，否则用户容易再次追加、再次取消，系统却无法解释新消息究竟会落到旧 run 还是新 run。

### 5.2 取消必须沿副作用树传播

```text
UI cancel
  -> durable cancel intent (target run + idempotency key)
  -> stop model stream / remote response when provider supports it
  -> stop local tool tasks and child processes
  -> wait for cooperative cleanup and record each settlement
  -> revoke owner epoch / release workspace writer lease
  -> append terminal event, then promote queued work
```

OpenAI 的 OpenAPI 说明 `responses/{id}/cancel` 只适用于 background response。[P1] 因而“中断前端 HTTP 流”或“请求 provider cancel”都不能推出本地 shell、MCP、文件写入已经停止。Temporal 的 heartbeat 语义进一步说明，长工具只能在自己检查取消的机会点停下。[T1]

对不能取消的外部副作用（例如已经发送的网络请求）应记录“已提交但未必可撤销”的结算状态，并在需要时走补偿操作；不能把它伪装成未发生。

## 6. 常见失败模式

| 反模式 | 触发方式 | 后果 | 更可靠的替代 |
| --- | --- | --- | --- |
| 单个 `cancelled: bool` | 新输入、重试和旧事件并发到达 | 无法判断取消属于哪次 run，旧结果可能覆盖新状态 | `run_id + cancel_request_id + terminal state + epoch`。 |
| 取消后立即启动下一轮 | 旧工具尚未退出 | 两个 writer 在同一目录操作，或旧日志混入新轮 | 在 cleanup barrier 后才提升 queue。 |
| 把所有新文本当 queue | 用户只是纠偏时要等待很久 | 交互迟钝，用户转而反复取消 | 提供 target-run steer，并对不可 steer 状态显式反馈。[C1] |
| 把所有新文本当 steer | 旧任务已停止、正在 compact / review，或用户意图完全无关 | 上下文污染、错误 task 被继续执行 | queue、side question、fork / new task 是独立操作。[A1][C1] |
| UI 断流即认定工具已停 | provider 或子进程仍在执行 | “幽灵写入”、孤儿进程、目录被过早删除 | 取消传播 + settlement + reconciler。[T1][C3] |
| 多个任务复用可写目录 | 并发 agent / retry / resume | 文件竞态、diff 不可归因、恢复不可靠 | 每个 writer 使用独立 workspace 或可验证的独占 lease。[A2][G1] |
| 用目录路径当 task 主键 | 路径移动、删除、复用 | stale event 写入了新任务目录 | 稳定 task / run ID，路径仅为受管理资源。[O3][G1] |
| 只做单机互斥却部署多 worker | failover 或重连后双 owner | 两个 runtime 都认为自己活跃 | durable owner lease + fencing epoch；OpenCode 源码对此仍标为待完成项。[O3] |

## 7. 需要按产品决定的开放问题

- **追加消息默认是什么？** 纠偏、FIFO、最新覆盖还是新 task？产品应让用户看见并可撤销该选择，而不是把语义藏在 Enter 键后。
- **取消后保留哪些变更？** Claude Code 文档明确保留已完成工作并提供 checkpoint / rewind；其他产品也可选择自动回滚、保留 diff 或要求用户确认，但这是一项产品策略。[A1][A3]
- **工具取消等级**：哪些工具支持 cooperative cancellation，哪些只能等待，哪些需要补偿？不同类型不能统一承诺“已停止”。
- **任务目录保留期与隐私**：completed / cancelled / orphaned workspace 的 TTL、手动锁定、归档和安全擦除需要独立策略。
- **分布式 ownership**：单进程 coordinator 足够时可很简单；一旦有多 worker、断线重连或远程执行，必须明确 lease 时长、续租、epoch 递增和故障恢复。
- **恢复边界**：恢复应从最后一个已结算的 tool / 采样边界继续，还是将中途 run 标为 interrupted 后新开 run？Codex 源码对 mid-turn fork 使用显式 interrupted marker，说明这一边界必须可表示。[C4]

## References

既有来源访问日期为 2026-07-17；以下 current source recheck 来源于 2026-08-04。Claude Code 的当前访问尝试超时，相关 URL 仅作为 evidence gap 保留。

- [A1] Claude Code, [Interactive mode](https://code.claude.com/docs/en/interactive-mode) — `Ctrl+C` / `Esc` 中断、`/btw` 旁路问题与 background controls。
- [A2] Claude Code, [Common workflows: Run parallel sessions with worktrees](https://code.claude.com/docs/en/common-workflows) — `--worktree` 隔离并行 session。
- [A3] Claude Code, [Checkpointing](https://code.claude.com/docs/en/checkpointing) — prompt 前快照、resume、rewind 与代码 / 对话恢复粒度。
- [A4] Claude Code, [How the agent loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop) — turn、tool execution、终态 `ResultMessage`。
- [C1] OpenAI Codex source, [`session/mod.rs` at `3151954`](https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/core/src/session/mod.rs) — active turn steering、expected turn ID、不可 steer turn、interrupt。
- [C2] OpenAI Codex source, [`features/src/lib.rs` at `3151954`](https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/features/src/lib.rs) — steer / queue 兼容标记的当前注释。
- [C3] OpenAI Codex source, [`session/tests.rs` at `3151954`](https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/core/src/session/tests.rs) — shell tool cancellation 等待 TERM cleanup 的测试证据。
- [C4] OpenAI Codex source, [`thread_manager.rs` at `3151954`](https://github.com/openai/codex/blob/315195492c80fdade38e917c18f9584efd599304/codex-rs/core/src/thread_manager.rs) — mid-turn snapshot / interrupted marker 的 fork 语义。
- [G1] Git, [`git-worktree` documentation](https://git-scm.com/docs/git-worktree) — linked worktree、lock、prune、repair、clean removal。
- [O1] OpenCode source, [`session/input.ts` at `3a1c6df`](https://github.com/anomalyco/opencode/blob/3a1c6df9e24672f0761a6ced18e1315d89334baf/packages/core/src/session/input.ts) — durable input admission、steer 与 FIFO queue promotion。
- [O2] OpenCode source, [`session/run-coordinator.ts` at `3a1c6df`](https://github.com/anomalyco/opencode/blob/3a1c6df9e24672f0761a6ced18e1315d89334baf/packages/core/src/session/run-coordinator.ts) — per-key serialization、coalesced wake、interrupt waits cleanup。
- [O3] OpenCode source, [`session/runner/llm.ts` at `3a1c6df`](https://github.com/anomalyco/opencode/blob/3a1c6df9e24672f0761a6ced18e1315d89334baf/packages/core/src/session/runner/llm.ts) — active drain、directory / workspace validation 与明确的分布式限制。
- [P1] OpenAI, [`openapi.yaml` at `db3e531`](https://github.com/openai/openai-openapi/blob/db3e53198a66732cfe161339ea63bf36fc0137ad/openapi.yaml) — Responses cancel endpoint 仅用于 `background=true` response。
- [T1] Temporal, [Cancel a Workflow - TypeScript SDK](https://docs.temporal.io/develop/typescript/workflows/cancellation) — heartbeat、取消投递时机、cleanup 与 cancelled 终态。
- [C5] OpenAI Codex current main source, [`session/mod.rs` at `f6af2f2`](https://github.com/openai/codex/blob/f6af2f206a01d1d56559fab9f89a49ff706a186c/codex-rs/core/src/session/mod.rs) — active regular turn steering、`expected_turn_id` mismatch rejection、不可 steer turn 与 pending input 路径（accessed 2026-08-04）。
- [C6] OpenAI Codex current main source, [`session/handlers.rs` at `8ed7a7b`](https://github.com/openai/codex/blob/8ed7a7badd51c707b22252520b0f68cdd2669790/codex-rs/core/src/session/handlers.rs) — 先尝试 steer、`NoActiveTurn` fallback 到 regular task、其他错误发 error event（accessed 2026-08-04）。
- [C7] OpenAI Codex current main source, [`session/input_queue.rs` at `34f6f77`](https://github.com/openai/codex/blob/34f6f778d7dab9d75f341cdefe50b2933d18fbd3/codex-rs/core/src/session/input_queue.rs) — turn-local pending input 与 `Steer` activity（accessed 2026-08-04）。
- [C8] OpenAI Codex current main source, [`features/src/lib.rs` at `937dd14`](https://github.com/openai/codex/blob/937dd1407a63684f5573cae89b292e131feb3e9e/codex-rs/features/src/lib.rs) — Enter 已即时 steer、兼容 flag 与恒启用行为的 feature 注释（accessed 2026-08-04）。
- [O4] OpenCode current dev source, [`session/input.ts` at `14b6136`](https://github.com/anomalyco/opencode/blob/14b613678dbe0deb3f14fe4c16ffe26db1f75b4a/packages/core/src/session/input.ts) — durable `PromptAdmitted` sequence、steer promotion 与单条 FIFO queue promotion（accessed 2026-08-04）。
- [O5] OpenCode current dev source, [`session/run-coordinator.ts` at `2f89aff`](https://github.com/anomalyco/opencode/blob/2f89aff9e3d202ec4bbca181e551438ba01f4667/packages/core/src/session/run-coordinator.ts) — per-key 单 active drain、`pendingWake` 合并与 interrupt 等待 owner（accessed 2026-08-04）。
- [O6] OpenCode current dev source, [`session/runner/llm.ts` at `72c761e`](https://github.com/anomalyco/opencode/blob/72c761e10d93e325ee6d99cb57b1c13923fd242a/packages/core/src/session/runner/llm.ts) — directory / workspaceID 校验，以及多节点 ownership、cancellation settlement 的未完成注释（accessed 2026-08-04）。
- [R1] Roo Code, [Message queuing](https://roocodeinc.github.io/Roo-Code/features/message-queueing) — FIFO queued messages 的可见、可编辑、可删除、不可重排与错误保留行为（accessed 2026-08-04；产品文档 / 厂商自述）。
- [A5] Claude Code, [Interactive mode](https://code.claude.com/docs/en/interactive-mode) — 2026-08-04 current recheck 在本环境连接超时；不据此新增当前行为事实，保留为 evidence gap。
