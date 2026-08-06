# Model reasoning effort configuration: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Model catalogs, effort levels, and provider
> request fields change quickly; re-check the cited revisions before adoption.
>
> Decision surface: how deployed coding agents and model APIs expose an
> optional per-model reasoning-effort control without conflating it with output
> length, agent step budgets, or visible reasoning summaries.
>
> Scope: model capability discovery, user-selected effort levels, provider
> defaults, unsupported values, and the separation between reasoning controls
> and reasoning visibility. Out of scope: this repository's implementation,
> provider SDK design, training-time test-time compute, and raw chain-of-thought
> disclosure.

## 1. Conclusions

1. **Reasoning effort is a separate control surface.** OpenAI, Anthropic, and
   Gemini expose a control for internal reasoning investment in addition to
   output-token limits. An agent step/time/cost budget and visible answer
   length solve different problems and should not be silently reused as effort.
2. **The valid range belongs to the selected model/provider.** Current Codex
   protocol types represent `ReasoningEffort` as an extensible string and
   advertise model-specific options. Anthropic and Gemini publish different
   level names and, in some generations, fixed thinking-token or adaptive
   alternatives. A client should not translate `high` into a guessed token
   count or claim equal meaning across vendors.
3. **An omitted value should preserve the provider/model default.** Explicit
   user input should remain distinguishable from an absent setting. A product
   should either discover that a model supports the requested value or surface
   the provider rejection; silently downgrading to another level hides a
   material latency/cost/quality change.
4. **Effort and visibility are orthogonal.** OpenAI separates reasoning effort
   from reasoning summaries; Anthropic separates effort from whether thinking
   is summarized or omitted; Gemini's thought inclusion is not the same as its
   thinking level/budget. Turning off visible reasoning does not imply that
   reasoning is disabled.
5. **A small compatibility surface is safer than a fake universal enum.** A
   client can provide a provider-neutral optional string plus capability-aware
   presentation, while retaining provider-specific advanced controls where
   fixed token budgets or adaptive modes are needed.

## 2. Evidence from deployed applications and public product contracts

### 2.1 Codex: model-specific, extensible effort values

The current public Codex app-server protocol defines `ReasoningEffort` as a
string rather than a closed client enum. Its protocol also includes a separate
reasoning-effort option type for advertising values available for a model.
This supports forward-compatible model catalogs: a client can display the
values a selected model actually offers instead of assuming one global
`low|medium|high` table. The source does not make all strings valid for all
models; capability advertisement remains the relevant boundary.

Evidence: [Codex `ReasoningEffort.ts`][codex-effort] and the generated protocol
tree at commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a` (accessed
2026-08-05).

### 2.2 Anthropic: effort, adaptive thinking, and fixed budgets coexist

Anthropic's current public Messages API materials describe `output_config`
effort levels (`low`, `medium`, `high`, `xhigh`, and `max`) for supported
models. Its extended-thinking contract also supports adaptive thinking and an
explicit `budget_tokens` mode for model generations where that control is
appropriate. The product's effort scale is calibrated per model; the same
label is not a cross-provider token promise.

The official Go API schema exposes the same `output_config.effort` field and
lists the current levels, but schema presence alone does not prove that every
model accepts every level. Unsupported combinations remain a request/model
capability error.

Evidence: [Anthropic extended thinking][anthropic-thinking], [Anthropic
official API schema][anthropic-schema] (SDK commit
`0303a8539676836e0cb351f3489fc2d347bbacde`, accessed 2026-08-05).

### 2.3 Gemini: semantic thinking levels and token budgets

Gemini's public thinking documentation describes model-dependent
`thinking_level` values and a separate `thinking_budget` control. Automatic
and disabled/zero-budget behavior are distinct from choosing a semantic
level, and the supported range varies by model generation. This is another
example of a provider retaining both a user-facing scale and a lower-level
budget for compatibility or precise cost control.

Evidence: [Gemini thinking][gemini-thinking] (official documentation, accessed
2026-08-05).

## 3. Mechanisms and tradeoffs

| Choice | Benefit | Risk / boundary |
| --- | --- | --- |
| Omit effort | Keeps the provider's calibrated default | User cannot request a latency/quality tradeoff |
| Explicit provider-neutral string | Forward-compatible and easy to persist/configure | Requires capability-aware validation or clear provider errors |
| Closed universal enum | Simple UI and config validation | Falsely implies equal meaning and blocks new model levels |
| Fixed thinking-token budget | Predictable upper bound for supported providers | Not equivalent to semantic effort and may be invalid for a model |
| Adaptive/provider default mode | Lets the model allocate effort by task | Less predictable cost/latency; must remain visible as a mode |
| Reasoning summary/display toggle | Controls user-facing process detail | Does not change internal reasoning or usage |

## 4. Cross-product synthesis

The most reusable contract is **optional, capability-aware, and lossless**:

1. Keep absent effort distinct from an explicit value and preserve the model's
   default when absent.
2. Treat the selected model's advertised values as authoritative where a
   catalog exists; otherwise pass an explicit value through and surface a
   stable provider error rather than silently map it.
3. Do not use `max_output_tokens`, agent max steps, or a UI summary toggle as
   substitutes for effort.
4. Keep provider-specific fixed budgets/adaptive switches separate from the
   basic effort control unless their semantics are documented for the selected
   provider.
5. Show the effective effort/mode independently from whether reasoning text is
   visible, and avoid promising raw private chain-of-thought.

## 5. Pitfalls and evidence gaps

- A model catalog may advertise levels that a proxy or compatibility endpoint
  does not implement; capability discovery is not a substitute for handling a
  request error.
- `high`/`max` labels do not provide a portable token or quality guarantee.
- Provider documentation often describes supported models but not the exact
  fallback behavior when an unsupported effort is sent.
- A persistent config value raises lifecycle questions: products may apply it
  at startup, per turn, or per task; public docs do not establish one shared
  session/resume contract.
- Current public material does not justify automatically changing effort based
  only on prompt length or tool-call count. Any automatic escalation needs its
  own user-visible policy and budget evidence.

## References

- [Codex `ReasoningEffort.ts`][codex-effort] (public source at commit
  `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`; accessed 2026-08-05).
- [Anthropic extended thinking][anthropic-thinking] (official API
  documentation; accessed 2026-08-05).
- [Anthropic official API schema][anthropic-schema] (official SDK source at
  commit `0303a8539676836e0cb351f3489fc2d347bbacde`; accessed 2026-08-05).
- [Gemini thinking][gemini-thinking] (official API documentation; accessed
  2026-08-05).

[codex-effort]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/ReasoningEffort.ts
[anthropic-thinking]: https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking
[anthropic-schema]: https://github.com/anthropics/anthropic-sdk-go/blob/0303a8539676836e0cb351f3489fc2d347bbacde/message.go
[gemini-thinking]: https://ai.google.dev/gemini-api/docs/thinking
