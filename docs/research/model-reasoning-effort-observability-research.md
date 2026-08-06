# Model reasoning effort observability: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Model catalogs, effort levels, and CLI status
> behavior change quickly; re-check the cited revisions before adoption.
>
> Decision surface: how deployed coding agents distinguish a user's requested
> reasoning effort, the model/provider's effective effort, supported options,
> and the visibility of reasoning summaries.
>
> Scope: status displays, capability/default discovery, accepted or clamped
> settings, and the separation between reasoning controls and visible process
> output. Out of scope: this repository's implementation, raw chain-of-thought
> disclosure, provider SDK design, and training-time test-time compute.

## 1. Conclusions

1. **Configured is not necessarily effective.** A CLI can echo the value the
   user selected, but only a provider/model response, capability contract, or
   an explicitly documented clamp can establish what will actually run. A
   status view should label a value as requested/configured unless that
   stronger evidence exists.
2. **Capability and default information are model-specific.** Codex exposes
   extensible effort values plus per-model option metadata. Claude Code's
   documented effort levels and defaults vary by model, and organization caps
   can change what is offered or applied. A universal `low|medium|high` list
   is not a reliable effective-value catalog.
3. **Mature clients surface the active choice close to the model identity.**
   Codex includes reasoning and summary settings in `/status`; Claude Code
   shows the current effort in the session header and briefly in the footer;
   Aider includes selected reasoning settings in its startup announcement.
   This is configuration observability, not proof that an upstream endpoint
   accepted the request.
4. **Reasoning effort and reasoning visibility remain separate.** Codex status
   distinguishes effort from summaries, and Claude Code keeps effort controls
   separate from collapsed or expanded thinking output. A user can therefore
   see a configured effort while summaries are hidden, or inspect summaries
   without treating them as a measurement of internal effort.
5. **The safest fallback is explicit uncertainty.** When a client has no
   capability discovery or acceptance signal, it can show the requested value
   and state that the provider default/effective value is unknown. Calling an
   empty setting `medium`, or calling a requested value `effective`, creates a
   false promise about latency, cost, or quality.

## 2. Evidence from deployed applications

### 2.1 Codex: status snapshots combine active effort and summary mode

The public Codex protocol represents `ReasoningEffort` as an extensible string
and defines a separate `ReasoningEffortOption` containing a value and
description. This supports a model-specific capability list rather than a
closed client-wide enum.

Codex's current TUI status snapshots make the distinction visible. A configured
session is rendered as `reasoning high, summaries detailed`; an empty effort
configuration is rendered as `reasoning medium, summaries auto`. The snapshot
is a user-facing status representation of the selected/default mode, not a
claim that the provider has returned a per-request acceptance receipt.

The configuration contract also separates hiding summary-style `AgentReasoning`
events from showing raw reasoning content. The two controls are independent of
the effort setting.

Evidence: [ReasoningEffort][codex-effort],
[ReasoningEffortOption][codex-effort-option],
[status snapshot with configured reasoning][codex-status-configured],
[status snapshot with default reasoning][codex-status-default], and
[Codex reasoning visibility configuration][codex-config] at commit
`e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a` (accessed 2026-08-05).

### 2.2 Claude Code: model capabilities, clamps, and active-session echo

Claude Code documents effort levels as model-dependent. Its model
configuration documentation lists different supported sets, states that the
same level name is calibrated per model, and documents a fallback to the
highest supported level at or below a requested level. Organization effort
limits can further cap the available or applied level; interactive sessions
warn about the requested and applied values, while some machine-readable or
background modes apply the clamp silently.

The same documentation says the current effort appears in the session header
next to the model name and briefly in the footer when it starts or changes.
This is stronger than merely printing a config file because it is attached to
the active model/session, but the documented clamp behavior still means a
request and an applied level must remain conceptually distinct.

Claude Code also documents that thinking output is collapsed by default and
can be expanded with `Ctrl+O`; its interactive-mode documentation describes a
transcript viewer that exposes detailed tool usage and execution separately.
Effort visibility and transcript visibility therefore answer different user
questions.

Evidence: [Claude Code model configuration, effort levels, and active-session
display][claude-model-config] and [Claude Code interactive mode and transcript
viewer][claude-interactive] (accessed 2026-08-05).

### 2.3 Aider: selected-setting announcement plus acceptance checking

Aider exposes `--reasoning-effort` and `--thinking-tokens` as separate options;
the documented default for each is not set. It also exposes
`--check-model-accepts-settings`, enabled by default, for settings such as
these. This keeps provider/model acceptance as a separate concern from the
user's selection.

At startup, Aider's announcement appends the selected thinking-token budget and
reasoning effort to the model line when present. Its response handling formats
reasoning content separately and removes it before the remaining response is
processed as the normal answer. The announcement is useful configuration
observability, while the acceptance check is the relevant boundary for claims
about support.

Evidence: [Aider CLI argument definitions][aider-args], [Aider configuration
options][aider-options], and [Aider announcement and reasoning rendering
code][aider-base-coder] (accessed 2026-08-05).

## 3. Mechanisms and tradeoffs

| State or signal | What it proves | Safe presentation | Unsafe presentation |
| --- | --- | --- | --- |
| User/config value | What the user or persisted settings requested | `requested: high` or `configured: high` | `effective: high` |
| Model capability list | Values a model/catalog says it supports | `available: low, medium, high` | A universal list for every model |
| Provider acceptance or documented clamp | What the active request will use | `effective: high` or `applied: high (requested: xhigh)` | Assuming the request was accepted because it was sent |
| Omitted effort | The provider/model chooses its default | `provider default` or `default (not reported)` | Guessing `medium` or another common default |
| Reasoning summary policy | What process information the UI exposes | `summaries: auto` / `collapsed` | Treating visibility as effort |
| Raw or detailed transcript | More process/tool detail is available | `transcript: available` | Treating generated explanation as a verified measurement |

The states form a useful product-neutral sequence:

```text
model selection
  -> capability/default information
  -> requested setting
  -> provider acceptance or documented clamp
  -> effective setting
  -> independent summary/transcript visibility
```

The sequence is intentionally not a guarantee that every application can fill
every field. A client without capability discovery may only know the model,
requested value, and visibility policy. It should preserve the unknown rather
than manufacture an effective value.

## 4. Cross-product synthesis

- **Echo configuration near model identity.** A header, footer, startup line,
  or `/status` entry gives users a quick answer to "what did I select?" and
  makes persisted settings discoverable.
- **Use capability-aware wording.** When the application has model options or a
  documented fallback, show the available/current distinction. When it does
  not, use requested/configured wording and reserve effective/applied for an
  acceptance signal.
- **Show the default as a mode, not a guessed level.** Provider defaults can
  vary by model and can change when a model is switched. "Provider default" is
  more honest than a globally hard-coded `medium`.
- **Keep visibility beside, not inside, effort.** Summary mode, collapsed
  thinking, raw-content settings, and transcript access should be independently
  inspectable. A hidden summary does not mean disabled reasoning.
- **Make clamping observable where possible.** If the runtime can detect a
  requested/applied mismatch, report both values and the reason. If it cannot,
  avoid implying that the request was accepted.

## 5. Pitfalls and evidence gaps

- A model catalog can be stale, incomplete, or different from the capability
  of a proxy, gateway, custom deployment, or organization role.
- A status line may represent the client-selected mode rather than an
  endpoint-confirmed effective value. The distinction must come from the
  product contract, not from the visual location of the label.
- Documented fallback behavior is product-specific. One client may clamp to a
  lower supported level, another may reject the request, and another may leave
  the provider to choose; there is no general downgrade rule.
- Reasoning summaries and detailed transcripts are generated explanations or
  event views, not complete or necessarily faithful private chain-of-thought.
  Tool results, tests, and other runtime evidence remain the stronger basis for
  claims about work performed.
- Public documentation does not establish a shared cross-product contract for
  whether a model switch, resumed session, subagent, or background task
  inherits the configured effort. Those lifecycle transitions need separate
  evidence.

## References

- OpenAI Codex, [ReasoningEffort][codex-effort],
  [ReasoningEffortOption][codex-effort-option], and
  [reasoning visibility configuration][codex-config] at commit
  `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`; accessed 2026-08-05.
- OpenAI Codex TUI, [`/status` snapshot with configured reasoning][codex-status-configured]
  and [default reasoning][codex-status-default] at commit
  `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`; accessed 2026-08-05.
- Anthropic, [Claude Code model configuration][claude-model-config]; accessed
  2026-08-05.
- Anthropic, [Claude Code interactive mode][claude-interactive]; accessed
  2026-08-05.
- Aider, [CLI argument definitions][aider-args], [configuration options][aider-options],
  and [announcement/reasoning rendering][aider-base-coder]; accessed
  2026-08-05.

[codex-effort]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/ReasoningEffort.ts
[codex-effort-option]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/v2/ReasoningEffortOption.ts
[codex-status-configured]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/status/snapshots/codex_tui__status__tests__status_snapshot_includes_reasoning_details.snap
[codex-status-default]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/status/snapshots/codex_tui__status__tests__status_snapshot_uses_default_reasoning_when_config_empty.snap
[codex-config]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/config/src/config_toml.rs
[claude-model-config]: https://code.claude.com/docs/en/model-config.md
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode.md
[aider-args]: https://github.com/Aider-AI/aider/blob/main/aider/args.py
[aider-options]: https://aider.chat/docs/config/options.html
[aider-base-coder]: https://github.com/Aider-AI/aider/blob/main/aider/coders/base_coder.py
