# Interactive model switching: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Model catalogs, aliases, picker behavior, and
> resume precedence change quickly; re-check the cited sources before adoption.
>
> Decision surface: how coding agents select a model at startup, change it
> during a session, and preserve model identity across history, effort, and
> resume boundaries.
>
> Scope: startup overrides, in-session selection, capability-aware pickers,
> confirmation/re-read behavior, and resumed-session model precedence. Out of
> scope: this repository's implementation, provider SDK design, model quality
> comparisons, and pricing claims beyond what products display.

## 1. Conclusions

1. **Startup selection and in-session switching are different contracts.** A
   startup override can build one runtime with a chosen model. In-session
   switching must decide what happens to the active turn, cached context,
   conversation history, effort settings, and pending UI state.
2. **A model picker is capability-aware, not just a string prompt.** Current
   Codex picker code marks the current model, exposes model descriptions and
   supported reasoning efforts, and routes advanced choices through an effort
   picker. Claude Code also documents model-dependent availability and
   organization/provider restrictions.
3. **Switching with prior output needs an explicit history policy.** Claude
   Code asks for confirmation because the next response re-reads full history
   without cached context. This makes the latency/cost/context consequence
   visible instead of silently changing the execution environment.
4. **Resume should not accidentally inherit another session's selection.**
   Claude Code documents that resumed sessions keep the model saved with the
   transcript, subject to explicit startup precedence and provider-specific
   exceptions. A current global default is not automatically the identity of
   an existing session.
5. **A bounded first step is startup-only selection.** It is useful and
   compatible with the CLI model, while avoiding the much larger semantics of
   changing a live session. It should still report the selected model and
   preserve the session's own model on resume unless an explicit override
   contract says otherwise.

## 2. Evidence from deployed applications

### 2.1 Claude Code: `/model`, startup flags, and resume precedence

Claude Code documents four model-selection entry points: `/model` during a
session, the `--model` startup flag, `ANTHROPIC_MODEL`, and settings. During a
session, `/model <alias|name>` switches immediately and `/model` opens a
picker. The picker asks for confirmation when prior output exists because the
next response re-reads the full history without cached context. The picker can
save a selection as the default or apply it only to the current session.

The interactive-mode documentation assigns `Option+P`/`Alt+P` to model
switching without clearing the prompt. The model configuration documentation
also states that resumed sessions keep the model saved with the transcript,
with documented precedence for a model explicitly supplied at the new launch
and provider-specific deployment exceptions.

These are documented user-visible behaviors. They do not disclose the full
internal cache invalidation algorithm or guarantee that every provider has the
same model identity semantics.

Evidence: [Claude Code model configuration][claude-model-config] and [Claude
Code interactive mode][claude-interactive] (accessed 2026-08-05).

### 2.2 Codex: catalog-backed picker with effort-aware selection

The current public Codex TUI opens a model popup only after startup is
configured, lists catalog presets, marks the current model, and offers an
"All models" path. The popup uses each preset's description and supported
reasoning-effort options; selecting a model can open a child effort picker.
The source warns when a custom OpenAI base URL may not support model selection
and tells users to use a startup `-m` or config path for legacy models.

This demonstrates that model selection is coupled to capability metadata and
reasoning settings. A client cannot provide a truthful picker by accepting
arbitrary model strings alone.

Evidence: [Codex model popup][codex-model-popup] and [Codex model catalog
type][codex-model-catalog] at commit
`e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a` (accessed 2026-08-05).

### 2.3 Aider: explicit in-chat model command and model search

Aider's current in-chat command reference exposes `/model` to switch the main
model and `/models` to search the list of available models. The command surface
also distinguishes the editor model in architect mode. This is a simpler
interaction contract than the catalog-backed Codex picker, but it confirms that
model switching is a first-class conversation command in another deployed
coding CLI.

Evidence: [Aider in-chat commands][aider-commands] (accessed 2026-08-05).

## 3. Mechanisms and tradeoffs

| Selection mode | Required boundary | Benefit | Risk |
| --- | --- | --- | --- |
| Startup model override | Before runtime/session creation | Simple, isolated, scriptable | Does not help a live session |
| In-session switch before first turn | Replace model before durable output | Low history compatibility cost | Still needs capability/error handling |
| In-session switch after output | Turn boundary plus history/cache policy | Fast model experimentation | Re-read cost, incompatible context, pending-turn races |
| Picker with capability catalog | Model metadata and current selection | Avoids invalid effort/model combinations | Catalog can be stale or provider-specific |
| Free-form model name | Provider request and error path | Works with custom endpoints | Cannot promise availability or effort support |
| Resume with saved model | Durable session metadata or model identity | Reproducible continuation | Retired/provider-specific models need fallback policy |

A safe lifecycle separates these stages:

```text
selection request
  -> resolve/catalog capability
  -> wait for an idle turn boundary
  -> decide history/cache behavior
  -> construct or select the new model
  -> update effort/cost/status metadata
  -> persist or keep session-only scope
```

The absence of a catalog should not be hidden by a fake picker. A free-form
startup override can remain opaque and surface provider errors; an interactive
picker needs stronger availability and capability evidence.

## 4. Cross-product synthesis

- **Expose the current model and scope.** Users need to know whether a choice
  applies to the current session, future sessions, or both.
- **Treat a switch as a state transition.** It should have an explicit idle or
  confirmation boundary, especially after output exists or while tools,
  approvals, compaction, or queued follow-ups are active.
- **Refresh coupled metadata.** Model effort options, default effort, pricing
  labels, context limits, and tool support can all change with the model. The
  UI should not carry the old model's metadata forward silently.
- **Keep resume deterministic.** A resumed transcript should use its recorded
  model identity or an explicitly documented override, not whichever global
  default happens to be current.
- **Start with a smaller contract when discovery is unavailable.** Startup
  selection and clear status reporting are lower-risk than live switching;
  they do not require the application to pretend it has a model catalog.

## 5. Pitfalls and evidence gaps

- A model name can be an alias, a provider-specific deployment ID, a gateway
  route, or an opaque custom endpoint identifier. Equality of strings does not
  imply equality of behavior.
- Re-reading history after a switch may change prompt caching, latency, token
  usage, and provider context limits. Products document parts of this tradeoff
  but not their complete cache implementation.
- The active turn may still be using the old model while a user opens a picker.
  Public docs do not establish one shared cancellation/queue contract for this
  race.
- A model can be listed but unavailable for the current account, region,
  gateway, organization cap, or provider deployment. Picker metadata is not a
  successful request receipt.
- Resume, fork, subagent, and background-task model inheritance are separate
  lifecycle questions. Evidence for one must not be generalized to the others.

## References

- Anthropic, [Claude Code model configuration][claude-model-config]; accessed
  2026-08-05.
- Anthropic, [Claude Code interactive mode][claude-interactive]; accessed
  2026-08-05.
- OpenAI Codex, [model popup][codex-model-popup] and [model catalog][codex-model-catalog]
  at commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`; accessed 2026-08-05.
- Aider, [in-chat commands][aider-commands]; accessed 2026-08-05.

[claude-model-config]: https://code.claude.com/docs/en/model-config.md
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode.md
[codex-model-popup]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/chatwidget/model_popups.rs
[codex-model-catalog]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/model_catalog.rs
[aider-commands]: https://aider.chat/docs/usage/commands.html
