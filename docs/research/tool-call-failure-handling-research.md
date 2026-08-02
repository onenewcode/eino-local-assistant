# 工具调用失败处理：industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-07-16. Re-verify before adopting; vendor behavior changes.
>
> Scope: 主流 coding agent / agent SDK 在**工具调用失败**时的分层策略——错误如何表示、是否回灌模型、runtime 是否重试、**重试粒度（单次 tool 执行 vs 整次 model 请求 vs 整轮 agent）**、**改参后再调是否算新调用/会否重复副作用**、何时熔断、与 API 传输层重试的边界。
>
> Out of scope: 权限策略细表、上下文压缩算法、本仓库落地映射、通用 LLM 可靠性教程。

## 1. Summary

- **行业主收敛**：工具执行失败几乎从不“崩循环”；默认把失败写成 **与成功同形态的 tool result**，回灌模型，由模型改参数/换工具/向用户解释。这是自愈循环的基础，而不是旁路异常。
- **“重试”不是一种东西**：至少要拆成 **R0 传输重试 / R1 单 tool 同参再执行 / R2 模型改参再声明 tool / R3 用户 continue 整段任务**。混淆这四层会错误判断“会不会重复执行命令”。
- **成熟 coding agent（Claude Code 等）对 shell 失败通常不做 R1**：失败结果回灌 → 模型发 **新的** tool call（新 `call_id` / `tool_use_id`）。同参盲重放整次 HTTP 请求（R0）仅在“尚无可见输出/尚无已提交 tool”时发生。
- **若 AI 改参数**：协议上几乎总是 **新的 tool invocation**，不是“同一 call 的第 N 次 attempt”。Runtime 若没有 **idempotency key / 副作用账本**，改参与同参一样会再次执行；**call_id 不会帮你去重**，它只做 request↔result 配对。
- **框架侧自动 R1 存在但可选且应收窄**：LangChain `toolRetryMiddleware`、LangGraph `RetryPolicy` 对 **单次 tool 调用或单节点** 同参再跑；Google ADK Reflect&Retry 按 **per-tool** 计数并逼模型反思——默认仍不保证幂等写工具安全。
- **危险边**：可见流式输出后再失败时重放整轮 → 同一批 tool 可执行两次（Claude Code 因此禁止）；agent 层无脑 retry 写工具 → 重复扣款/发邮件（Stripe AI 类 issue）；无诊断同参 loop → 烧 turn。
- **惊群 / retry storm**：纯指数退避若无 **jitter**、无 **预算**、无 **背压**，多会话/多 tool/多 subagent 会在 429/5xx 恢复窗口同步开火，把瞬时故障放大成持续过载；agent 场景下每次 R2 还附带**整段上下文 token 成本**，比 HTTP 惊群更贵。

## 2. Problem boundary

容易混在一起的四类“失败”：

| 层 | 谁先处理 | 典型信号 | 常见策略 |
| --- | --- | --- | --- |
| Provider / 网络 | runtime | 5xx、529、429、超时、断流 | 指数退避；可配置次数；部分场景禁止重试 |
| 工具执行 | tool host → 模型 | 异常、exit≠0、业务错误、超时 | **错误结果回灌**，继续 agent loop |
| 工具调用形状 | runtime 解析/校验 | 未知工具名、JSON 非法、schema 不符 | 回灌可修错误 / `repairToolCall` / 抛 `ModelBehaviorError` |
| 循环与安全 | agent policy | 死循环重试、max_turns、权限拒绝、认证失效 | 硬停、人机审批、切换模式 |

术语（业界常用，命名不一）：

- **Soft tool failure**：工具跑完了但结果是错的（含 `is_error=true` / stderr / `tool-error` part）；循环可继续。
- **Hard runtime failure**：无法安全继续（协议破损且不可补、预算耗尽、鉴权失败）；应 stop 并带 end reason。
- **Error-as-result vs raise**：同一异常在不同配置下可变成“给模型看的字符串”或“向上抛、结束 run”。
- **Idempotency risk**：传输层重试若重放整轮 assistant+tools，可能对非幂等工具二次执行。
- **Retry unit（重试单元）**：被“再来一次”的最小边界——HTTP 请求、单个 tool handler 调用、图节点、还是整次用户 turn。
- **Attempt vs re-invocation**：Attempt = runtime 对**同一逻辑调用**（同 call_id / 同绑定参数）再执行；Re-invocation = 模型（或用户）发出**新的** tool 声明（新 id，参数可同可不同）。

与《工具调用控制与终止》的关系：那篇管“何时继续/结束”；本文管“失败时如何表示、是否重试、如何避免坏重试”。

## 3. Retry 粒度：单命令、整批 tool、还是整轮？

这是上一版调研写得不够细、但**最容易设计错**的决策面。

### 3.0 四种“重试”必须分开记账

```text
用户 turn
  └─ model request #1  ──R0──►  (API 失败时可能再发同一请求)
        │
        ├─ tool_use A (call_id=c1, args=P) ──R1──► 同 c1/同 P 再 exec（middleware）
        ├─ tool_use B (call_id=c2, args=Q) ──R1──► 只动 B，不动 A
        └─ tool results 回灌
  └─ model request #2
        └─ tool_use C (call_id=c3, args=P' 可能改参) ──R2──► 新 id，全新执行
  └─ (用户说 continue / 新 prompt) ──R3──► 任务级重开，不保证跳过已做副作用
```

| 代号 | 名称 | 重试单元 | 谁触发 | 参数是否可变 | 新 call_id？ | 典型产品机制 |
| --- | --- | --- | --- | --- | --- | --- |
| **R0** | 传输/模型请求重试 | **一次** `messages.create` / Responses 请求（尚未或未安全提交 tool 副作用时） | runtime | 输入应相同（同 history） | 若重放成功，模型可能再吐 **同一批** tool（危险） | Claude Code 自动重试 5xx/429；**有可见输出则不重试** |
| **R1** | 单 tool 执行重试 | **一个** tool handler 调用（固定 name+args） | runtime middleware / 节点 RetryPolicy | **不可变**（同参再跑） | **否**（仍服务原 call_id 的 result） | LangChain `toolRetryMiddleware`；LangGraph node `RetryPolicy` |
| **R2** | 模型自愈再调 | **新的** tool_use / function_call | 模型看 error result 后决策 | **可变**（改参/换工具/换命令） | **是（新 id）** | Claude Code / Codex / Agents SDK 默认 agent loop |
| **R3** | 任务/用户级重试 | 整段任务或 subagent | 用户 “continue” / 运维重跑 job | 任意 | 全新一轮 | Claude incomplete notice 后用户 continue；CI 重跑 |

**确保“重的是单个命令还是全部”的方法**：先给每次失败贴标签——是 R0/R1/R2/R3 哪一种。  
行业默认分工是：

- **瞬时网络 → R0 或 R1（读/幂等工具）**  
- **命令失败、测试红、路径错 → 几乎全是 R2**（不做 R1 同参 shell 重跑）  
- **用户继续 → R3**，不自动撤销已写文件

### 3.1 单次 tool vs 并行一批 vs 整轮

#### 并行多 tool 时 runtime 通常怎么做

模型一次响应里可能发出 `shell` + `read` + `grep` 等多个 call。

| 失败形态 | 主流行为 | 是否“全部重试” |
| --- | --- | --- |
| 仅 tool B 抛错/超时 | **B 写 error result**；A/C 成功 result 仍回灌；再调模型（R2） | **否**——不重跑 A/C |
| ToolNode / middleware R1 | 仅对失败的那次 invoke 做同参 attempt | **否**——按 **单 call** |
| R0 重放整次 model 请求 | 若模型回复含多个 tool_use，**整批可能再次被执行** | **是整批**——这是最危险路径 |
| 图节点 `RetryPolicy` 包住整个 `tools` 节点 | **整节点**同 state 再进（可能重跑该节点内所有 tool） | **节点粒度**，不是“用户整会话” |

LangGraph 文档与工程文的共识：`RetryPolicy` **只重试失败的那个 node**，用**相同输入**再跑，不是重放整张图；也不是“用户说的全部历史”。但若 `tools` 节点内部一次执行了多个 tool_call，节点级 retry 的副作用边界取决于 ToolNode 实现是否幂等、是否在部分成功后重入——**产品文档很少保证 partial-success 后节点级 retry 的细语义**，实现时需单测。

LangChain `toolRetryMiddleware`：

- 作用域：可选 **全部 tool** 或 **指定 tool 名列表**  
- 行为：对**失败的那次 tool 调用**做 `maxRetries` 次同参再执行（R1）  
- 耗尽后：`onFailure: 'continue'` → 该 call 的 error `ToolMessage` 回模型（进入 R2）；`'error'` → 整 agent 停  
- **不会**因为 tool A 失败而自动重跑 tool B

Google ADK Reflect & Retry：

- **per-tool** 失败计数（不是 per-call_id 的全局无差别计数 alone；文档写 *failure counts are tracked per-tool*）  
- `max_retries`：每个 tool 最多额外 N 次  
- 机制是拦截失败 → **给模型结构化反思指引 → 再试**（偏 R2 + 计数熔断，而不是静默同参 R1）  
- 支持 `tracking_scope`：单次 invocation 或 global  

Claude Code / Codex 类 coding agent：

- shell 非零退出：**R2 only**（结果进 transcript，模型再决定下一条命令）  
- 公开错误文档**不**描述“同一 bash 命令自动重跑 3 次”的 R1  
- 传输失败：R0，且用 **“是否已有可见输出”** 决定能否重放（见下）

#### Claude Code：R0 何时会“把全部 tool 再跑一遍”

官方 [Error reference](https://code.claude.com/docs/en/errors) 写得很硬：

- 连接在**任何可见输出之前**断开 → **re-issue the request**（R0，同输入再请求）  
- 已有可见输出后 server error → **不重试**，保留 partial + incomplete notice  
- 原因写死：**re-running the request could execute the same tools twice**

含义拆开：

1. R0 的单元是 **model HTTP 请求**，不是“单个 shell 字符串”。  
2. 若第一次流式响应里**已经出现 tool_use 且 runtime 已执行**，再 R0 就可能重复副作用——因此产品选择 **禁止 R0**。  
3. “可见输出”是启发式安全门（文本或已展示内容）；**不是**精细的“已执行 call_id 集合”账本的公开描述，但目标是同一类问题。  
4. 用户 `continue` 是 **R3**：模型在已有 partial 历史上继续，**不是** runtime 对旧 call_id 做 R1。

OpenAI Agents SDK：

- 默认 **R1 关闭**（失败 → `failure_error_function` 生成 result 字符串 → 回模型 = R2）  
- 超时：`error_as_result`（一次 result，不自动再 exec）或 `raise_exception`（停）  
- 文档未见“timeout 后自动同参再跑 N 次”的内建 R1；若需要，由工具内部或外层 middleware 实现  

### 3.2 AI 改参数：会不会“重复”、算不算同一次重试？

#### 协议事实（跨厂商）

1. 每个 tool 声明有独立 id：`tool_use.id` / `call_id` / `tool_call_id`。  
2. Result **必须**挂回该 id；**不能**把新执行结果挂到旧 id 上冒充“第 2 次 attempt”（除非你的 runtime 从未把第一次结果提交给 API）。  
3. 模型下一轮即使调用**同名工具、几乎相同参数**，也会生成 **新 id** → 在协议上是 **R2 re-invocation**，不是 R1 attempt。  
4. 因此：**改参不会“续上”旧 call 的 attempt 计数**——除非产品在应用层用 **指纹** 自己记账。

```text
Turn N:   shell(cmd="go test ./...")  id=c1  →  exit 1 + stderr  (soft fail, R2 path)
Turn N+1: shell(cmd="go test ./pkg")  id=c2  →  全新执行
Turn N+1': shell(cmd="go test ./...") id=c3  →  仍是全新执行（同参也新 id）
```

**结论 A**：改参 → 一定是新执行机会；runtime **默认不会**说“这是 c1 的第 2 次 attempt，跳过”。  
**结论 B**：不改参、模型又调一次 → **也会再执行**，除非你有去重层。  
**结论 C**：`call_id` **只保证配对，不保证幂等**。

#### 业界如何（不）处理“重复”

| 策略 | 谁做 | 指纹键 | 改参后 | 同参再调 |
| --- | --- | --- | --- | --- |
| 无去重（coding agent 默认） | — | — | 再执行 | 再执行 |
| R1 middleware 同参 attempt | runtime | 绑定当前 call 的 args | 不适用（未到模型） | 同 call 内最多 N 次 |
| per-tool 失败计数（ADK Reflect） | plugin | tool **name**（文档级） | 计入该 tool 的失败次数 | 计入；超限停/抛 |
| 应用幂等键 | tool 实现 | `Idempotency-Key` / 业务主键 | 新键→新副作用；同键→返回缓存 | 同键安全 |
| 环检测 hook | agent 策略 | `(tool, args_hash)` 最近 K 次 | 新 hash → 放行 | 重复则拦截/逼解释 |
| Stripe 类支付 tool | 工具层必须 | 逻辑操作 id | 依赖业务 | 同参应返回原收据 |

工程共识（多篇 agent idempotency 文 + Stripe AI issue 一类反馈）：

- **Agent 层“失败就整段 retry”** 若没有工具幂等，会 **重复扣款/重复发信**。  
- 正确默认：  
  - **读/搜索/list**：可 R1 或同参 R2  
  - **写/支付/发邮件/apply_patch**：默认 **禁止静默 R1**；R2 靠模型；工具内 **idempotency key**  
  - **shell**：coding agent 把“再跑”交给模型（R2）；同参再跑测试往往**有意为之**（修代码后验证），不能用“同 args 指纹永久拒绝”

#### “修代码后再跑同一命令” vs “盲目同参重试”

这是 coding agent 特有的岔路：

| 场景 | args 指纹相同？ | 环境/仓库状态 | 是否应拦截 |
| --- | --- | --- | --- |
| `go test` 失败 → 模型改 `.go` → 再 `go test` | 命令字符串可相同 | **状态已变** | **不应**按 args 去重拦截 |
| `go test` 失败 → 无文件变更 → 再 `go test` | 相同 | 状态未变 | 可告警 / 计次后熔断 |
| `rm -rf` / `git push` / `stripe.charge` | 相同或略改 | 副作用 | 审批 + 幂等键 + 禁止 R1 |

因此：**不能**只对 `args_hash` 去重；合理指纹常是：

```text
fingerprint = hash(tool_name, args, optional workspace_revision / git_head / filesystem_epoch)
```

主流公开 CLI **很少**做完整 workspace_revision 指纹；更多依赖 **max_turns、用户中断、prompt“先诊断再换策略”**。这是已知缺口，不是已解决标准。

### 3.3 对照：各系统“重的是什么”

| 系统 | 默认同参自动再执行？ | 单元 | 改参后再调 | 防重复手段 |
| --- | --- | --- | --- | --- |
| Claude Code | shell **否**（R2）；API **条件 R0** | R0=请求；工具失败=结果回灌 | 新 tool_use，再执行 | 可见输出后禁用 R0；max turns/budget |
| OpenAI Agents SDK | function tool **否**（error result） | 单 tool → 一条 output | 新 call，再执行 | `max_turns`；可选自写 R1 |
| Codex harness | shell **否**（exit/stderr 回灌） | 单命令结果 | 新 function_call | 审批/sandbox；社区仍见命令连错 |
| LangChain toolRetryMiddleware | **是（可选 R1）** | **单次** tool call | 之后仍可 R2 | `retryOn` 收窄；`onFailure` |
| LangGraph RetryPolicy | **是（节点级）** | **失败 node** 同 input | 另一轮图步进 | `retry_on`；recursion_limit |
| LangGraph ToolNode handle_errors | **否**（转 ToolMessage） | 单 tool 错误串 | R2 | 无默认同参熔断 |
| Google ADK Reflect&Retry | 偏 **R2+计数** | **per-tool** 次数 | 继续直到超限 | max_retries per tool |
| AI SDK multi-step | 执行错 → `tool-error` part | 单 tool | 下一步可新 call；可有 `repairToolCall` | `stopWhen`；repair 对未知工具可 return null |

### 3.4 设计检查清单（行业实践提炼）

回答“你如何确保重试是单命令还是全部”时，成熟设计会显式回答：

1. **这次失败的 retry class 是 R0/R1/R2/R3 哪一个？**  
2. **单元边界**：一个 `call_id`、一个图 node、一次 HTTP、还是用户 turn？  
3. **并行兄弟 tool**：失败时是否保留已成功兄弟的 result 且不重跑它们？（期望：**是**）  
4. **改参**：是否新 id？是否清零 attempt 计数？（期望：**新 id；attempt 计数按新调用或按业务幂等键**）  
5. **同参再调**：读工具可放行；写工具是否需要 key / 审批 / 指纹+workspace 版本？  
6. **R0 安全门**：是否在“已执行任意非幂等 tool”或“已有可见 output”后禁用请求重放？  
7. **账本**：是否记录 `executed_call_ids` / idempotency store，而不是只靠模型自觉？

### 3.5 惊群（thundering herd）与 agent retry storm

上一版调研**未展开**此题。它与「重试单元」（§3.0–3.4）正交但强耦合：**单元决定“谁再跑”**；惊群决定**“多少人同时再跑、何时再跑”**。

#### 问题定义（在 agent 里长什么样）

经典分布式：大量客户端在几乎同一时刻对恢复中的服务发起重试 → 二次压垮。

Agent 系统会**放大**同一机制，并多出几条路径：

| 形态 | 触发 | 同步点 | 放大物 |
| --- | --- | --- | --- |
| **A. 多会话 R0 对齐** | 共享 429/5xx/断网恢复 | 退避到点后集体开火 | 对 LLM provider 的 QPS |
| **B. 多 tool 并行 R1** | 一批并行 tool 同时超时/5xx | 同 `initial_interval` 无 jitter | 对下游 API |
| **C. 多 agent / subagent** | 父 agent 拉起 N 个子任务，共享依赖 | 同时失败 → 同时退避结束 | N 倍 tool + N 倍 model |
| **D. 用户重复提交（R3）** | UI 无响应，用户刷新/再发 | 第二、第三实例叠在原实例上 | 实例数 × 重试 |
| **E. 模型 R2 “语义惊群”** | 瞬时故障被写成 soft error | 模型立刻再调（几乎 0 退避） | **整段 context token** + 下游调用 |
| **F. Watchdog 无上限** | CI/无人值守拉满重试 | 多 job 同配置 | 对 capacity 的持续锤 |

工程文对 agent 特有放大的共识（如 [Retry Storm in Agentic Systems](https://tianpan.co/blog/2026-04-10-retry-storm-problem-agentic-systems)）：

1. **每次“再试”往往不只是再打 HTTP**：R2/整轮恢复会把 **完整对话上下文** 再送进模型 → token 成本远高于微服务重试。  
2. **重试决策部分在模型里**：prompt 无法可靠禁止“换个等价 tool 再打同一后端”，纯 circuit breaker 挂在单一 HTTP client 上会漏。  
3. **用户重试**会叠出多实例（pattern D），传统 per-client 退避管不到。

#### 业界在机制层怎么防

**1) 指数退避 + jitter（防 A/B 时间对齐）**

- 经典参考：[AWS Exponential Backoff and Jitter](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/) — **Full Jitter** / Equal Jitter / Decorrelated Jitter；无 jitter 的纯 `2^n` 会让客户端在同一时刻齐射。  
- **LangGraph `RetryPolicy`**：`jitter=True` 为默认；文档/工程文直接写 *avoid thundering herd*。  
- **LangChain `toolRetryMiddleware` / `modelRetryMiddleware`**：默认 **jitter ±25%**（JS 文档），专门打散 tool/model R0/R1。  
- **Claude Code**：公开文档写 **exponential backoff**、最多约 10 次（可 env 调）；**未在错误页写明 jitter 算法**（是否 full jitter 需再核实现）。有 `CLAUDE_CODE_RETRY_WATCHDOG` 对 429/529 **近无限重试** —— 在**多 CI job / 多用户**场景下，若缺少跨实例抖动与全局背压，**会放大 F 类惊群**（单机会内部退避，集群仍可能对齐在 rate window 边界）。

**2) 预算与熔断（防 E 烧 token、防无限风暴）**

| 层 | 手段 | 惊群作用 |
| --- | --- | --- |
| Tool R1 预算 | maxAttempts、总时长 | 限制单 call 齐射次数 |
| Agent turn/费用 | max_turns、max_budget | 限制 R2 语义重试 |
| per-tool 失败计数 | ADK Reflect&Retry | 限制“同一 tool 名”连打 |
| 错误分类 | transient 才 R0/R1；逻辑错只 R2 且有限次 | 避免对 4xx 齐射 |
| 系统级 | concurrency limit、queue、circuit open | 防 D 与跨 agent 级联 |

仅 jitter **不够**：服务整体 down 时，所有客户端仍会在 cap 后的窗口内持续重试 → 需要 **fail-fast / 降级 / 换 provider**，而不是更“均匀”的锤。

**3) 背压与去重（防 C/D）**

- 编排层：**每用户/每线程 inflight=1**（或可配置上限），重复提交排队或拒绝，而不是再 spawn agent。  
- **共享下游**（同一 MCP、同一 DB）：按 dependency 的并发上限 + 全局限流，而不是每 tool 独立 R1 无协调。  
- 多 subagent：父级应感知子级 429/overload，**串行化或降低 fan-out**，避免 C 形 N 倍惊群。

**4) R0 安全门与“恢复窗口齐射”**

Claude Code 的“可见输出后不重放”解决的是 **重复 tool 副作用**，不是惊群本身；但减少无效 R0 也降低了齐射密度。  
反过来：大量会话在 **无输出阶段** 同时 529 → 退避结束仍可能 herd；此时靠 **jitter + 全局限流 + 不要无限 watchdog 全集群同开**。

**5) Agent 特有：R2 几乎无退避**

Coding agent 默认路径是 soft fail → **下一轮模型立刻再想**（R2）。这在单用户交互里可接受，但在：

- 后端持续 500  
- 模型每次换参仍打同一 host  
- 无 max_turns  

时会形成 **高频语义惊群**：不是时钟对齐，而是 **token 驱动的紧密环**。缓解：

- tool 结果里带 `retryable_after_ms` / `Retry-After` 提示（模型不一定遵守）  
- **runtime 强制**：对同一 `host+error_class` 注入 cooldown，再允许下一 tool 执行  
- 结构化 error taxonomy：`transient` vs `modified` vs `terminal`（工程建议；产品默认程度不一）

#### 与 §3.0 四类重试的交叉

| 重试类 | 惊群敏感度 | 最低防护 |
| --- | --- | --- |
| R0 传输 | **高**（多会话同步） | exp backoff + **jitter** + max；跨实例勿同 seed；慎无限 watchdog |
| R1 单 tool | **中高**（并行 tool 同延迟） | 仅 transient；per-tool jitter；不要默认对所有 error 重试 |
| R2 模型再调 | **高成本**（token 风暴） | max_turns / per-tool 预算；cooldown；勿把 5xx 当“再想想就好”无限循环 |
| R3 用户/任务 | **高**（多实例） | inflight 去重；idempotency；UI 防重复提交 |

#### 产品对照（惊群相关，能核实的）

| 系统 | 公开的 herd 相关机制 | 缺口 / 注意 |
| --- | --- | --- |
| Claude Code | exp backoff；R0 有次数；可见输出禁 R0 | jitter 未文档化；RETRY_WATCHDOG 集群场景危险 |
| LangGraph RetryPolicy | backoff + **jitter 默认 true** + retry_on | 节点级；多节点仍可能独立齐射 |
| LangChain tool/model retry middleware | backoff + **±25% jitter**；retryOn 过滤 | 默认 retry 过宽时仍 herd |
| OpenAI Agents SDK | tool 默认 error-as-result（抑 R1 风暴） | R2 仍靠 max_turns；无全局 herd 协调 |
| Codex 类 | shell 偏 R2 | 模型连错命令 = 本地“语义风暴”，靠 turn/用户停 |
| ADK Reflect&Retry | per-tool max_retries | 防单 tool 死循环，不防多会话 herd |

#### 设计要点（可复用）

1. **R0/R1 必须 jitter**；纯 `sleep(2^n)` 在多副本下不够。优先 full/decorrelated jitter（AWS 系结论）。  
2. **Jitter 解时间对齐，不解过载**：系统性故障要 circuit + 降级，而不是更大 max_retries。  
3. **Agent 成本函数不同**：限制的是 **token×次数×fan-out**，不只是 HTTP QPS。  
4. **R2 需要独立刹车**：max_turns、per-tool 失败预算、可选下游 cooldown；不要假设模型会“等等再试”。  
5. **多 agent / 用户双提** 必须在编排层 inflight 去重。  
6. **并行 tool**：R1 应对 **每个 call 独立 jitter**，避免一批 timeout 后同一毫秒重打。  
7. **可观测**：retry ratio、waste tokens、cascade depth（工程文建议）；没有指标就看不到 herd。

## 4. Industry mechanisms

### 4.1 Provider 契约：失败如何进入对话

#### Anthropic Messages API

官方 [Handle tool calls](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)：

- 客户端工具：执行后发 `user` 消息，内容含 `tool_result`：`tool_use_id` + `content` + 可选 **`is_error: true`**。
- **硬格式要求**：每个 `tool_use` 必须有紧随其后的对应 `tool_result`；user 消息里 `tool_result` 必须排在任意 free text 之前；否则 400（如 *tool_use ids were found without tool_result blocks immediately after*）。
- 设计含义：**即使失败也要回一条配对结果**，不能省略 result 来表示错误。

社区/工程实践（如 Claude Agent 循环写作者）一致采用：参数错误、执行异常 → `is_error=true` + 可读说明，让 Claude 下一轮自修。

#### OpenAI function / Responses tool output

[Function calling](https://developers.openai.com/api/docs/guides/function-calling) 侧：

- tool output 格式**由应用自定**（JSON / 纯文本 / 错误码字符串均可）；模型解读字符串。
- 无统一的 wire 级 `is_error` 字段；行业惯例是内容里写清 `error` / 失败原因，或 SDK 层包装。

#### 对比表

| 能力 | Anthropic | OpenAI（function/tool output） |
| --- | --- | --- |
| 失败布尔标记 | `is_error` | 无标准字段；靠 content |
| 必须配对 result | 是（协议硬约束） | 是（call_id / tool_call_id 配对） |
| 失败后默认是否继续 | 由 agent 决定；API 允许继续 | 同上 |

### 4.2 Claude Code（产品级 coding agent）

一手：[Error reference](https://code.claude.com/docs/en/errors)。

**A. 传输层自动重试（不是工具语义重试）**

- 5xx、overload、超时、临时 429、中途断连等：**最多约 10 次指数退避**（`CLAUDE_CODE_MAX_RETRIES`，有上限；`CLAUDE_CODE_RETRY_WATCHDOG` 可对容量错误拉长）。
- **不重试**：证书校验失败（立即失败以便用户修配置）；Bedrock 错误 content-type（重试同样会被网关改写）。
- **关键安全决策（v2.1.199+）**：已出现**可见流式输出**后的 server error → **保留 partial**，附加 incomplete-response notice，**不重放请求**——因为重放可能**重复执行工具**。无可见输出的断连仍可重试。

**B. 工具语义失败 → 模型自愈**

- 文档明确：**built-in tool 拒绝输入时，Claude 多数会自己纠正**；仅少数需用户改配置（如 subagent tools 解析为空、Read deny 导致 Edit 被拒）。
- 权限/策略拒绝、工具错误结果进入 transcript，作为下一轮上下文（与 [agent loop](https://code.claude.com/docs/en/agent-sdk/agent-loop) 中 “denied tool 以 rejection 回灌” 一致）。

**C. 子 agent / 中断**

- 子 agent 遇终端 API 错误：报告 *Agent terminated early due to an API error*（v2.1.199+），不再把 API 错误文本伪装成 subagent 正常 result。
- 有部分文本输出时，前台 subagent 可把 incomplete 结果交回主 agent。

**D. 产品提示层（社区对源码的归纳，次级来源）**

公开 deep-dive 描述 Claude Code 将 tool error 与 success **同一 message 格式**回灌，并在 system 侧引导：“失败时先读错误、诊断再换策略，不要原样盲重试”。四层恢复常被归纳为：过长上下文 / 输出 token 顶 / 连续 overload 换 fallback model / 工具错误自然回灌。其中工具层与官方 `is_error` 语义一致；其余层是 product 级恢复，采用前宜对照当前版本。

### 4.3 OpenAI Agents SDK

一手：[Tools](https://openai.github.io/openai-agents-python/tools/)。

**Function tool 失败默认 = error-as-result**

- `@function_tool` 支持 `failure_error_function`：
  - **默认**：`default_tool_error_function` → 把“发生了错误”类消息送给 LLM，**run 继续**。
  - **自定义**：你的函数返回字符串 → 仍回灌 LLM。
  - **显式 `None`**：异常向上抛（可能是 `ModelBehaviorError` 非法 JSON，或 `UserError` 业务崩溃）→ **由调用方/结束 run**。
- 手动构造 `FunctionTool` 时，错误必须在 `on_invoke_tool` 内自行处理。

**超时（async function tools）**

| `timeout_behavior` | 行为 |
| --- | --- |
| `error_as_result`（默认） | 模型可见超时文案，如 `Tool 'x' timed out after N seconds.` |
| `raise_exception` | 抛 `ToolTimeoutError`，run 失败 |

可用 `timeout_error_function` 定制超时文案。

**与终止的衔接**（见既有终止调研）：`max_turns`、`tool_use_behavior`、未知 tool / 审批拒绝可配置为抛错或回灌；`error_handlers["max_turns"]` 可把硬停变成受控 final。

### 4.4 Codex CLI（coding agent harness）

次级但较细的源码阅读：[Exploring the OpenAI Codex CLI Source Code](https://zenn.dev/takiko/articles/e2b8065158c8d0)（2025-04，针对当时 TS 版结构；**采用前需对应当前 Rust/CLI 版本**）。

可核对的设计倾向：

1. **工具执行尽量不抛穿 agent loop**：`rawExec` 类路径 **Promise 总 resolve**；致命调用错误也变成 `ExecResult{ exitCode≠0, stderr }`。
2. **语义**：对 AgentLoop 而言“tool 调用成功完成”，但结果是错误输出 → 下一轮模型看到 exit/stderr 并决策。
3. **顶层 catch**：API/stream 异常转 system 提示给用户，**不拖垮 CLI 进程**。
4. **prompt 侧**：引导在 pre-commit 等失败时有限重试后向用户说明环境坏了，而不是无限修。

社区反馈（如 [incorrect terminal commands](https://community.openai.com/t/codex-agent-executes-many-incorrect-terminal-commands/1355773)）：模型会反复试错命令（跨平台命令差异等），说明 **error-as-result + 无专用“同调用熔断”** 时，依赖 max_turns/用户中断控制成本。

### 4.5 LangGraph `ToolNode`

一手：[ToolNode](https://reference.langchain.com/python/langgraph.prebuilt/tool_node/ToolNode) `handle_tool_errors`。

配置面很全：

| 值 | 行为 |
| --- | --- |
| `True` | 捕获所有异常 → `ToolMessage` 默认错误模板 |
| `str` | 捕获全部 → 固定错误串 |
| `Exception` / tuple | 只捕获指定类型 |
| `Callable` | 捕获后自定义错误文本 |
| `False` | **不处理，异常上抛**，图可崩溃 |
| 默认 callable | 文档：捕获**调用/参数类**错误并给出描述；**执行期错误默认 re-raise**（与“全开 True”不同——版本注意） |

社区反复出现的坑：

- “Tool not found” 若在 lookup 阶段抛死，**来不及**变成 `ToolMessage`，模型无法自修（[forum 讨论](https://forum.langchain.com/t/allow-graceful-handling-of-tool-not-found-errors-in-langgraph-toolnode-to-enable-agent-self-correction/2427)）。
- 校验错误内容过短（如 “Field required”）→ 模型无诊断线索 → **同错误重试上百次**（[forum](https://forum.langchain.com/t/raising-tool-call-errors-so-agents-can-be-self-healing/3152)）。说明 **error-as-result 必要但不充分**，错误文本必须可操作。

图级还有 `RetryPolicy`（节点重试）与 `recursion_limit`（步数熔断），与 tool 回灌是不同轴。

### 4.6 Vercel AI SDK

一手：[Tool Calling — Handling Errors / Tool Call Repair](https://ai-sdk.dev/docs/ai-sdk-core/tools-and-tool-calling)。

分层清晰：

1. **Schema / 未知工具**：`NoSuchToolError`、`InvalidToolInputError` 等——`generateText` 可抛；也可进 repair 路径。
2. **执行失败**：`execute` 抛错 → 成为 **`tool-error` content part**，在 multi-step 中参与下一轮，而不是默认杀死整个 generate。
3. **`repairToolCall`**：对坏参数可二次模型/结构化输出修复；可对 `NoSuchToolError` 选择 **不修**（返回 null）。
4. 观测：`onToolExecutionEnd` 区分 `tool-error` 与成功 output。

这是把“校验失败 / 执行失败 / 可选修复”产品化得比较完整的一套 API。

### 4.7 OpenHands SDK

一手：[Exception Handling](https://docs.openhands.dev/sdk/guides/llm-error-handling.md)。

重点在 **LLM/provider 异常类型化**（auth、rate limit、timeout、context window、malformed action、function validation、unknown function 等），由应用 `try/except` 处理；context 溢出可走 condenser 而非硬崩。

对 **工具业务失败**，文档主线是 provider 错误与 action 解析错误；工具 runtime 失败仍应回到“结构化 action result / observation”一类反馈（OpenHands 传统 agent 循环），与 error-as-result 同族。另有 issue 显示：严格 schema 字段缺失若变成 **不可恢复校验错误**，会阻断自愈（例如缺 `security_risk` 字段）。

### 4.8 Google ADK（补充）

- 推荐工具返回 **`{status: success|error, ...}` 结构**，而不是裸抛，以便模型与插件处理。
- [Reflect and Retry plugin](https://adk.dev/integrations/reflect-and-retry/)：拦截 tool failure，给模型结构化反思指引，**按工具粒度**计数重试。
- 插件钩子 `on_tool_error` / `on_model_error` 提供全局恢复；历史上 MCP 工具失败曾 **未捕获导致整图崩溃**（社区讨论），说明“默认是否 catch”仍是实现质量点。

### 4.9 横向对照（机制，非品牌话术）

| 系统 | 工具执行失败默认 | 传输失败 | 坏 tool call 形状 | 熔断/预算 |
| --- | --- | --- | --- | --- |
| Claude Code | 结果回灌；多数自愈 | 最多 ~10 次退避；可见输出后不重放 | 协议层 400 需 harness 配对 result；产品修 transcript | max_turns / budget / 用户中断 |
| OpenAI Agents SDK | `failure_error_function` 回灌 | SDK/应用重试 | `failure_error_function=None` 可抛 `ModelBehaviorError` | `max_turns`、timeout raise |
| Codex harness | exit/stderr 当结果 | 顶层 catch 提示用户 | 依赖模型再试（社区可见盲目重试） | 审批/sandbox + 会话限制（版本相关） |
| LangGraph ToolNode | 可配置；默认偏参数错回灌 | `RetryPolicy` 另轴 | unknown tool 若未转 message 则崩 | `recursion_limit` |
| AI SDK | `tool-error` part 继续 multi-step | 应用层 | 可 `repairToolCall` | `stopWhen` 等 |
| OpenHands | 类型化 LLM 错 + observation 循环 | 应用处理 typed errors | validation/not exists 类异常 | condenser / cancel |
| Google ADK | 结构化 error 响应 + 可选 Reflect&Retry | 插件/回调 | schema/回调校验 | max retries per tool |

## 5. Efficient / reasonable patterns

成熟系统收敛到的可复用模式：

### 5.1 永远配对 tool result（协议正确性优先）

- 有 tool call 就必须有 result（成功或失败）。
- Anthropic 下失败仍写 `tool_result` + `is_error`；OpenAI 下写 tool 角色消息，content 说明失败。
- **绝不**用“不回 result”表示错误——会 400 或破坏多轮。

### 5.2 Error-as-result 作为工具层默认

- 工具 host catch 异常 → 模型可读文本。
- 包含：错误类型、关键参数、**可执行下一步建议**（缺哪个字段、合法枚举、是否应用另一工具）。
- OpenAI Agents 的 `failure_error_function`、LangGraph `handle_tool_errors`、AI SDK `tool-error`、Codex 的 exit/stderr 同属一族。

### 5.3 分离“可重试传输”与“不可重放执行”（并写清单元）

| 场景 | 建议 | 单元 |
| --- | --- | --- |
| 无副作用前的 API 失败 | 指数退避 **R0** + **jitter** | 单次 model HTTP 请求，**不是**单个 shell |
| 已开始 tool 或已有可见 side-effect 输出 | **禁止 R0**；incomplete + 用户/模型 R3/R2 | 已执行 call 不得静默再跑 |
| 瞬时工具网络错误（读/幂等） | 可选 **R1** 同参 + **per-call jitter** | **单个** tool invoke |
| 命令失败 / 测试红 / 参数错 | **禁止 R1**；error-as-result → **R2** | 新 call_id；允许改参 |
| 非幂等写工具 | 工具 **idempotency key**；禁止 agent 整段盲重试 | 业务操作 id，不是 call_id |
| 并行 tool 部分失败 | 成功者 result 保留；只给失败者 error | **按 call**，不整批重跑 |
| 多会话 429/5xx 恢复 | jitter + 全局限流；慎集群统一无限 watchdog | 跨实例，不只单进程 |
| 用户连点/刷新 | inflight 去重，勿并行第二 agent | R3 实例边界 |

Claude Code 对 mid-stream server error 的处理是 R0 **副作用**安全门的产品级范例；**惊群**还要另加 jitter/预算/背压（§3.5）。

### 5.4 错误分类矩阵（设计时显式化）

```text
                    回灌模型          runtime 重试        硬停/人机
传输瞬时故障           可选              是（有上限）        预算耗尽后
工具业务失败           是（默认）          否*               连续同失败 N 次
参数/schema 错         是 + 诊断文案      否                可选 repair 1 次
未知工具名             是（列举可用）      否                或严格模式抛
权限拒绝               是（说明策略）      否                或 ask 用户
鉴权/配额/组织禁用       否                否/有限            是
```

\* 工具层“重试”若存在，应是 **模型换参再调**，或 **框架对 transient 工具**（网络 fetch）做有限次，而不是无脑同参重放。

### 5.5 有诊断的错误文本 > 布尔 is_error

`is_error` 帮助模型区分“正常空结果”与“失败”，但自愈靠 content：

- 坏：`"Error"` / `"Field required"`
- 好：`"write_file failed: path '/x' is outside workspace. Use relative path under /repo. Available tools: ..."`

### 5.6 防止失败驱动的死循环（区分同参 R1 与改状态后 R2）

与终止策略配合：

- `max_turns` / recursion_limit / 费用预算  
- **R1**：仅对标记为 transient 的异常、且工具声明幂等时启用；默认不要对 shell 开  
- **R2 环检测**：`(tool, args_hash)` 连续命中可告警；但 **git_head/workspace 变化后应清零**，否则会误杀“修代码后再测”  
- **per-tool** 失败预算（ADK Reflect&Retry）比“全局无 retry 整段任务”更安全  
- prompt：失败后先诊断再换策略  
- 超时默认 error-as-result（一次 result，不自动 R1），避免 hung tool 卡死  

### 5.7 Shell/测试工具：失败即信号（故意允许“同命令再跑”）

Coding agent 不应把 `go test` 非零退出当 runtime 崩溃，也**不应**用纯 args 指纹永久去重：

- 结构化回传：exit code、stdout/stderr 截断策略、时长  
- 期望路径是 R2：读日志 → 改代码 → **再发新 call_id 的同一命令**  
- 无文件变更的同命令连打：可用计数熔断；有变更的同命令：放行  

### 5.8 何时不用 error-as-result

- 内部不变式破坏、会话状态已损坏  
- 安全策略要求停止（例如检测到恶意路径且不允许继续探索）  
- 自动化流水线需要 **非零进程退出码** 而非“模型解释失败”  
此时 OpenAI Agents 的 `failure_error_function=None` 或 LangGraph `handle_tool_errors=False` 更合适。

## 6. Pitfalls

1. **把 R0（整次 model 请求重放）当成“只重试失败的那条命令”** → 并行 tool 会整批再跑；Claude Code 用“可见输出”门禁正是防这个。  
2. **把 R2（模型改参新 call）当成 R1（同 call 再 attempt）** → 误以为改参不会重复副作用；实际上 **新 id = 新执行**。  
3. **对 shell 默认开 R1 同参自动重跑** → 非瞬时错误会空转；行业 coding agent 更倾向 R2。  
4. **用纯 `args_hash` 永久去重** → 误杀“改代码后再 `go test` 同一命令”。  
5. **Agent 任务级 retry 无工具幂等** → 重复扣款/发信（Stripe AI 类问题）。  
6. **工具异常向上抛死整个 agent** → 一次 MCP 抖动毁掉长会话。  
7. **校验失败无诊断 + 无 turn 上限** → 同参 R2 烧数百次。  
8. **省略失败 tool_result** → 协议 400 / transcript 损坏。  
9. **可见输出后仍 R0** → 同一批 tool 执行两次。  
10. **把 API 错误伪装成 subagent 成功 result** → 主 agent 误判完成。  
11. **error-as-result 泄露栈/密钥**。  
12. **节点级 RetryPolicy 包住多 tool 节点却未定义 partial success** → 可能重入已成功 call。  
13. **混淆 exit≠0 与 harness 故障** → 前者 R2；后者 UI/停。  
14. **有 backoff 无 jitter** → 多副本在 2^n 边界齐射（经典惊群）。  
15. **有 jitter 无熔断** → 服务整体 down 时仍被“均匀地”持续锤。  
16. **只限 R0/R1、放任 R2** → token 语义风暴（上下文全量重送）。  
17. **全集群打开无限 RETRY_WATCHDOG 类开关** → capacity 错误时的集群级 herd。  
18. **用户重复提交再 spawn agent** → 实例倍增叠在原重试上。

## 7. Open questions

- **当前 Codex（Rust 主路径）** 是否仍完全遵循早期 TS 文中的 “rawExec 永不 throw / 仅 ExecResult” 细节，需对最新源码再核。  
- Anthropic 官方 `is_error` 页对错误类型的完整分类在部分抓取中被折叠；实现前应打开全文核对。  
- 并行 tool 部分成功时，**节点级 / 批级 retry** 是否跳过已成功 `call_id`：公开文档保证弱，需实现单测。  
- Claude Code “可见输出”启发式与“已执行 call_id 集合”是否等价：文档只保证前者。  
- ADK Reflect&Retry 的 per-tool 计数在 **改参成功后是否清零**、是否按 invocation 隔离：需读插件源码确认。  
- MCP `isError` 与 harness 映射在版本间漂移。  
- workspace_revision 指纹（git HEAD + 脏文件哈希）是否应成为 coding agent 默认环检测——**目前不是公开默认**，但是合理开放设计点。  
- Claude Code / Codex **R0 backoff 是否带 jitter、何种算法**，公开错误文档未写死，需读源码。  
- 多 subagent 是否共享下游 rate limiter：产品文档少见，属实现细节。  
- 是否应在 tool_result 中标准化 `Retry-After` / `retryable_after_ms` 并强制 runtime 遵守（模型不遵守时）。

## References

- Anthropic: [Handle tool calls](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)（`tool_result` / `is_error`）  
- Claude Code: [Error reference](https://code.claude.com/docs/en/errors)（R0 条件重试、*same tools twice*、incomplete response）  
- Claude Code: [How the agent loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop)（结果回灌与终止，交叉）  
- OpenAI: [Function calling](https://developers.openai.com/api/docs/guides/function-calling)（output 格式由应用决定；call_id 配对）  
- OpenAI Agents SDK: [Tools — Handling errors / timeouts](https://openai.github.io/openai-agents-python/tools/)（`failure_error_function`、`timeout_behavior`；默认非 R1）  
- LangChain: [toolRetryMiddleware / Model retry](https://docs.langchain.com/oss/javascript/langchain/middleware/built-in)（R1 单 tool；与 model retry 分列）  
- LangGraph: [ToolNode `handle_tool_errors`](https://reference.langchain.com/python/langgraph.prebuilt/tool_node/ToolNode)；工程文对 `RetryPolicy` 节点级同输入重试的说明  
- Vercel AI SDK: [Tool Calling — Handling Errors / repairToolCall](https://ai-sdk.dev/docs/ai-sdk-core/tools-and-tool-calling)  
- OpenHands: [Exception Handling](https://docs.openhands.dev/sdk/guides/llm-error-handling.md)  
- Google ADK: [Reflect and Retry plugin](https://adk.dev/integrations/reflect-and-retry/)（per-tool 计数、max_retries）  
- Idempotency / 重复副作用: [Stripe AI #402 agent-level retry duplicate charges](https://github.com/stripe/ai/issues/402)；行业文对 tool 幂等键与 agent retry 的讨论  
- Codex 源码阅读（次级）: [Exploring the OpenAI Codex CLI Source Code](https://zenn.dev/takiko/articles/e2b8065158c8d0)  
- 社区失败模式: [LangGraph tool not found](https://forum.langchain.com/t/allow-graceful-handling-of-tool-not-found-errors-in-langgraph-toolnode-to-enable-agent-self-correction/2427)、[tool error self-healing / 249 retries](https://forum.langchain.com/t/raising-tool-call-errors-so-agents-can-be-self-healing/3152)、[Codex incorrect commands](https://community.openai.com/t/codex-agent-executes-many-incorrect-terminal-commands/1355773)  
- 相关本库调研（边界交叉，非本主题证据）: `docs/research/tool-call-control-termination-research.md`  
- 惊群 / retry storm: [AWS Exponential Backoff and Jitter](https://aws.amazon.com/blogs/architecture/exponential-backoff-and-jitter/)；[Retry Storm Problem in Agentic Systems](https://tianpan.co/blog/2026-04-10-retry-storm-problem-agentic-systems)；LangGraph `RetryPolicy.jitter`；LangChain tool/model retry middleware jitter；Claude Code automatic retries / `CLAUDE_CODE_RETRY_WATCHDOG`（[Error reference](https://code.claude.com/docs/en/errors)）
