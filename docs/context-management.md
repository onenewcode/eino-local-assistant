# 上下文管理

本文件定义会话上下文、自动压缩和恢复的产品合同。实现位置主要是
`internal/chat`、`internal/contextbuild` 与 `internal/store`。

## 配置

`[model.context]` 只有 `window_tokens`：它是模型的完整物理上下文窗口，
必须与实际部署相符。它不是“输入预算”，状态栏与 `/context` 也绝不将
输出预留后的数值伪装成模型窗口。

上下文策略是产品合同，不能成为用户调出的第二套资源预算。

## 为什么没有输出上限配置

`max_output_tokens` 看起来像“限制回答字数”，实际同时影响工具循环、推理、
上下文准入和服务商协议。把它设成固定的本地值会在模型之间产生不可预测的
截断，并诱使 UI 将“窗口减预留”误报为真实窗口。因此没有公开配置或 catalog
元数据。

OpenAI 与 OpenAI-compatible Chat Completions 请求不发送 `max_tokens` 或
`max_completion_tokens`，让服务端/模型使用自己的默认行为。

Anthropic Messages API 要求 `max_tokens`。程序在每次请求内部计算它：从完整
窗口的 95% 安全上限中扣除本次消息、已绑定工具 schema、协议 framing 和保守
tokenizer guard，至少发送 `1`。它是满足传输合同的剩余容量，不是面向用户的
回复长度偏好，不能配置。

## 固定策略

所有比例都相对完整的 `window_tokens` 计算：

| 规则 | 固定值 | 含义 |
| --- | ---: | --- |
| 自动压缩触发 | 85% | 仅在已完成 turn 的稳定边界创建 checkpoint |
| 请求安全上限 | 95% | 每次模型请求的本地准入 ceiling |
| 压缩目标 | <= 50% | checkpoint 成功后希望达到的上下文占用 |
| 热 tail | 12 个完整 turn group | 不拆用户/工具事务，自动压缩优先保留它们 |
| checkpoint 目标 | <= 2k tokens | 防止 checkpoint 本身逐次膨胀 |
| 最小有效释放 | 20% | 低于此值的 checkpoint 不安装 |

没有 75% 的后台预取压缩：它会产生可能计费的 provider 调用，却不一定会被下一
轮使用。

## 准入、压缩与工具事务

Session 在创建新 turn 前进行权威检查，覆盖 TUI、队列、headless、`resume` 和
`--last`。它估算消息、工具 schema 与 framing；ReAct 在每一个 provider 调用前
再次检查。因此 UI 的主动压缩只是优化，不能决定正确性。

若下一请求会遗漏尚未被 checkpoint 覆盖的完整历史 group，session 会先尝试在稳定
边界压缩；若不能安全完成，会以可操作错误阻止请求，绝不静默丢历史。压缩不能在
工具调用与结果之间运行：工具结果先提交到账本；若下一次 ReAct 调用仍超出安全
上限，该 turn 安全失败，既不重跑工具，也不假设工具没有副作用。

自动压缩一旦已到达 provider 调用但未成功，会写入失败 lifecycle，保留旧
checkpoint 并暂停自动重试。用户仍可使用 `/compact` 显式重试；恢复不会替用户
重试一笔可能已计费的压缩调用。

## 压缩如何进行

压缩生成一个新的 checkpoint，而不是删掉对话。原始 JSONL 事件保持不变；checkpoint
只是在下一次模型请求中代替其已经覆盖的历史前缀。它有完整的来源 event ID、来源
hash、父 checkpoint ID 和 lineage hash，因此恢复时可验证它对应的确切账本范围。

### 一次自动压缩的实际算法

1. **从账本重放完整 group。** 每个已提交 turn 是不可拆分的 group：用户输入、
   assistant tool call、相应 tool result 与最终回答必须一起保留或一起压缩。tool result
   在账本中有完整 artifact payload；普通模型视图和 compactor 只接收带
   `artifact://...` 引用的有界预览，不会把原始大输出重复塞进 prompt。
2. **计算当前模型视图。** 视图固定为 `system prompt + active checkpoint + 未覆盖的
   连续 turn group 后缀 + 当前用户输入`。规划器从最新 group 向旧选择，超过 95%
   ceiling 后只会省略连续旧前缀，绝不截断 group 内的一条 tool result 或消息。
3. **判断是否需要自动压缩。** 已完成 turn 的完整候选视图达到 85%，且它已经超过
   50% 目标或有 group 放不下时，标记为需要压缩。若没有 compactor、正在执行 tool
   transaction、自动压缩已暂停或没有可压缩前缀，则不会发出压缩请求。
4. **选择确切来源。** 自动路径先保留最近 12 个完整 group；候选是更早的未覆盖
   连续前缀。若这 12 个 group 中有某个已经放不下，它和它之前的前缀也会加入候选，
   防止上下文静默遗漏。手动 `/compact` 把热 tail 也作为候选。候选的全部 event ID 与
   对应内容 hash 在请求前冻结。
5. **建立无工具压缩请求。** session 先写入 `context.compaction.started`，随后以同一
   基础模型发送两个消息：固定的 checkpoint system prompt 与一个 JSON 数据 prompt。
   数据 prompt 包含任务目标、触发原因、冻结的来源范围和 hash、上一个 checkpoint
   （如有）以及完整候选 group。压缩模型没有任何 tool schema，也不能调用工具。
6. **得到结构化 checkpoint。** 返回必须是一个 JSON 对象，包含 `task_goal`、
   `constraints`、`confirmed_facts`、`decisions`、`attempts_and_results`、
   `files_or_artifacts`、`open_questions` 与 `next_actions`。每个事实或决定都必须带
   `source_refs` 和 `confidence`；来源只能指向这次输入实际展示的 event 或父
   checkpoint。空回答、Markdown、tool call、无效 JSON、来源不匹配或大于 2k tokens
   都视为失败。
7. **超长来源按 group 分块。** 单个压缩请求超过 95% ceiling 时，只在完整 group
   边界分块；先把各块压成临时 checkpoint，再将临时 checkpoint 合并为一个最终
   checkpoint。临时 checkpoint 不写入 JSONL。最多进行四层递归归并；一个 group 自身
   已经超出上限时不截断它，而是让压缩失败。
8. **重新规划并 CAS 安装。** 最终 checkpoint 与 system prompt、未覆盖热 tail 和当前
   输入重新规划。它必须实际可进入 prompt，且相对压缩前至少释放 20% token；然后以
   冻结的 revision 做 CAS，成功后才写 `context.compacted` 并把候选前缀标记为 covered。
   下一轮请求就成为 `system prompt + 新 checkpoint + 热 tail + 当前输入`。

例如有 18 个未覆盖的已完成 group，自动路径的正常候选是 group 1--6，group 7--18
保持展开。安装成功后，模型不再读取 group 1--6 的原始消息，而读取它们的 checkpoint；
group 7--18 仍按原顺序展开。若 group 10 已经无法进入 95% ceiling，候选会扩大为
group 1--10，不能把 group 10 的工具结果单独丢掉。

完成的每个 compactor provider 调用都先记录 usage，即使之后 JSON 校验或 CAS 安装
失败，账本仍能反映这次可能计费的调用。

### 失败与下一步

| 情况 | 行为 |
| --- | --- |
| 调用前发现无候选或候选 stale | 不花 provider 调用；下一稳定边界重新规划 |
| provider 返回空、无效、低收益或请求失败 | 持久化 `compaction.failed`，保留旧 checkpoint，暂停自动重试 |
| 用量已记录后安装 CAS 失败 | 持久化失败，不安装过期结果，也不丢失 usage |
| 仍有 active tool transaction | 不压缩；先落盘工具结果，下一 ReAct 调用超限则安全失败 |
| 用户执行 `/compact` | 显式创建新操作，可在暂停状态后重试，但不覆盖历史或重跑工具 |

自动路径没有“失败后再试几次”的隐式循环。因为一次失败可能已经产生 provider 成本，
它需要用户的显式 `/compact` 决定才会再次收费。

## 持久化与恢复

单会话 JSONL 是恢复真相。SQLite 仅作为可重建的 session catalog，绝不承担
消息、工具输出、checkpoint 或 transcript 的恢复。checkpoint 是 JSONL 中的模型
上下文投影，不删除或替代原始用户消息、工具调用和结果。

每个完成工具结果的原始 payload 一次性保存在该会话 JSONL artifact 中；
`tool.completed` 只保留 artifact 引用和有界模型投影。正常 TUI 只显示一行摘要，
不渲染原始 JSON 或多行输出。并行 tool batch 共享模型可见预览额度，避免多个大
结果同时撑满下一次请求。

`resume <id>` 在会话锁内重放并校验 JSONL 的 sequence、revision、hash 与
lifecycle；不会从 SQLite 恢复状态。持久会话仅自动修复无歧义的损坏尾行；中间
损坏、活跃 turn 或 pending compaction 默认拒绝。`--recover` 才能以 CAS 关闭
中断 lifecycle，且绝不重跑工具或压缩。`--ephemeral` 只在副本中执行读取、修复和
恢复，源 JSONL 与 durable catalog 保持不变。

## 状态显示

provider 已返回 usage 时，状态栏显示 `ctx=<used>/<window>`；本地规划但尚未发出
请求时显示 `ctx≈<estimate>/<window>`。两种形式都以完整窗口为分母，且不将本地
估算说成 provider 事实。`/context` 同时列出 85% 触发点、95% 安全上限、50% 目标、
最近 provider 快照、当前本地估算和自动压缩暂停原因。
