# 非交互式 Coding Agent CLI 的最终机器输出与流式事件：业界实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-03. Re-verify before adopting; CLI behavior and
> schemas change with releases.
>
> Scope: non-interactive coding-agent CLI 的最终机器可读结果、流式事件、
> stdout/stderr、退出状态以及会话 ID/持久化的可观察契约。
>
> Out of scope: 本仓库实现、未公开的产品内部机制、模型输出质量、工具副作用
> 的原子性或回滚保证。

## 1. 结论

- **跨产品综合。** “单个最终 JSON”与“JSONL 事件流”应当是两个显式选择的
  输出契约，而不能把前者当成后者的最后一行。Claude Code 和 Gemini CLI 都
  公开区分 `json` 与 `stream-json`；观察到的 Codex CLI 只承诺其 `--json`
  把事件写到 stdout 为 JSONL，尚不能由此推出终态事件或其字段。 [O1][S1][S2]
- **跨产品综合。** 对脚本最稳健、且不需要先冻结长寿 JSONL 协议的最小承诺是：
  每次调用在开始前选择最终模式；最终模式在 stdout 输出**恰好一个**有版本的
  JSON 文档；流式模式是另一个可独立演进的可观测性接口。退出码与最终 JSON
  共同判定结果，不能单独依赖任一项。
- **跨产品综合。** 最终文档至少需要传达结果状态、结果或错误、以及会话是否可
  恢复；成本、token、模型和工具轨迹应为可忽略的扩展字段。Claude 已把文本
  结果、会话 ID、元数据和成本暴露在最终/终态结果附近；Gemini 把响应、统计和
  可选错误放进单个 JSON。 [S1][S2]
- **证据边界。** 三个产品没有公布同一个跨厂商 JSONL schema、相同的错误码表，
  或“失败后工作区必定回滚”的保证。因此，不能把字段或退出码同名当成互操作
  标准。

## 2. 术语与决策面

- **最终机器输出（final output）**：一次运行完成后可一次性解析的、边界明确的
  结果文档；它不是终端文本的抓取结果。
- **流式事件（streaming events）**：运行中递增输出的多条记录，适合进度、工具
  调用和诊断，但消费者须面对顺序、截断和协议演进。
- **进程成功**：OS 层退出码为零；它与“已有可解析的最终结果”相关但并非可交换的
  概念。例如，Claude 文档明确说明一次运行内失败可作为 stdout 的结果输出，同时
  进程以非零状态结束。 [S1]

本研究只问一个产品中立的问题：当自动化只需得到“这次 agent 运行最后怎样了”时，
什么最小契约能长期稳定；当它还要显示实时活动时，哪些承诺必须留在独立的流协议中。

## 3. 已发布产品的证据

### Codex CLI 0.146.0：本机版本化观察

- **文档化事实（本机观察）。** 于 2026-08-03 执行 `codex --version` 和
  `codex exec --help`，得到版本 `codex-cli 0.146.0`；`codex exec` 被描述为
  非交互式运行。其 `--json` 唯一相关说明为：`Print events to stdout as JSONL`。 [O1]
- **证据边界。** 这段 help 没有给出事件类型、字段、排序、最后一条是否为终态、
  如何在事件中表示错误，或退出码分类。它也没有把 `--json` 说成单个最终 JSON。
  本文不从该措辞推断任何未列出的 schema 或持久化语义。

### Claude Code：官方 headless/print 文档

- **文档化事实。** 官方文档将 `--output-format` 明确分为默认 `text`、最终的
  `json`（结果、会话 ID 和元数据）与实时的 `stream-json`（逐行 JSON）。最终
  `json` 的文本结果在 `result`；采用 `--json-schema` 时，结构化结果在
  `structured_output`。文档还说明 JSON 有 `total_cost_usd` 和按模型的成本
  分解，但没有在该页发布穷尽字段表。 [S1]
- **文档化事实。** `stream-json` 的每行都是事件；官方保证流的最后一行是
  `result` 消息，含最终响应文本、成本和会话元数据。其文档还说明消费者很慢时，
  CLI 会在退出前等待输出队列排空，等待上限为 30 秒。 [S1]
- **文档化事实。** 成功退出码为 `0`，失败为非零；无效 flag 会在开始运行前写入
  stderr；运行内失败（例如缺少认证）会把失败作为 stdout 的 result 输出。
  会话 ID 可由最终 JSON 取出并交给 `--resume`；文档称其查找范围受当前项目目录
  和 git worktree 限定。 [S1]

### Gemini CLI：官方 headless 与 CLI reference

- **文档化事实。** Gemini 的 `--output-format` 选择为 `text`、`json`、
  `stream-json`；headless 文档把 `json` 定义为一个 JSON 对象，其中
  `response` 是最终答案，`stats` 是 token/API 延迟统计，`error` 是可选的错误
  对象。 [S2][S3]
- **文档化事实。** `stream-json` 是 JSONL，并列出 `init`、`message`、
  `tool_use`、`tool_result`、`error` 和最终 `result` 六类事件；`result` 含聚合
  统计和按模型的 token 使用量分解。这里的 `error` 被说明为非致命告警或系统错误，
  不能只因已收到一个错误事件就断言整次运行失败。 [S2]
- **文档化事实。** Headless 文档公开退出码：`0` 成功、`1` 一般/API 错误、`42`
  输入错误、`53` 超过 turn limit。CLI reference 也公开 `--resume <session-id>`，
  但本来源集没有证明每一次 headless 调用一定产生、保存或在输出中返回 session ID。
  [S2][S3]

## 4. 对照：终态与流并非同一层

| 决策面 | Codex CLI 0.146.0 的已知事实 | Claude Code 的已知事实 | Gemini CLI 的已知事实 |
| --- | --- | --- | --- |
| 最终 JSON | 此次 help 观察未声明。 [O1] | `json` 有 `result`、session ID 和元数据；`structured_output` 可承载 schema 结果。 [S1] | `json` 是包含 `response`、`stats`、可选 `error` 的单个对象。 [S2] |
| 流 | `--json` 将 events 写到 stdout 为 JSONL；schema 与终态未知。 [O1] | `stream-json` 为逐行 JSON；最后一行是 `result`。 [S1] | `stream-json` 为 JSONL，列出事件种类及最终 `result`。 [S2] |
| stdout/stderr | 本次 help 未说明错误通道。 [O1] | 启动前 flag 错误进 stderr；运行内失败可作为 stdout result。 [S1] | 此来源集说明 JSON 可含 `error`，未给出 stdout/stderr 的完整分配规则。 [S2] |
| 退出信号 | 本次 help 未给出分类。 [O1] | `0` 成功，非零失败。 [S1] | `0`、`1`、`42`、`53` 有公开含义。 [S2] |
| 持久化/ID | 本次 `--json` help 未说明。 [O1] | 最终 JSON 可取 `session_id`，可用于按项目/worktree 范围 resume。 [S1] | 可传 session ID 给 `--resume`；产生/保存/输出该 ID 的条件未由这里证明。 [S3] |

## 5. 最小最终输出契约（跨产品综合）

以下是基于上述证据的**产品中立综合**，不是任何一家已经发布的逐字段规范。目标是
让 CI、脚本和调用程序可稳定取得终态，且不要求现在就承诺一条长期 JSONL 协议。

### 5.1 在启动前固定输出模式

一次调用只选择下列一种模式，运行中不隐式切换：

| 模式 | stdout 契约 | 适用边界 |
| --- | --- | --- |
| `text` | 面向人的文本；禁止程序据此解析状态。 | 人工 shell 使用。 |
| `json` | 一个、且仅一个最终 JSON 文档。 | CI、脚本、轮询任务状态。 |
| `stream-json` | 多条 JSONL 事件；另有版本化流文档才可把事件字段当稳定接口。 | 进度 UI、实时日志、调试。 |

**综合结论。** 调用方想要可靠终态时应选择 `json`，不应要求从 `stream-json` 反推
最终对象。Claude 和 Gemini 的终态事件可作为其各自版本的便利能力；Codex 的已观察
help 尚不足以把其 JSONL 当作具有同样终止语义的协议。 [O1][S1][S2]

### 5.2 有界的最终 JSON

最小对象可限制为下列字段；字段名可因产品不同而变，但语义应一次性、明确地提供。

```json
{
  "contract_version": 1,
  "status": "completed",
  "result": "final answer or structured JSON value",
  "error": null,
  "session": {"id": "opaque-id", "persistent": true}
}
```

- `contract_version` 让新增字段或语义变化可被显式协商；消费者应忽略未知字段，
  不应猜测未知版本。
- `status` 至少区分 `completed`、`failed`、`cancelled`。这避免把“有一段文本”或
  “收到一个流事件”误判成成功。
- `result` 在 `completed` 时承载文本或 JSON 值；失败/取消时为 `null`。若产品已有
  schema 结果，可把它作为该值，而不是强制另起一套事件协议。
- `error` 在非完成状态为 `{ "code": "stable-token", "message": "human detail" }`；
  代码应可分类，消息用于诊断。非致命流告警不得替代最终 `status`。
- `session.id` 是不透明值，`persistent` 明确它在本次结束后是否能被恢复。没有该
  明确布尔值时，调用方不能仅凭 ID 存在就推断其生命周期。匿名/一次性运行可返回
  `{ "id": null, "persistent": false }`。

成本、模型、token、耗时、工具摘要和供应商诊断不是最小判定所需字段，可作为向后
兼容的可选元数据。这与 Claude/Gemini 将统计或成本作为结果附近的元数据、而不是
以它们决定终态的做法一致。 [S1][S2]

### 5.3 转换、退出与通道语义

- **转换。** 输出模式在 agent 工作开始前锁定；`text` 和 `stream-json` 都不会在
  运行末尾自动“升级”为最终 JSON。若调用方同时要实时显示与稳定终态，应使用两个
  明确接口（例如流只作显示、完成后另取最终结果），或先发布独立、带版本的流协议。
- **stdout。** `json` 模式仅写最终对象，不能混入 banner、进度、警告或 ANSI 控制
  序列。这样一个解析器能把整个 stdout 当一份 JSON，而不是扫描最后一行。
- **stderr。** 只承载诊断、启动失败和非机器协议的运行信息；调用方可以保存它，
  但不得解析它来构造业务结果。Claude 的“启动前 stderr、运行中结果 stdout”正说明
  错误归属必须在契约中写清楚。 [S1]
- **退出。** 最小跨环境保证是 `0` 仅表示 `status=completed`，任何
  `failed`/`cancelled` 以非零退出。细分退出码可以另行发布（Gemini 的 `42`/`53`
  是一个例子），但在未有版本化表之前，消费者只能依赖零/非零与最终对象。 [S2]
- **错误完成与硬中断。** 输出模式已建立且运行能正常收束时，即使失败或取消，也应
  写一个最终对象再退出；在被杀死、崩溃或 pipe 断开而无法 flush 时，允许只有非零
  退出与缺失最终对象。后者必须被消费者标为“结果未知/未完成”，不能冒充 `failed`
  或 `completed`。

## 6. 陷阱与证据缺口

- **证据缺口。** 对 Codex 0.146.0，这次仅有 `--help` 的 JSONL 说明；没有公开
  schema、终态事件、stdout/stderr、错误码或 `--json` 关联会话字段的证据。任何对其
  JSONL 解析都应先针对版本取得独立规范或运行样本。
- **证据缺口。** Claude headless 页面列出命名字段和终态事件，但没有在所引用页面
  给出穷尽、版本化的 `json`/`result` JSON Schema；Gemini 文档则未在本来源集中
  声明 JSON 与 stderr 的完整并发规则。不要把这两份文档的示例扩张为跨版本保证。
- **陷阱。** 流中的 `error` 不必是终态失败：Gemini 明确称其可为非致命告警/系统
  错误；而 Claude 明确把运行内失败作为 stdout result。应等最终状态和进程退出后
  再作判定。 [S1][S2]
- **陷阱。** “有 session ID”“能传 `--resume`”和“这个 headless 运行已持久化且
  可恢复”不是同义命题。Claude 的结果 ID/resume 关系有文档支撑；Gemini 的来源集
  只证明 resume 接受 ID；Codex 的此处证据未涉及此点。 [O1][S1][S3]
- **证据缺口。** 本来源集未建立任一产品会因最终 JSON 为失败而撤销已执行的工具或
  工作区修改。调用方不能将最终输出契约误解为事务或回滚契约。

## References

All sources accessed 2026-08-03.

- **[O1] Local observation:** `codex --version` and `codex exec --help` on the
  research machine; observed version `codex-cli 0.146.0`. This is a
  reproducible observation of that installed binary, not an external schema
  specification.
- **[S1] Anthropic, “Run Claude Code programmatically”** (official Claude Code
  documentation): https://code.claude.com/docs/en/headless.md
- **[S2] Google, “Headless mode reference”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/headless/
- **[S3] Google, “Gemini CLI cheatsheet”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/cli-reference/
