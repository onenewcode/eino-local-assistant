# 非交互式 coding agent 的版本化 JSONL 事件契约：业界实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-03. Re-verify before adopting; CLI behavior and
> schemas change with releases.
>
> Scope: 非交互式 coding agent 的 stdout JSONL 事件流如何定义事件顺序、
> 身份、成功/失败/取消终态、慢消费者、断管和协议演进；以及公开流是否应暴露
> token 与工具载荷。
>
> Out of scope: 本仓库实现映射；任一产品未公开的内部队列或取消实现；工具副作用
> 的事务/回滚保证；面向人类的 TUI 渲染格式。

## 1. 结论

- **跨产品综合。** 最小的公开 JSONL 流应先只承诺**生命周期事件**，而不是原始
  token、tool input 或 tool output。三个产品都证明这些细节对实时 UI 很有价值，
  但 Claude 的部分流直接承载原始 API 事件和递增 tool JSON，Gemini 的 `tool_use` /
  `tool_result` 也包含参数/输出；它们天然是高频、可变且可能含敏感工作区内容的
  诊断面。[C1][C2][G1]
- **跨产品综合。** 流记录须有版本化包络、`run_id`、每条唯一 `event_id` 和每个
  `run_id` 内严格递增的 `seq`。不要用 wall-clock timestamp 排序：Claude 明确说
  消息时间戳来自产生日志的机器时钟，只适合展示；其公开消息同时使用 UUID、
  session ID 和 parent tool ID，说明这些身份维度不能互相替代。[C2]
- **跨产品综合。** 正常完成的流应有且仅有一个 terminal 事件，明确区分
  `completed`、`failed`、`cancelled`；但“终态事件存在”不能替代进程退出状态。Claude
  把最后一行规定为 `result`，Gemini 列出最终 `result` 和独立退出码；二者都表明
  调用方应在 EOF 后同时检查 terminal 与 exit status。[C1][G1]
- **跨产品综合。** 必须把 stdout 当成纯 JSONL 数据通道，stderr 当诊断通道；慢
  消费者需要有界 drain/backpressure，断管则允许 terminal 缺失。Claude 已将慢消费者
  的退出等待公开为“按剩余队列量缩放、上限 30 秒”，但没有把断开下游 pipe 的完整
  语义写成协议；其他两个来源也未发布该保证。[C1]
- **证据边界。** 观察到的 Codex CLI 0.146.0 只写明 `exec --json` “Print events to
  stdout as JSONL”；没有公开 schema、排序、终态、stderr、broken-pipe 或兼容策略。
  这是一条输出边界观察，不能被扩张成可依赖的事件契约。[O1]

## 2. 术语与决策面

| 术语 | 本文含义 | 不应混同为 |
| --- | --- | --- |
| JSONL event stream | stdout 上每行一个完整 JSON 对象的增量记录。 | 一个最终 JSON、终端彩色日志或可任意截断的 token 文本。 |
| `run_id` | 这一次 agent 运行的不透明身份。 | 可恢复 session ID、单条事件 ID 或工具调用 ID。 |
| terminal event | 正常收束时说明这次运行最终状态的唯一记录。 | 中途的 `error`/warning、EOF、或 OS exit code。 |
| cancel | 调用方或控制面已请求并被 agent 确认的中止状态。 | 消费者断开 stdout、进程崩溃或无法写出终态。 |
| public stream | 默认可被 CI、插件或 UI 消费的稳定接口。 | 仅供调试的完整 transcript、模型 token 或原始工具 I/O。 |

本文的问题不是“如何忠实镜像任一厂商的 transcript”，而是：当调用方只需安全地
观察一次非交互式运行时，哪些事件和边界能成为长期公共契约。

## 3. 已发布产品的证据

### 3.1 Codex CLI 0.146.0：已观察到的边界，不是 schema

- **文档化事实（本机版本化观察）。** 2026-08-03 在研究机执行 `codex --version`
  和 `codex exec --help`，版本为 `codex-cli 0.146.0`；该命令将 `--json` 描述为
  `Print events to stdout as JSONL`。[O1]
- **证据边界。** 此帮助文本没有公布 event type、字段、每行的 ID、事件顺序、最后
  一行是否 terminal、退出码分类、stderr 分配、背压、断管或兼容规则。因此不能从
  “events as JSONL” 推断 `result`、tool event 或版本协商的存在。[O1]

### 3.2 Claude Code：明确的终态、排空和能力协商

- **文档化事实。** Claude Code 的 `--output-format stream-json` 是逐行 JSON；在
  `--verbose --include-partial-messages` 下可收到 token/工具生成中的事件。文档承诺
  最后一行是 `result`，其中有最终文本、成本和 session metadata。[C1]
- **文档化事实。** 其流不是一个扁平、跨版本固定的“tool”事件枚举：初始化是
  `system/init`，完整消息为 `assistant` / `user`，原始 API 部分事件为
  `stream_event`，而 tool call 是消息的内容块。部分事件与完整消息带 `uuid` 和
  `session_id`；完整消息还可带 `parent_tool_use_id`，让消费者恢复 subagent
  关系。[C1][C2]
- **文档化事实。** `SDKResultMessage` 明确有 `success` 和多种执行错误 subtype；
  被 interrupt/abort 截断的 assistant message 带 `aborted: true`，内容可能在词中间
  结束。初始化事件的开放式 `capabilities` 数组要求消费者忽略未知值，并用能力而非
  版本号判断可用行为。[C2]
- **文档化事实。** 正常事件顺序并非绝对“init 第一条”：`system/init` 通常首先出现，
  但 plugin/hook startup events 可以在它之前。若消费者读取很慢，CLI 会在退出前等待
  已排队输出被读取，等待随积压伸缩、最多 30 秒。[C1]

### 3.3 Gemini CLI：公开的事件种类、结果和退出码

- **文档化事实。** Gemini 的 headless `stream-json` 是 JSONL，公开列出 `init`
  （session ID、model）、`message`、`tool_use`（含参数）、`tool_result`（工具
  输出）、`error`（非致命警告或系统错误）和最终 `result`（聚合统计及按模型的
  token 用量）。因此中途的 `error` 不能单独被判作这次运行失败。[G1]
- **文档化事实。** 同一文档给出 `0` 成功、`1` general/API error、`42` input error、
  `53` turn limit；`--output-format` 也明确区分 `text`、单个 `json` 与
  `stream-json`。该文档没有给出这些 JSONL events 的逐字段 schema、排序数字、
  stdout/stderr 并发规则、慢消费者或断管规则。[G1][G2]

## 4. 机制与取舍

### 4.1 终态、EOF 与退出码是三份不同信号

```text
正常收束： init -> ...lifecycle/progress... -> terminal -> EOF -> process exit
中途失败： init -> ...error/progress...     -> terminal(failed) -> EOF -> non-zero
已确认取消：init -> ...                     -> terminal(cancelled) -> EOF -> non-zero
崩溃/断管： init -> ...                     -> write/read path breaks -> EOF/exit
                                                ^ terminal may be absent
```

- **跨产品综合。** 把 terminal 当作一次运行的最终业务状态，把 exit status 当作
  进程是否正常交付/收束的 OS 信号，把 EOF 当作传输结束。三者在正常成功时会一致，
  在 SIGKILL、崩溃、consumer 提前关闭 pipe 时不会；缺 terminal 的调用方应归类为
  `delivery_unknown`，而不是臆造 `failed` 或 `cancelled`。
- **跨产品综合。** `error` 必须分成 non-terminal diagnostic 与 terminal failure。
  Gemini 公开将 `error` 定义为非致命警告或系统错误；Claude 则把 API retry 作为
  `system/api_retry` 事件后继续执行。这两种情况都不能抢占 `result` 的判定权。
  [C1][G1]
- **证据缺口。** 这组资料没有证明三家会在任意取消路径都成功写出 terminal。
  Claude 只公开 SIGTERM 会 abort in-progress turn、终止 Bash process tree 并以
  143 退出，并没有据此承诺 stdout 收尾记录必达。[C1]

### 4.2 事件身份和顺序须独立建模

Claude 的 `uuid`、`session_id`、`parent_tool_use_id` 与 `user_message_uuid` 体现了
四个不同的问题：去重、会话归属、父子工具关系、输入和结果的关联；其中没有一个等于
“这个 stdout 流中的全序号”。[C2]

因此可复用的**产品中立综合**是：

| 字段 | 解决的问题 | 契约要求 |
| --- | --- | --- |
| `run_id` | 哪次运行 | 同一 stdout 流的一次调用内固定；不承诺可恢复。 |
| `event_id` | 重放/去重 | 每一记录唯一、不可复用；重复投递须用此键去重。 |
| `seq` | 展示和持久化的因果顺序 | 同一 `run_id` 的正整数严格递增且无重排；不能由 timestamp 代替。 |
| `causation_id` | 可选的父子关联 | 只在公开 progress 需要关联时出现；不得强迫暴露工具调用 ID。 |
| `session_id` | 可选的恢复身份 | 与 `run_id` 分开；不存在时应省略或为 `null`，不猜测持久性。 |

单一 stdout writer 可自然保证 `seq` 的线性顺序；如果内部有并行 tools 或 subagents，
需要在写入点给记录分配该序号，而不是让消费者按并发完成时间猜测。

### 4.3 stdout、慢消费者和 broken pipe

- **跨产品综合。** machine-readable stdout 只能包含完整 JSONL record，不能插入
  banner、ANSI、进度条或未完成的 JSON。stderr 可以保存启动/调试诊断，但不是事件
  协议的一部分；Claude 的启动前 flag error 写 stderr、运行内失败作为 stdout result
  的做法，正说明这两个通道需要书面区分。[C1]
- **跨产品综合。** 无界内存队列不是 slow-consumer 策略。Claude 的最多 30 秒
  drain 证明“发出 terminal 后立即 exit”也会丢尾部；一个安全的通用模型是：对公共
  生命周期记录 backpressure，限制诊断缓冲区，且把日志/高频可视事件合并或丢弃为一条
  明确的 summary，而不是偷偷无限积压。[C1]
- **跨产品综合。** 下游关闭 pipe、`EPIPE`/`SIGPIPE`、stdout write error 都是**传输
  失败**，不是消费者已经收到了 `cancelled`。若运行的唯一所有者就是该 CLI，可停止
  agent 和子进程以避免无观察者的继续执行；若运行另有 durable owner，则与该 owner
  协商取消。无论哪种，已断开的 stdout 无法可靠接收 terminal，因此不能许诺终态
  必达。此处是工程综合，不是任何一家的已发布断管语义。

### 4.4 工具/模型载荷是可选诊断，不是默认公开 API

Claude 文档说明部分流直接透传 raw API events，tool input JSON 以增量片段到达；其
subagent text/thinking 可被可选地转发。Gemini 文档也直接描述 `tool_use` 含参数、
`tool_result` 含执行输出。[C1][C2][G1] shell 命令、文件内容、环境变量和模型的
中间文字都可能包含凭据、私有源码、提示词、PII 或不稳定的内部字段。

因此这是**跨产品综合**：

- 默认的 public stream 只给 `run.started`、有限 `run.progress`、terminal，最多再给
  经明确允许的安全摘要（例如 tool 名称/计数，而非 arguments/output）。
- 原始 token、thinking、tool arguments、tool result 和 transcript 应为独立显式
  opt-in，例如 `detail=debug`；它必须有单独的权限、redaction、大小上限和保留策略，
  不应通过给默认事件“多加几个字段”悄悄启用。
- 秘密清洗不能只靠消费者：一旦原始载荷写进 stdout、CI artifact 或集中日志，收集者
  就已获得副本。公共协议只能承诺最小必要数据；调试协议须把泄露风险写入契约。

### 4.5 兼容策略：先协商、再忽略未知、最后才弃用

Claude 的开放 `capabilities` 集合明确要求消费者忽略未知值并按能力检测，而不是按
CLI version 做行为分支。[C1][C2] 据此形成的**跨产品综合**：

1. 第一条 `run.started` 固定含 `protocol`、整型 `version` 和开放的 `capabilities`；
   不认识的 capability 不能使旧消费者失败。
2. 同一 major version 只新增 optional fields / event types；消费者必须忽略未知字段和
   未请求的未知事件，而生产者不能更改既有字段含义或 terminal 语义。
3. 删除、重命名、改变字段类型/终态语义，或把默认事件升级为含敏感载荷，应发布新的
   major version 或新的 detail channel，不能以 CLI build number 充当协议版本。
4. `run.started` 在正常启动时应尽早写出；若参数验证或 stdout 建立在它之前失败，
   允许没有 JSONL，调用方只根据 stderr/exit 做启动失败处理。Claude 的 startup events
   可以先于 `system/init`，也是把“启动过程”与“运行契约开始”分开的反例提醒。[C1]

## 5. 最小 public protocol（产品中立综合）

这不是对任何现有 CLI 的 schema 宣称，而是根据上述证据得出的、可版本化的最小公开面。

```json
{"protocol":"agent.run.events","version":1,"run_id":"r_opaque","event_id":"e_01","seq":1,"type":"run.started","data":{"capabilities":["lifecycle_v1"]}}
{"protocol":"agent.run.events","version":1,"run_id":"r_opaque","event_id":"e_02","seq":2,"type":"run.progress","data":{"phase":"working"}}
{"protocol":"agent.run.events","version":1,"run_id":"r_opaque","event_id":"e_03","seq":3,"type":"run.completed","data":{"summary":"completed"}}
```

### 5.1 允许的事件和终态不变量

| 类型 | 含义 | 公开数据上限 |
| --- | --- | --- |
| `run.started` | 协议已建立，提供 version/capabilities。 | `run_id`、可选安全 model label；不含 prompt 或环境。 |
| `run.progress` | 有限、可合并的阶段变化。 | 枚举 phase、计数或经审查摘要；不含 raw token/tool I/O。 |
| `run.completed` | agent 正常完成。 | 安全最终摘要或由另一最终 JSON 接口取得的结果引用。 |
| `run.failed` | agent 明确失败。 | 稳定 error code、可读消息、可选可公开诊断。 |
| `run.cancelled` | 请求取消已被确认。 | 可选稳定 reason code；不能把断管伪装为它。 |

不变量如下：正常建立的流以 `run.started` 开始；`seq` 连续严格递增；最多一个 terminal；
terminal 后不再有同一 `run_id` 的记录；`completed` 对应零退出，`failed` 和
`cancelled` 对应非零退出。发生崩溃/断管时允许违背“有 terminal”这一交付性质，但
不得写出第二个或冲突的 terminal。

### 5.2 为什么不从 tokens/tools 开始

原始 tokens 和工具载荷同时引入四类长寿承诺：高频背压、分块/重组顺序、敏感数据
处理、内部 tool schema 演进。Claude 的 `include-partial-messages` 正是一个显式开关，
且结构化输出只出现在最终 `ResultMessage.structured_output`，不是 token delta；这说明
“实时细节”与“稳定结果”可以、也应当分层。[C2]

所以首个 public version 应让 UI 至少可表示 started/progress/terminal、让 CI 可靠地
识别失败、让审计可关联记录；等确有受控消费者需要重建工具树或实时文字时，再发布
权限隔离的 detail profile。这样既不否认 Claude/Gemini 的可观察性价值，也不把其
最敏感、最易变的一面定格为默认兼容负担。

## 6. 陷阱与证据缺口

- **陷阱。** 收到 Gemini `error`、Claude `api_retry` 或一段不完整 assistant text 都
  不表示终态；必须等 terminal、EOF 和 exit status。[C1][G1]
- **陷阱。** UUID 不是顺序号，session ID 也不是 run ID；不显式提供 `seq` 会迫使
  消费者按接收时间或远程机器时间排序，Claude 文档明确反对后一种做法。[C2]
- **陷阱。** “最后一行是 result”只适用于已正常排空的流。slow consumer、SIGTERM、
  crash 和 broken pipe 都可能让消费者少看到尾部；应记录为交付未知而非伪造一个结果。
  Claude 只公开前者的 30 秒 drain，未公开后面几种的 JSONL 投递保证。[C1]
- **证据缺口。** Codex 0.146.0 的 `--json` help 没有公开 schema；任何针对其字段的
  解析都须先取得该版本的独立规范或可保留的运行样本。[O1]
- **证据缺口。** 本来源集没有证明任何一个产品对 stdout 断管后的 agent 继续运行、
  自动取消、终态写盘或重连重放的精确行为，也没有为工具输出的 secret redaction
  发表逐字段保证。不要把本研究的保守模型误读为厂商承诺。

## References

All sources accessed 2026-08-03.

- **[O1] Local observation:** `codex --version` and `codex exec --help` on the
  research machine; observed version `codex-cli 0.146.0`. This is a
  reproducible observation of an installed binary, not an external schema
  specification.
- **[C1] Anthropic, “Run Claude Code programmatically”** (official Claude Code
  documentation): https://code.claude.com/docs/en/headless.md
- **[C2] Anthropic, “Stream responses in real-time” and “TypeScript SDK
  reference”** (official Claude Code documentation):
  https://code.claude.com/docs/en/agent-sdk/streaming-output.md and
  https://code.claude.com/docs/en/agent-sdk/typescript.md
- **[G1] Google, “Headless mode reference”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/headless/
- **[G2] Google, “Gemini CLI cheatsheet”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/cli-reference/
