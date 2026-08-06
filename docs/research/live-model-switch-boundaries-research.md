# Live model switching after history: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Model catalogs, aliases, picker behavior, and
> resume precedence change quickly; re-verify the cited sources before adoption.
>
> Decision surface: how a deployed coding agent handles changing the main model
> after a transcript already exists, including the active-turn boundary,
> history/cache confirmation, failed selection, reasoning effort, and resume
> identity.
>
> Scope: user-visible behavior and first-party product material from Claude
> Code, OpenAI Codex CLI, Aider, and Gemini CLI.
>
> Out of scope: this repository's implementation, provider SDK contracts,
> model-quality comparisons, pricing comparisons, and undisclosed internal
> cache or routing algorithms.

## 1. Conclusions

1. **[Cross-product synthesis] A model switch is normally a boundary for a
   subsequent model interaction, not a way to retarget an already-issued
   request.** Claude Code says that a post-history switch causes the next
   response to reread history; Gemini CLI says model changes apply to all
   subsequent interactions. Codex admits its model command while a task is in
   progress, but its public source does not say whether an already-running
   request can change model. Aider documents the command but does not document
   its active-turn timing.
2. **[Documented fact] Claude Code is the clearest example of an explicit
   history/cache consent boundary.** When prior output exists, its model picker
   asks for confirmation because the next response rereads the full history
   without cached context. The other cited products expose model selection, and
   some expose prompt caching, but their public materials do not document an
   equivalent switch-specific confirmation.
3. **[Cross-product synthesis] Model selection and reasoning effort form a
   capability tuple.** Claude Code lists effort levels per model and clamps
   unsupported values. Codex routes model choices through supported-effort
   metadata and an effort picker. Aider checks model metadata before applying
   reasoning settings. A model switch cannot safely be treated as changing only
   an opaque model-name string.
4. **[Documented fact] Resume identity is deterministic in the clearest
   Claude Code and Codex paths, but not uniformly documented across products.**
   Claude Code restores the transcript's model unless an explicit new-launch
   override or a documented provider/retirement exception applies. Codex's
   public server code merges persisted model, provider, and reasoning effort
   into resume configuration unless the resume request supplies an override.
   Gemini CLI restores the conversation and Aider can restore chat history, but
   the cited user documentation does not state that either restores the model
   identity or how an explicit override wins.
5. **[Cross-product synthesis] Failure handling is less consistently specified
   than selection.** Claude Code has a preflight validation path that rejects an
   unrecognized model while retaining the current model. Codex's cited UI
   path applies the in-memory/thread selection before attempting to persist a
   global default and reports a persistence error without showing a
   compensating reset. Aider documents warnings for incompatible reasoning
   settings, while Gemini CLI's cited docs do not specify rollback.
6. **[Evidence gap] No cited product publishes a complete contract for cache
   invalidation, in-flight model-switch races, or rollback across all layers.**
   A visible picker result, a model setting stored in a session, a successful
   provider request, and a new global default are different outcomes. Public
   material often describes only one of them.

## 2. Evidence from deployed applications

### 2.1 Claude Code

**Documented facts**

- Claude Code supports an in-session /model command and a picker. Its model
  configuration documentation says the switch is immediate, but the picker
  asks for confirmation when the conversation has prior output because the
  next response rereads the full history without cached context. The
  interactive-mode documentation also assigns Option+P or Alt+P to model
  switching without clearing the prompt.
- The picker distinguishes persistence scope: Enter changes the model and saves
  it as the default, while s changes it for the current session only. A direct
  /model name behaves like Enter. Startup --model and ANTHROPIC_MODEL apply only
  to the launched session.
- On the Anthropic API, an unrecognized model string is rejected and the
  session keeps its current model instead of saving the invalid value. That
  preflight does not cover custom provider deployment names, gateways, or all
  startup settings; in those cases a bad value can survive until the first
  request fails.
- Resumed sessions normally keep the model saved with the transcript,
  regardless of the current model setting. A retired or disallowed model falls
  through to normal precedence. An explicit --model or ANTHROPIC_MODEL on the
  new launch takes precedence. Provider-specific deployment IDs for Bedrock,
  Google Cloud's Agent Platform, and Microsoft Foundry are documented
  exceptions where the transcript model is not restored.
- Effort levels depend on the active model. Unsupported levels fall back to the
  highest supported level at or below the requested value. Model defaults and
  organization effort limits can change the available or applied level, and
  some model families hold or reset their default effort until the user makes
  an explicit effort choice.

**Boundary and gap**

The documentation describes an immediate switch and a confirmation condition
based on prior output, but does not specify whether /model is disabled, queued,
or applied to the next boundary while a tool call or model response is
actively running. It also does not publish the complete cache invalidation or
rebuild algorithm behind the stated reread.

**Evidence:** Claude Code model configuration and interactive-mode
documentation, accessed 2026-08-05. [claude-model-config] [claude-interactive]

### 2.2 OpenAI Codex CLI

The Codex evidence below is a first-party public source snapshot at commit
e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a (commit date 2026-08-04). It is
implementation evidence for the deployed CLI surface, not a claim that the
source exposes every runtime detail.

**Documented facts in the public source**

- The slash-command availability table classifies Model as available during a
  task in progress. This is a different admission policy from an idle-only
  picker. The model popup itself is disabled until startup is complete; if the
  catalog cannot be listed, it tells the user to try /model again later.
- The picker marks the current model, shows model descriptions, and carries
  supported reasoning-effort metadata. Selecting a model can open a separate
  reasoning-effort picker. The selection action emits model and effort updates
  together with a request to persist the model selection.
- The app event handler updates the current model and synchronizes the active
  thread's model setting. It separately handles persistence of the global
  model/effort configuration. If that config write fails, the handler reports
  the error; the cited handler has no compensating reset of the already-updated
  current selection.
- Resume processing merges persisted model, provider, and reasoning-effort
  metadata into the resumed configuration when no model-related resume
  override is supplied. A request-level override prevents that merge.
- The picker warns when a custom OpenAI base URL may not support model
  selection. This makes catalog availability and actual endpoint acceptance
  distinct concerns.

**Boundary and gap**

The cited model command is admitted during an active task, but the source does
not state whether the request already in flight remains on its original model,
whether a later tool-loop call uses the new setting, or whether any work is
cancelled. The picker path contains no history-re-read confirmation branch,
and the cited sources do not disclose whether a switch preserves, rebuilds, or
invalidates provider prompt-cache state. The persistence error path is visible,
but the public source does not establish a product-wide rollback contract for
provider errors, endpoint errors, or concurrent selection events.

**Evidence:** Codex first-party source at the cited commit:
[codex-model-popup] [codex-slash-command] [codex-event-dispatch]
[codex-thread-settings] [codex-resume-merge]. Accessed 2026-08-05.

### 2.3 Aider

**Documented facts**

- Aider's in-chat command reference exposes /model to switch the main model and
  /models to search available models. It separately exposes /editor-model and
  /weak-model, so "the model" is not one universal role in every mode.
- The command reference exposes /reasoning-effort, with values described as a
  number or low/medium/high depending on the model. The reasoning-model
  documentation also exposes /reasoning-effort and /thinking-tokens inside a
  chat.
- Aider uses per-model metadata such as accepts_settings to decide whether a
  reasoning setting is supported. It warns and ignores a setting that the
  model does not explicitly accept. A no-check option can force the setting,
  with the documented risk of an API error.
- Aider documents prompt caching for selected providers and says it organizes
  the system prompt, read-only files, repository map, and editable files to
  try to cache them. Its options reference separately documents a chat-history
  file and --restore-chat-history, whose default is false.

**Boundary and gap**

The cited command and configuration documentation does not specify whether a
model switch is accepted during an active response, whether it waits for an
idle turn, or whether existing history is reread or confirmed. The caching
documentation explains what Aider tries to cache, but not what happens to that
cache when /model changes the provider or model. Chat-history restoration is
documented, but the cited material does not state that the saved model
identity, reasoning effort, or an explicit launch override is restored with
the messages. No transactional rollback contract is documented for a failed
model request or a failed model switch.

**Evidence:** Aider in-chat commands, reasoning-model, prompt-caching, and
options documentation, accessed 2026-08-05. [aider-commands]
[aider-reasoning] [aider-caching] [aider-options]

### 2.4 Gemini CLI

**Documented facts**

- Gemini CLI's /model command opens a dialog with Auto and Manual choices. It
  supports aliases or concrete model names, and the documentation says changes
  apply to all subsequent interactions. The startup --model flag is a separate
  entry point.
- The model-selection documentation explicitly says that /model and --model do
  not override the model used by sub-agents. Model selection therefore has a
  documented scope boundary even when the main session changes.
- Gemini CLI automatically saves complete conversation history, including
  prompts, model responses, tool executions, token usage (including cached
  usage when available), and thoughts or reasoning summaries when available.
  The --resume flag and /resume browser restore a previous session's context.
- The cited model-selection page describes Pro, Flash, and Auto behavior and
  describes Pro as providing the highest levels of reasoning and creativity,
  but it does not present a user-selectable reasoning-effort contract tied to
  each model.

**Boundary and gap**

The "subsequent interactions" wording scopes the new setting away from the
interaction already sent, but the cited documentation does not say whether a
user can open /model while a task is active or how that request is serialized.
It does not document a prior-history confirmation, cache invalidation rule, or
model-selection rollback. Session resume restores conversation context, but
the cited session documentation does not state whether the original model
identity or model-specific effort is restored, overridden, or resolved again
from current settings.

**Evidence:** Gemini CLI model-selection, session-management, and command
reference documentation, accessed 2026-08-05. [gemini-model] [gemini-session]
[gemini-commands]

## 3. Mechanisms and tradeoffs

### 3.1 Active-turn and idle boundaries

**Documented facts**

- Claude Code calls the in-session switch immediate but does not publish a
  busy-state rule in the cited pages.
- Codex explicitly leaves /model available during a task.
- Gemini CLI says the change applies to subsequent interactions.
- Aider documents the command but not its active-turn rule.

**Cross-product synthesis**

The products expose at least three different boundaries: command admission,
the next model call, and the next user turn. These must not be conflated. A
selection command accepted during a running task does not, by itself, prove
that the active provider request changed. For a coding agent, tool execution,
approval prompts, queued follow-ups, and compaction can all sit between the
user's selection and the next model call. Public product material is strongest
when it tells users which future interaction is affected; it is weakest on the
race between selection and an already-running loop.

**Tradeoff**

Allowing selection during work reduces friction and can let users prepare the
next model. An idle-only gate gives a simpler consistency boundary. A
confirmation or explicit "applies next turn" message is useful when the
product permits a selection before the current task has settled.

### 3.2 History versus provider prompt cache

**Documented facts**

- Claude Code explicitly distinguishes rereading full history from retaining
  cached context and asks for confirmation when the former follows a switch.
- Aider documents provider prompt caching and the stable prompt/file regions it
  tries to cache, but not its model-switch transition.
- Gemini CLI records token-usage fields that include cached usage when
  available, but does not connect that field to /model behavior.
- The cited Codex picker sources show no history confirmation branch and no
  cache contract.

**Cross-product synthesis**

Visible transcript history, serialized session history, provider-side cached
prefixes, and the next request's assembled context are different state
objects. Reusing the same transcript does not guarantee reuse of a cache, and
losing a cache does not necessarily mean losing conversation history. A
switch contract should therefore state both what context is sent and what
cache or latency consequence is user-visible; the cited products do not all
make both statements.

**Evidence gap**

No cited product discloses the complete cache key, invalidation timing, or
whether a cache miss is charged or displayed specifically because of a model
switch. Claude's confirmation is strong user-facing evidence of a known
consequence, not a complete cache algorithm.

### 3.3 Failure and rollback boundaries

**Documented facts**

- Claude Code's recognized-model validation keeps the current model when the
  requested string is rejected. Its provider-specific pass-through cases move
  failure to the first request instead.
- Codex's cited event path updates current/thread settings before the global
  config write and reports a persistence failure without an evident reset in
  that handler.
- Aider checks whether a model accepts a reasoning setting, warns and ignores
  unsupported settings by default, and offers a force option that may lead to
  an API error.
- Gemini CLI's cited documentation does not define model-switch rollback.

**Cross-product synthesis**

There are at least three failure points: selection validation, runtime
construction or endpoint acceptance, and persistence of a default or session
setting. "The switch failed" is ambiguous unless the product says which state
was committed. Claude's validation path provides a clean pre-commit behavior;
Codex's source illustrates why UI state, session state, and global-default
state need separate success/error reporting. The public evidence does not
establish a common industry transaction or rollback standard.

### 3.4 Reasoning effort and related model capabilities

**Documented facts**

- Claude Code treats effort levels as model-dependent, applies supported-level
  fallback, and exposes organization caps per model.
- Codex's catalog includes supported reasoning efforts, marks the current
  model/effort, and can route selection through a dedicated effort picker.
- Aider keeps model capability metadata and warns when a requested reasoning
  setting is not accepted.
- Gemini CLI's cited model page describes model tiers and reasoning quality,
  not a selectable effort setting.

**Cross-product synthesis**

Reasoning effort is part of the selected model's capability and policy
context, not merely a global UI preference. A switch can change the legal
values, default, cap, cost/latency expectation, or even whether the setting
exists. The clearest products either reselect effort with the model or validate
the old effort against the new model. The cited sources do not prove that
every product persists the effective, rather than requested, effort value.

### 3.5 Resume identity

**Documented facts**

- Claude Code restores the transcript model in normal cases, then lets an
  explicit new-launch model override it; retirement, allowlists, and
  provider-specific deployment IDs are exceptions.
- Codex resume code merges persisted model/provider/effort metadata unless a
  model-related request override is present.
- Gemini CLI restores saved conversation context by session ID or latest
  session, but the cited docs do not specify the restored model identity.
- Aider can restore chat history, but the cited docs do not specify model
  identity or precedence on restoration.

**Cross-product synthesis**

Resume has two separate decisions: which transcript or checkpoint is opened,
and which model is used for the next request. Products with an explicit
session identity contract treat the saved model as session metadata and make a
new-launch override an explicit precedence rule. Alias resolution, retired
models, provider deployment IDs, and sub-agent models need separate policy;
one product's main-session rule should not be generalized to all descendants.

## 4. Cross-product synthesis

The evidence supports these patterns, with the stated applicability limits:

1. **Future-boundary semantics are more common than retroactive switching.**
   Claude Code and Gemini CLI explicitly describe effects on a future response
   or subsequent interaction. Codex's active-task admission shows that the
   command can be accepted earlier, but does not establish retroactive
   retargeting. Aider's timing remains undisclosed.
2. **History confirmation is a deliberate UX choice, not a universal norm.**
   Claude Code makes a cache-related reread cost explicit. Codex, Aider, and
   Gemini CLI do not provide equivalent switch-specific confirmation in the
   cited evidence. Absence of a documented prompt is not proof that a product
   never prompts in another surface or version.
3. **Capability-aware selection is a recurring control point.** Claude Code,
   Codex, and Aider all attach model-specific metadata to effort or related
   request settings. Free-form model names remain useful for custom endpoints,
   but they weaken preflight validation and picker guarantees.
4. **Resume identity is more mature than live-switch rollback.** Claude Code
   and Codex publish or implement a saved-model-plus-explicit-override rule.
   Gemini CLI and Aider document history restoration without an equally clear
   model identity rule. No product in this evidence set documents a universal
   rollback that spans active runtime, transcript, session metadata, and global
   defaults.
5. **"Current model" has multiple scopes.** It may mean the next main-session
   request, the current task, the persisted default, the resumed transcript's
   model, or a sub-agent model. Gemini CLI explicitly excludes sub-agents from
   its main model setting; Aider exposes separate main, editor, and weak
   models; Claude Code has separate session/default scopes.

## 5. Pitfalls and evidence gaps

- **Alias versus deployment identity:** an alias can resolve differently by
  provider, organization, account, region, or date. String equality is not
  behavioral equality.
- **History versus cache:** a full transcript can be available while a
  provider prefix cache is cold, or a cache can be reused while the visible
  transcript is unchanged. Do not infer one from the other.
- **Active work:** a picker can update the next-turn setting while a tool,
  approval, sub-agent, compaction, or provider request still uses prior state.
  Public docs rarely define the ordering or cancellation race.
- **Validation versus availability:** catalog membership, local capability
  metadata, endpoint acceptance, account entitlement, and a successful
  completion are different checks.
- **Requested versus effective effort:** a requested level may be clamped,
  ignored, reset to a model default, or unavailable under organization policy.
  A status display should not be treated as proof that the provider applied it.
- **Persistence failure:** a switch can be session-only, active-thread
  metadata, or a global default. A failure in one layer does not imply that
  the other layers rolled back.
- **Resume and descendants:** main-session resume, fork/checkpoint restore,
  background tasks, and sub-agents can have different model inheritance rules.
- **Version drift:** this note reflects the cited sources as accessed on
  2026-08-05; product docs and public source snapshots are moving targets.

## References

- Anthropic, [Claude Code model configuration][claude-model-config];
  accessed 2026-08-05. Page update date was not exposed in the fetched
  document.
- Anthropic, [Claude Code interactive mode][claude-interactive];
  accessed 2026-08-05. Page update date was not exposed in the fetched
  document.
- OpenAI Codex, [model popup source][codex-model-popup] at commit
  e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a (2026-08-04);
  [slash-command availability][codex-slash-command];
  [model update event handling][codex-event-dispatch];
  [active thread settings][codex-thread-settings]; and
  [resume metadata merge][codex-resume-merge]. All accessed 2026-08-05.
- Aider, [in-chat commands][aider-commands];
  [reasoning models][aider-reasoning];
  [prompt caching][aider-caching]; and
  [options reference][aider-options]; all accessed 2026-08-05. Page update
  dates were not exposed in the fetched documents.
- Google, [Gemini CLI model selection][gemini-model];
  [session management][gemini-session]; and
  [CLI command reference][gemini-commands]; all accessed 2026-08-05.
  The fetched session-management page reported a last-updated date of
  2026-06-18.

[claude-model-config]: https://code.claude.com/docs/en/model-config.md
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode.md
[codex-model-popup]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/chatwidget/model_popups.rs
[codex-slash-command]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/slash_command.rs
[codex-event-dispatch]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/app/event_dispatch.rs
[codex-thread-settings]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/app/thread_settings.rs
[codex-resume-merge]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server/src/request_processors/thread_processor.rs
[aider-commands]: https://aider.chat/docs/usage/commands.html
[aider-reasoning]: https://aider.chat/docs/config/reasoning.html
[aider-caching]: https://aider.chat/docs/usage/caching.html
[aider-options]: https://aider.chat/docs/config/options.html
[gemini-model]: https://geminicli.com/docs/cli/model/
[gemini-session]: https://geminicli.com/docs/cli/session-management/
[gemini-commands]: https://geminicli.com/docs/reference/commands/
