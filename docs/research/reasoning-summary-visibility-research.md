# Reasoning summary visibility: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Provider response shapes and CLI display
> controls change quickly; re-check the cited sources before adoption.
>
> Decision surface: how deployed coding agents expose, collapse, or suppress
> model reasoning summaries while keeping effort controls, tool evidence, and
> machine-readable output separate.
>
> Scope: default visibility, progressive disclosure, interactive toggles,
> transcript views, and separation of reasoning display from final content.
> Out of scope: this repository's implementation, raw chain-of-thought
> disclosure, provider SDK design, and claims about the faithfulness of model
> generated explanations.

## 1. Conclusions

1. **Visibility is an independent product policy.** Effort controls how much
   reasoning a model may invest; visibility controls what the user sees. A
   client should not infer either setting from the other.
2. **Progressive disclosure is the common interaction shape.** Claude Code
   collapses thinking by default and offers a transcript/verbose view on
   demand. Codex keeps summary-style reasoning events and raw reasoning
   content behind separate controls. Aider formats reasoning separately from
   the answer and removes it before normal response processing.
3. **The default should show work state and evidence before detail.** A short
   activity/status line, a compact summary, and tool results answer the user's
   immediate questions without requiring a long stream of internal text.
   Detailed reasoning or transcript material is better behind an explicit
   toggle or viewer.
4. **A display toggle must not change model semantics accidentally.** Hiding a
   summary should change presentation only unless a product explicitly
   documents a separate provider request control. It should not disable model
   reasoning, alter effort, or remove tool results from the durable audit path.
5. **Machine output needs a stricter boundary than a human TUI.** A human view
   may offer an opt-in expanded transcript, while a structured stream can
   omit reasoning text and expose stable activity/result events. Raw or
   provider-redacted content should not become an implicit CLI contract.

## 2. Evidence from deployed applications

### 2.1 Claude Code: collapsed thinking and an explicit transcript viewer

Claude Code documents extended thinking as reasoning emitted before a response
and says that effort is the primary control for adaptive-reasoning models. Its
display controls are separate: thinking output is collapsed by default, and
`Ctrl+O` toggles verbose mode. The documentation also describes redacted
thinking blocks by default for interactive Anthropic API sessions and an
optional `showThinkingSummaries` setting for full summaries when expanded.

The interactive-mode documentation describes the same `Ctrl+O` action as a
transcript viewer for detailed tool usage and execution, including timestamps
and the model used on assistant messages. MCP calls are collapsed into a
one-line aggregate by default. This is a viewer boundary, not a change to the
underlying tool execution.

These are documented product behaviors. They do not establish that displayed
thinking is a complete or faithful private chain of thought.

Evidence: [Claude Code model configuration, extended thinking, and display
settings][claude-model-config] and [Claude Code interactive mode and
transcript viewer][claude-interactive] (accessed 2026-08-05).

### 2.2 Codex: separate summary-event and raw-content controls

The current public Codex configuration contract has independent controls for
hiding `AgentReasoning` events and showing
`AgentReasoningRawContentEvent` events. The former defaults to visible; the
latter defaults to hidden. The same configuration area separately carries
model reasoning effort and reasoning summary settings.

This gives Codex a three-way distinction: a productized reasoning event can be
visible, raw reasoning content can remain disabled, and the model's effort or
requested summary mode can still be configured independently. The source does
not imply that every provider supplies the same event types or that raw
content is a stable cross-provider contract.

Evidence: [Codex TUI/config reasoning visibility settings][codex-config] at
commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a` (accessed 2026-08-05).

### 2.3 Aider: reasoning is formatted separately from the answer

Aider's current CLI announces a selected reasoning effort or thinking-token
budget alongside the model. Its response path formats reasoning content with
distinct markers, while a separate cleanup path removes reasoning-tag content
from the normal partial response before it is processed as the answer.

This is a different UX from Claude Code's transcript viewer, but it preserves
the same important boundary: reasoning display is not allowed to become the
ordinary answer/edit payload. The source shows how Aider handles content that
is present; it does not promise that every model emits reasoning or that the
formatted section is an authoritative internal trace.

Evidence: [Aider announcement and response rendering][aider-base-coder] and
[Aider reasoning tag handling][aider-reasoning-tags] (accessed 2026-08-05).

## 3. Mechanisms and tradeoffs

| Layer | Typical content | Default posture | Tradeoff |
| --- | --- | --- | --- |
| Activity/status | Working state, current tool, elapsed time | Visible | High signal, low detail; must not pretend to be model reasoning |
| Compact summary | A short model/product summary or folded preview | Visible or lightly folded | Helps orientation but is generated text, not proof |
| Detailed transcript | Expanded thinking, tool inputs/results, timestamps | Explicit viewer/toggle | Useful for debugging; can overwhelm and expose sensitive data |
| Raw provider reasoning | Raw or provider-specific content | Off unless explicitly supported | Unstable semantics and stronger disclosure risk |
| Machine events | Stable lifecycle/activity/result records | Contract-defined, often no reasoning text | Easier to consume and audit, less conversational detail |

The display path can be modeled independently from the model request:

```text
model effort / provider response
          |
          +--> durable or ephemeral runtime events
                       |
                       +--> compact human view
                       +--> expanded transcript view
                       +--> machine-readable projection
```

The branches may choose different redaction and aggregation rules. They should
not silently mutate the source event semantics merely because one view is
collapsed.

## 4. Cross-product synthesis

- **Name the visibility level.** Terms such as `summary`, `verbose`,
  `transcript`, `raw`, `collapsed`, and `hidden` communicate different
  contracts. A single boolean called `show_thinking` can conceal meaningful
  differences unless the product deliberately keeps the scope narrow.
- **Keep the default low-noise.** Show that work is happening and what
  verifiable action completed; make long reasoning and detailed tool traces
  opt-in or collapsed. This improves scanability without claiming that the
  model did not reason.
- **Preserve facts when collapsing.** Collapsing a tool or reasoning block
  should retain a stable summary, outcome, and error state. It should not make
  a completed tool call look as if it never ran.
- **Separate generated explanation from runtime evidence.** Reasoning text can
  explain a decision, but tests, tool results, approvals, and final artifacts
  are the stronger evidence of what happened.
- **Keep human and machine surfaces distinct.** Interactive viewers can expose
  details after a deliberate action; JSON/JSONL should use an explicit event
  schema and avoid leaking provider-specific reasoning fields by accident.

## 5. Pitfalls and evidence gaps

- "Collapsed" can mean hidden UI text, redacted provider content, or an
  aggregated event. Products do not use these terms identically.
- A provider may return a summary, a redacted block, an opaque continuation
  token, or no reasoning content. A client cannot safely normalize all of them
  into one raw-text field.
- Showing an effort value next to a collapsed summary does not prove that the
  provider accepted the effort or that the summary reflects the invested
  reasoning. Effective effort requires a separate capability or acceptance
  signal.
- Transcript expansion can expose prompts, file paths, tool arguments, or
  secrets that were safe in a compact view. Redaction and permissions remain
  necessary even when a user asks for detail.
- Public product documentation rarely specifies exactly what is retained,
  redacted, or replayed after resume, compaction, model switching, or a
  background task. Those lifecycle semantics need separate evidence.

## References

- Anthropic, [Claude Code model configuration][claude-model-config]; accessed
  2026-08-05.
- Anthropic, [Claude Code interactive mode][claude-interactive]; accessed
  2026-08-05.
- OpenAI Codex, [TUI/config reasoning visibility settings][codex-config] at
  commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`; accessed 2026-08-05.
- Aider, [announcement and response rendering][aider-base-coder] and
  [reasoning tag handling][aider-reasoning-tags]; accessed 2026-08-05.

[claude-model-config]: https://code.claude.com/docs/en/model-config.md
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode.md
[codex-config]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/config/src/config_toml.rs
[aider-base-coder]: https://github.com/Aider-AI/aider/blob/main/aider/coders/base_coder.py
[aider-reasoning-tags]: https://github.com/Aider-AI/aider/blob/main/aider/reasoning_tags.py
