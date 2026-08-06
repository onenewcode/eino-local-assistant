# Model + reasoning-effort persistence: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Re-verify before adopting; model catalogs and CLI
> behavior change quickly.
>
> Decision surface: how deployed coding agents preserve, replace, or resolve a
> selected model and reasoning effort across live switches, new sessions,
> resume, fork-like operations, provider defaults, and capability limits.
>
> Scope: interactive coding-agent CLIs and their documented model-selection,
> session, checkpoint, and provider behavior. "Reasoning effort" includes
> named levels, sliders, thinking-token budgets, and equivalent controls.
>
> Out of scope: API parameter design as an industry in itself, undocumented
> internals, benchmark quality, and any mapping to a particular codebase.

## 1. Conclusions

1. A model and an effort level are increasingly treated as a tuple, but the
   tuple has more than one lifetime. Codex exposes model-scoped supported and
   default efforts and persists the selected pair; Claude Code separates a
   session-only switch from a saved default and restores a saved model on
   resume. The other products expose pieces of this behavior without
   documenting the complete tuple contract. [Documented fact]

2. The most robust precedence pattern is explicit and layered: an interactive
   selection can update the current session, a separate action can update the
   default for future sessions, and an explicit launch override can win over a
   restored value. Claude Code documents all three layers, including the
   distinction between an Anthropic model ID and a provider deployment ID.
   Gemini CLI documents a similar launch/config/default order, but not how a
   resumed session's effort participates in that order. [Cross-product
   synthesis]

3. "Requested" and "effective" must remain separate. Claude Code documents
   requested-versus-applied effort warnings when an organization cap clamps the
   request. Gemini documents fallback routing that can use another model while
   retaining the configured model, and Aider warns or ignores settings that
   model metadata says are unsupported. These are different mechanisms, so a
   successful selection must not be presented as proof that the provider used
   the requested effort. [Cross-product synthesis]

4. Fork semantics are the least consistently documented surface. Codex's public
   thread protocol accepts model overrides on a fork and records fork lineage,
   but does not document an effort override in the fork request. Claude Code,
   Gemini CLI, GitHub Copilot CLI, and Aider document resume/checkpoint or
   rewind features, but do not publish a complete model+effort inheritance
   contract for a fork. [Evidence gap]

5. The practical tradeoff is continuity versus surprise: carrying a tuple into
   a resumed or forked conversation preserves intent, while model retirement,
   provider deployment IDs, organization policy, and capability changes require
   a re-resolution step. The products with the clearest behavior either warn,
   reject, or expose the substituted value; silent carry-over is not a safe
   assumption. [Cross-product synthesis]

## 2. Evidence from deployed applications

### Codex CLI

- The official Codex product source defines a model catalog entry with
  `supported_reasoning_efforts` and `default_reasoning_effort`. The interactive
  picker opens a second reasoning-level picker when a model has multiple
  choices, highlights the current effort for the current model, labels the
  model default, and treats advanced levels as an explicit follow-up choice.
  Selecting a normal choice updates both model and effort and emits a
  persistence action. [Documented fact; official product source]
- The same product source writes `model` and `model_reasoning_effort` together
  when saving the model selection; clearing the effort value represents the
  provider/model default rather than a concrete level. [Documented fact; source]
- The state migration adds both `model` and `reasoning_effort` to a durable
  thread record. The public thread start/resume response exposes the active
  reasoning effort, and thread settings updates can override effort for later
  turns. [Documented fact; source]
- The public thread data includes fork lineage, and the fork request accepts a
  model override. The published fork request does not contain a corresponding
  effort field, so the documented schema does not establish whether a fork
  inherits the parent's effort, resolves the new model's default, or uses some
  other rule. [Documented fact plus evidence gap]
- The model protocol also exposes reroute notifications and provider model
  fallback controls. Those surfaces establish that the model actually used can
  differ from a requested model, but the inspected public material does not
  state a user-facing effort clamp or an effective-effort value. [Evidence gap]

### Claude Code

- `/model` switches immediately. In the interactive picker, `Enter` switches
  and saves the choice as the default for new sessions, while `s` switches only
  for the current session. `/model` also offers left/right effort adjustment
  for models that support it; `/effort` opens a slider or accepts a named level,
  and `auto` resets to the model default. [Documented fact]
- A resumed session started with `--resume`, `--continue`, or `/resume` keeps
  the model recorded in its saved transcript regardless of the current model
  setting. If the model is retired or excluded, Claude Code falls through to
  normal precedence. Provider-specific deployment IDs on Bedrock, Google
  Cloud's Agent Platform, and Microsoft Foundry are explicitly not restored
  from the transcript. A new `--model` or `ANTHROPIC_MODEL` override takes
  precedence over the restored model. [Documented fact]
- Claude Code documents organization effort caps per model. Levels above the
  cap are omitted from the interactive picker; a higher named request runs at
  the cap. Interactive and plain-text runs warn with both requested and
  applied levels, while JSON/stream-JSON and background-agent cases may clamp
  silently. Caps can change when the model changes. [Documented fact]
- The docs describe model aliases, provider-specific resolution, allowlists,
  retirement remapping, and fallback chains. They do not provide an equivalent
  explicit guarantee that a saved effort level is restored with every resumed
  transcript or inherited by a fork-like branch. [Evidence gap]

### Gemini CLI

- `/model` changes the model for all subsequent interactions in the current
  CLI process. The command reference also exposes `/model set <model> [--persist]`,
  making current-session selection and persistent selection distinct controls.
  The documented selection UI includes Auto and Manual choices, and model
  selection does not override the model used by sub-agents. [Documented fact]
- The documented model precedence is: `--model`, `GEMINI_MODEL`,
  `model.name` in `settings.json`, experimental local model routing, then the
  default Auto model. This is a precedence for startup/configuration; the
  session docs do not say where a resumed session's model or reasoning setting
  is inserted. [Documented fact plus evidence gap]
- Gemini's advanced model-configuration documentation separates a requested
  model identifier from the underlying generation configuration. Aliases and
  context-matched overrides can inject `thinkingConfig`, including a
  `thinkingBudget`, and values are passed to the provider with minimal
  validation. [Documented fact]
- Session history is automatically saved with prompts, responses, tool calls,
  token usage, and available thought summaries. `--resume` and `/resume` restore
  conversation context; checkpoint restore also restores conversation history
  and re-proposes the original tool call. The published session and checkpoint
  docs do not state that the model+thinking configuration is stored and
  restored as a tuple. [Documented fact plus evidence gap]
- Model routing can use a fallback for a failed model for the current turn or
  the rest of the session. Some silent utility-call fallback chains do not
  change the configured model. This is a clear configured-versus-effective
  distinction, but no public status contract reports an effective thinking
  budget or a clamp. [Documented fact plus evidence gap]

### GitHub Copilot CLI

- The official Copilot CLI documentation supports `/model` and `--model`; the
  interactive flow selects a model and, for models with extended capabilities,
  separately asks for default or one-million-token context and configurable
  reasoning levels. Higher reasoning consumes more AI credits. [Documented
  fact]
- Copilot CLI supports `--resume`, `/resume`, and `--continue` to reopen a
  saved interactive session with its context. Cloud-backed sessions are also
  described as retaining state between uses and allowing work from another
  machine. The docs do not say whether the selected model, context size, and
  reasoning level are transcript state, user defaults, or recomputed at resume.
  [Documented fact plus evidence gap]
- BYOK configuration uses provider environment variables and `COPILOT_MODEL`;
  compatible models must support tool calling and streaming, otherwise the CLI
  returns an error. The provider documentation does not define a portable
  reasoning-effort vocabulary, a clamp policy, or an effective-effort status.
  [Documented fact plus evidence gap]
- GitHub documents base models as defaults for some enterprise plans and
  separately documents model availability. This establishes that a default can
  change outside a conversation, but not how a saved effort choice should be
  reconciled with that change. [Documented fact plus evidence gap]

### Aider

- Aider supports `--model` at launch and `/model` during chat. It exposes
  `/reasoning-effort` and `/thinking-tokens` as independent in-chat controls,
  while the corresponding launch/configuration values are
  `--reasoning-effort` and `--thinking-tokens`. The docs describe `/save` as
  saving commands that reconstruct the current chat session's files, and
  `--restore-chat-history` as a separate history option. [Documented fact]
- Aider's model metadata declares `accepts_settings`, such as
  `reasoning_effort` or `thinking_tokens`. If a setting is not explicitly
  supported, Aider warns and ignores it; `--no-check-model-accepts-settings`
  can force it, with a stated risk of an API error. This is client-side
  compatibility handling, not proof that a provider applied the requested
  effort. [Documented fact]
- Aider's config files and environment variables provide durable defaults for a
  new invocation, and model aliases have an explicit command-line, config-file,
  then built-in priority. The public docs do not define model+effort inheritance
  across a resumed chat, nor a fork operation with a separate tuple. [Documented
  fact plus evidence gap]

## 3. Mechanisms and tradeoffs

### Separate the lifetimes of a selection

The evidence supports at least four distinct scopes:

| Scope | User intent | Evidence pattern |
| --- | --- | --- |
| Current interaction/session | Change what the next request uses | Claude `/model ...` with `s`; Gemini `/model`; Aider `/model`; Codex non-persist action |
| New-session default | Make future launches start with a choice | Claude `Enter` save; Gemini `--persist`/settings; Aider config; Codex config persistence |
| Resumed transcript | Preserve continuity of an existing conversation | Claude explicitly restores model; Gemini/Copilot restore context but omit tuple semantics; Codex exposes thread settings |
| Fork/branch | Create a related conversation with explicit inheritance or overrides | Codex publishes a model fork override and lineage; the others do not publish a complete tuple contract |

Collapsing these scopes into one mutable value makes a live switch leak into
future sessions or makes a resume unexpectedly inherit a different global
default. The separate-scope design costs more UI and precedence explanation,
but it makes the user's intent observable.

### Resolve effort against the selected model

Codex's catalog and Claude's per-model effort picker make the capability
boundary visible before application. A selected effort can therefore be
represented as either a concrete value or "provider/model default." Gemini's
configuration aliases and Aider's metadata checks show two alternatives:
centralized configuration can inject a budget, while model metadata can reject
or ignore an incompatible setting. Carrying an old concrete value to a new
model is the dangerous case; the inspected products do not establish a single
industry rule for whether to clear it, clamp it, or reject the switch.

### Make substitutions observable

There are at least three different effective-value events:

1. **Capability resolution:** a model does not accept the requested effort
   (Aider warns/ignores; Claude hides or caps levels).
2. **Policy resolution:** an organization or allowlist restricts the request
   (Claude reports requested and applied effort in supported output modes).
3. **Availability routing:** the requested model fails and another model runs
   (Gemini documents fallback; Codex exposes reroute notifications).

The safe common vocabulary is therefore `requested`, `applied/effective`, and
`reason` or `source`. A status line that only echoes the request is not an
effective-value report. Silent fallback can preserve availability but makes
cost, latency, and quality harder to predict.

### Provider defaults are a real selection

"Auto," "default," and an omitted effort are not equivalent to a remembered
concrete level. Claude's `auto` resets to the model default; Codex clears the
effort configuration to select the provider/model default; Gemini's Auto model
selection can route within a family. Treating these as the same value would
prevent a user from intentionally returning to a moving provider default.

## 4. Cross-product synthesis

- **Convergence:** all five products expose a live model switch; all five have
  some form of persistent launch/configuration state; and all five acknowledge
  model-specific capability or availability differences, although the control
  and reporting strength varies.
- **Strongest documented persistence:** Claude Code provides the clearest
  distinction between current-session and future-session model selection and
  gives resume precedence and retirement exceptions. Codex is the clearest
  example of treating model and effort as a durable pair in the product's
  picker/catalog and thread state.
- **Configuration-oriented persistence:** Gemini and Aider emphasize settings,
  aliases, and metadata. Their public docs are useful for default precedence
  and compatibility, but do not establish that an interactive model+effort
  choice travels with a resumed conversation.
- **Session-oriented persistence:** Copilot documents rich context/session
  continuity and model/reasoning selection, but leaves the boundary between
  saved session state and current model policy unspecified.
- **Fork gap:** public documentation consistently explains history restoration
  more often than model-policy inheritance. A fork should be treated as a
  separate decision surface, not inferred from resume behavior.
- **Status gap:** Claude is the only reviewed product with an explicit,
  user-visible requested/applied effort clamp contract. Fallback and metadata
  warnings in Gemini, Codex, and Aider are related signals, not evidence of a
  common effective-effort API.

## 5. Pitfalls and evidence gaps

- Do not infer tuple persistence from "resume restores context." Gemini and
  Copilot explicitly document context restoration but do not state that model
  and effort are transcript fields.
- Do not infer fork inheritance from a rewind or checkpoint. Rewind restores a
  prior history/file point; it is not necessarily a new branch with independent
  model policy.
- Do not call a provider default an applied numeric/name level. Auto/default is
  an intentional unresolved choice that may vary by provider, account, or
  model version.
- Do not treat a catalog's supported list as proof that the provider accepted a
  request. Claude's organization cap and Gemini's routing are examples of
  later constraints; Aider's metadata is a client-side warning layer.
- Do not assume launch precedence and resume precedence are identical. Claude
  explicitly gives launch overrides priority over a restored model and disables
  transcript restoration for some provider deployment IDs; the other products
  do not publish equally complete rules.
- Public materials do not sufficiently disclose whether effort is serialized
  in Gemini, Copilot, or Aider session records; whether any of those products
  has a first-class model+effort fork; or how provider-side effort clamping is
  reported when the CLI cannot inspect the applied value.
- Product versions, plans, organization policy, provider integrations, and
  model catalogs change independently. The cited behavior is time-scoped to
  the access date and should not be generalized to every deployment.

## References

All sources below are official product documentation or official product
engineering material. Accessed 2026-08-05.

### Codex CLI

1. OpenAI Codex product source, model picker and reasoning selection:
   <https://github.com/openai/codex/blob/main/codex-rs/tui/src/chatwidget/model_popups.rs>
2. OpenAI Codex product source, persisted model-selection edits:
   <https://github.com/openai/codex/blob/main/codex-rs/tui/src/config_update.rs>
3. OpenAI Codex product source, model catalog effort capabilities:
   <https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/src/protocol/v2/model.rs>
4. OpenAI Codex product source, durable thread effort migration:
   <https://github.com/openai/codex/blob/main/codex-rs/state/migrations/0020_threads_model_reasoning_effort.sql>
5. OpenAI Codex product source, thread start/resume/settings and fork protocol:
   <https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/src/protocol/v2/thread.rs>
   and <https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadForkParams.json>

### Claude Code

6. Model configuration, precedence, resume behavior, provider resolution, and
   organization effort limits:
   <https://code.claude.com/docs/en/model-config>
7. Interactive controls, `/model`, `/effort`, and session commands:
   <https://code.claude.com/docs/en/commands>
8. Interactive-mode shortcuts including model switching and thinking controls:
   <https://code.claude.com/docs/en/interactive-mode>

### Gemini CLI

9. Model selection and `/model` behavior:
   <https://geminicli.com/docs/cli/model/>
10. Model routing and startup precedence:
    <https://geminicli.com/docs/cli/model-routing/>
11. Advanced model configuration, aliases, overrides, and thinking budget:
    <https://geminicli.com/docs/cli/generation-settings/>
12. Session saving and resume:
    <https://geminicli.com/docs/cli/session-management/>
13. Checkpoint restore:
    <https://geminicli.com/docs/cli/checkpointing/>
14. Slash-command reference, including persistent model selection:
    <https://geminicli.com/docs/reference/commands/>

### GitHub Copilot CLI

15. Copilot CLI model usage, reasoning levels, fallback/provider requirements:
    <https://docs.github.com/en/copilot/concepts/agents/copilot-cli/about-copilot-cli>
16. Copilot CLI interactive use, resume, and `--continue`:
    <https://docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview>
17. Copilot CLI BYOK provider and model requirements:
    <https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models>
18. Copilot CLI context and session checkpoints:
    <https://docs.github.com/en/copilot/concepts/agents/copilot-cli/context-management>
19. GitHub Copilot base/default and LTS model policy:
    <https://docs.github.com/en/copilot/concepts/models/fallback-and-lts-models>

### Aider

20. Aider usage and live model switching:
    <https://aider.chat/docs/usage.html>
21. Aider command reference, model and reasoning commands:
    <https://aider.chat/docs/usage/commands.html>
22. Aider options reference, configuration and reasoning settings:
    <https://aider.chat/docs/config/options.html>
23. Aider reasoning model compatibility and unsupported-setting warnings:
    <https://aider.chat/docs/config/reasoning.html>
24. Aider model aliases and precedence:
    <https://aider.chat/docs/config/model-aliases.html>
25. Aider YAML configuration and chat-history options:
    <https://aider.chat/docs/config/aider_conf.html>
