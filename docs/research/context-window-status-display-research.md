# Agent CLI 上下文状态栏：业界呈现实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-07。产品和实现会演进，采用前应重新核验。
>
> 范围：交互式 agent CLI 如何向用户呈现模型上下文窗口、已用量和剩余量，以及内部输入预算与用户可见进度的关系。
>
> 不在范围：各供应商未公开的 tokenizer 细节、模型实际可用的全部私有容量、或任何本仓库的实现映射。

## 1. 结论

- **跨产品综合：** 主流产品把用户可见的主指标表述为“上下文已用”或“上下文剩余”，其参照物是模型的完整 context window，而非“总窗口减去本次最大输出后的内部输入预算”。
- **跨产品综合：** 为输出和压缩预留 token 是正常的内部准入/触发机制；这不表示预留后的数值适合冒充用户配置的 context-window 总量。
- **适用边界：** “下一次请求的本地估算”与“最近一次 API 返回的上下文使用量”不是同一种测量。前者可用于预测和阻止超窗，但应明确标为 estimate；后者适合显示为 context used/left，仍要标注它是上一次请求的快照。
- 因而，`next≈1.1k/15.9k (6%)` 这类表达若处于主状态栏，容易让用户误以为模型窗口是 `15.9k`，或误以为这是已由 provider 确认的 context 使用率。公开证据不支持把它称为主流 UX。

## 2. 已公开的产品与实现证据

### Codex：优先显示 context remaining，未知容量时才退化为已用 token

**开源实现快照，非长期产品合同。** Codex 的 TUI footer 在有百分比时渲染 `N% context left`；只有无法取得百分比时才退化为 `N used`。当前快照测试还展示了 `Context 100% left` 的状态行。这是以完整窗口的剩余比例为用户语义，而不是暴露请求构建器的剩余输入预算。
[Codex footer renderer](https://github.com/openai/codex/blob/9afb96faffc2d679788f2192f9efce5451a0d7f2/codex-rs/tui/src/bottom_pane/footer.rs#L999-L1011) 与 [footer snapshot](https://github.com/openai/codex/blob/9afb96faffc2d679788f2192f9efce5451a0d7f2/codex-rs/tui/src/chatwidget/snapshots/codex_tui__chatwidget__tests__status_line_model_with_reasoning_context_remaining_footer.snap)（快照提交 `9afb96f`；访问于 2026-08-07）。

**开源实现快照，非长期产品合同。** 同一 TUI 的百分比来自 `model_context_window` 与最近 token usage 的 remaining percentage；有窗口大小时不会同时以累计 token 数取代百分比。这表明 UI 的“剩余”是状态快照，和模型窗口绑定。
[Codex chat widget token-info handling](https://github.com/openai/codex/blob/9afb96faffc2d679788f2192f9efce5451a0d7f2/codex-rs/tui/src/chatwidget.rs#L1159-L1182)（快照提交 `9afb96f`；访问于 2026-08-07）。

### Gemini CLI：显示 prompt 用量占模型 token limit 的百分比

**开源实现快照，非长期产品合同。** Gemini CLI 的 `ContextUsageDisplay` 显示 `N% used`，颜色随压缩阈值变化。它调用 `getContextUsagePercentage(promptTokenCount, model)`；该函数以 `promptTokenCount / tokenLimit(model)` 计算比例。这里的分母是模型 token limit，而不是扣除输出预留后的可用输入预算。
[Gemini CLI ContextUsageDisplay](https://github.com/google-gemini/gemini-cli/blob/2139b121bc028e0b4c96b97385555b19c2dd629d/packages/cli/src/ui/components/ContextUsageDisplay.tsx) 与 [percentage calculation](https://github.com/google-gemini/gemini-cli/blob/2139b121bc028e0b4c96b97385555b19c2dd629d/packages/cli/src/ui/utils/contextUsage.ts)（快照提交 `2139b12`；访问于 2026-08-07）。

该组件测试固定了 `5,000 / 10,000 -> 50% used`、`10,000 / 10,000 -> 100% used` 的用户可见合同。
[Gemini CLI ContextUsageDisplay tests](https://github.com/google-gemini/gemini-cli/blob/2139b121bc028e0b4c96b97385555b19c2dd629d/packages/cli/src/ui/components/ContextUsageDisplay.test.tsx)（快照提交 `2139b12`；访问于 2026-08-07）。

### Claude Code：向状态栏提供总窗口、最近 API token 快照及已用/剩余百分比

**产品文档。** Claude Code 的 status line 输入包含：最近一次 API 响应所处窗口的 input/output token 数、最大 `context_window_size`、预先计算的 `used_percentage` 和 `remaining_percentage`。文档同时说明这些字段在会话初期可能为 null，且 percentage 与 `/context` 的值可能因计算时机而不同。它明确区分了可显示的状态快照和显示时机，而不是把内部请求预算当作窗口总量。
[Claude Code status line documentation](https://code.claude.com/docs/en/statusline#context-window-fields)（访问于 2026-08-07）。

**产品文档。** Claude Code 的示例直接输出 `used_percentage`，并建议状态栏输出保持短小，说明其把 context percentage 当作常驻、面向人的状态信号。
[Claude Code status line examples and tips](https://code.claude.com/docs/en/statusline#context-window-usage)（访问于 2026-08-07）。

### OpenCode：输出预留用于 overflow 判断，不构成其 TUI 显示合同

**开源实现快照，非长期产品合同。** OpenCode 在 overflow 判断前计算 usable input：当 provider 未单列 input limit 时，以 `context - maxOutputTokens` 作为上限；否则还会采用 compaction reserve。它用这个值决定是否 overflow/触发自动压缩。这是“预留输出预算”的明确工程证据。
[OpenCode overflow handling](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/opencode/src/session/overflow.ts#L7-L34)（快照提交 `69f2cba`；访问于 2026-08-07）。

**证据边界。** 同一快照可见 OpenCode 向 Agent Client Protocol 传递 `used` 与完整 `size`，但本次未找到足以证明其自有 TUI 精确标签的公开证据。因此它能支持“内部必须留预算”，不能支持“状态栏应把 usable input 当作总窗口分母”。
[OpenCode ACP usage update](https://github.com/anomalyco/opencode/blob/69f2cbaa3ab875bd1a1cf4392ea68f207b7966d8/packages/opencode/src/acp/usage.ts#L196-L232)（快照提交 `69f2cba`；访问于 2026-08-07）。

## 3. 机制与取舍

| 概念 | 合理数据来源 | 合适的用户标签 | 不应混同为 |
| --- | --- | --- | --- |
| 模型 context window | 模型配置/供应商能力 | `context window 20k` | 本次请求的 input cap |
| 最近上下文使用量 | 最近 API usage | `context 16.3k/20k`、`18% left` | 下一次请求的精确预测 |
| 下一请求输入预测 | 本地 prompt planner/token estimator | `next input approx. 1.1k` | provider 已确认的 usage |
| 输出预留 | `max_output_tokens`、compaction safety reserve | `output reserve up to 4.1k`（诊断视图） | 用户配置的窗口大小 |

内部通常需要满足：`预计输入 + 允许输出 + 安全余量 <= 完整窗口`。这是发起请求前的准入条件；而状态栏回答的是“当前会话在窗口中处于什么位置”。两者可以在诊断页并列，却不应让后者的标签或分母掩盖前者的完整窗口。

## 4. 跨产品综合

**跨产品综合，不是统一产品合同。** 三个有直接 UI/状态栏证据的产品虽然措辞不同：Codex 用 `context left`，Gemini CLI 用 `% used`，Claude Code 提供 used/remaining percentage 供状态栏选择；它们的共同点是用户看见的 percentage 以完整 context window 为参照。没有一份证据将“输出预留后的 input budget”作为面向用户的 context total。

**跨产品综合，不是统一产品合同。** 最不易误解的紧凑表达必须先说明 measurement 语义。例如在有 provider 快照时可采用“`context <used>/<window> (<percent>)`”；本地 estimate 则可作为补充“`next input approx. <tokens>; output reserve <= <tokens>`”。当没有 API 快照时，显示未知/估计状态比制造看似精确的 context percentage 更诚实。

## 5. 风险与证据缺口

- `promptTokenCount`、cache token、reasoning token 和 output token 的包含范围会因供应商而异；相同的 `N%` 不能被理解为跨产品严格可比的计费指标。
- 本地 tokenizer estimate 与 provider tokenizer 可有差异；即使 UI 显示低于阈值，运行时仍应保留服务端 overflow 处理。
- Codex 和 Gemini 的这里是开源实现快照，不保证任何未来版本仍使用相同文字或计算；Claude Code 的文档描述 status-line data contract，但用户最终可配置自定义脚本。
- 未找到足够公开证据来断言 OpenCode 自有 TUI 如何逐字呈现 context；本笔记不据此推测其视觉行为。

## References

- OpenAI Codex TUI footer 和 token-info 源码快照（提交 `9afb96f`），访问于 2026-08-07。
- Google Gemini CLI context-usage 组件、计算与测试源码快照（提交 `2139b12`），访问于 2026-08-07。
- Anthropic Claude Code status line 文档，访问于 2026-08-07。
- OpenCode overflow 和 ACP usage 源码快照（提交 `69f2cba`），访问于 2026-08-07。
