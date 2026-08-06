# Model catalog and capability discovery: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Catalog contents, aliases, provider support, and
> model availability change quickly; re-check the cited sources before adoption.
>
> Decision surface: how deployed coding agents discover selectable models,
> represent model identity and capabilities, operate with custom endpoints, and
> handle unavailable models across a main session and its subagents.
>
> Scope: catalog provenance and refresh, aliases versus deployment IDs,
> reasoning/tool/context metadata, no-catalog fallback, retired or unavailable
> models, picker failure UX, and main-session/subagent scope.
>
> Out of scope: model quality or price comparisons, provider API design as a
> standalone subject, undisclosed routing internals, and this repository's
> implementation or migration plan.

## 1. Conclusions

1. **[Cross-product synthesis] A useful catalog is a product-owned availability
   view, not merely a list of strings.** Codex refreshes a model manager online,
   GitHub publishes availability by plan and client, and Cline's hosted provider
   exposes a curated set of supported models. Gemini's manual option is explicitly
   limited to models available to that installation. The catalog therefore carries
   entitlement, surface, provider, and lifecycle information in addition to a
   model name.

2. **[Cross-product synthesis] Display names, aliases, canonical model names, and
   deployment identities are separate identifiers in mature products.** Codex's
   public model object has separate `id`, `model`, and `display_name` fields.
   Aider aliases map a short user name to a provider-qualified model name, while
   Azure and other custom endpoints use deployment-specific identifiers. Treating
   the text shown in a picker as the durable provider identity makes resume,
   retirement, and custom-endpoint behavior ambiguous.

3. **[Cross-product synthesis] Capability metadata is multidimensional and
   surface-specific.** Reasoning levels, context limits, image or multimodal
   input, tool calling, streaming, service tiers, and client availability are
   exposed by different products. GitHub explicitly publishes extended context
   and configurable reasoning by client; Codex exposes reasoning, modality, and
   provider capability fields; Cline asks custom-endpoint users to supply context,
   image, computer-use, and pricing settings. A model catalog that only records
   `supports_reasoning` cannot describe the actual picker constraints.

4. **[Documented fact] No-catalog endpoints fall back to explicit configuration
   and
   late validation, not a fabricated catalog.** Aider accepts an OpenAI-compatible
   endpoint and warns when the model is unknown, Cline asks for a base URL and
   model ID plus manual configuration, and Copilot CLI requires a model identifier
   and rejects endpoints whose model lacks tool calling or streaming. These paths
   preserve flexibility but move correctness and capability discovery onto the
   user or the first request.

5. **[Cross-product synthesis] Unavailable-model UX has at least three distinct
   states: catalog omission, selectable-but-unhealthy fallback, and explicit
   identifier failure.** GitHub records retirement dates and suggested
   alternatives; Gemini can ask permission to route a failed model to an available
   fallback; Aider suggests similar names for an unfamiliar identifier; Codex
   tells the user that its models are being updated and to retry. These are not
   interchangeable: a picker should distinguish "not offered here" from "offered
   but temporarily unhealthy" and from "the configured endpoint rejected it."

6. **[Documented fact] Main-session model selection does not consistently control
   subagents.** Gemini explicitly says `/model` and `--model` do not override
   subagents and provides per-agent model overrides. Cline documents separate
   per-phase configuration directories for multi-model workflows. GitHub exposes
   model selection for cloud-agent task entry points, while its model availability
   is also client-specific. A visible main-session selection is therefore not
   evidence that every delegated execution uses the same model identity or
   capabilities.

7. **[Evidence gap] Public product material rarely specifies catalog consistency
   and identity semantics end to end.** The sources do not consistently state
   whether a refresh is atomic, how an alias is canonicalized in durable history,
   when a capability change invalidates an existing picker, or whether a fallback
   model is recorded as the model of the turn. A catalog can be well documented
   at
   the UI boundary while its cache, persistence, and in-flight request semantics
   remain unknown.

## 2. Evidence from deployed applications

### 2.1 OpenAI Codex CLI: refreshed catalog with capability-rich model records

#### Codex: documented facts

- **[Documented fact]** In the public Codex source snapshot examined at commit
  `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`, the app-server obtains supported
  models through a models manager with `OnlineIfUncached` refresh behavior and
  filters hidden models before returning them to clients. A background worker
  performs an online refresh every three minutes. This is a product-source
  observation, not a claim about every Codex deployment or future release.
- **[Documented fact]** The model record separates an opaque `id`, a `model`
  value, a display name, a description, hidden/default flags, upgrade information,
  supported reasoning efforts, input modalities, service tiers, and other model
  metadata. The app-server protocol also supports pagination and an option to
  include hidden models.
- **[Documented fact]** The TUI picker filters hidden presets, marks the current
  model, offers an "All models" path, and routes presets with advanced reasoning
  options through a reasoning picker. If the catalog cannot be listed, it tells
  the user that models are being updated and asks them to try `/model` again.
- **[Documented fact]** The picker warns when an OpenAI-compatible base URL is
  overridden because selecting catalog models may not be supported or may not
  work properly. The provider registry also permits user-defined providers with
  a
  base URL and wire protocol, while provider-level capabilities such as namespace
  tools, image generation, and web search are exposed separately.

#### Codex: boundary and evidence gap

- **[Evidence gap]** The public files show refresh triggers and picker failure
  text, but do not fully specify catalog versioning, stale-data policy, the
  canonical relationship between `id` and `model` at resume time, or the exact
  behavior when a custom deployment accepts a model name that is absent from the
  hosted catalog.

Evidence: [Codex model service][codex-model-service], [Codex model refresh
worker][codex-model-refresh], [Codex model protocol][codex-model-protocol],
[Codex model picker][codex-model-picker], and [Codex provider registry][codex-provider]
(public source snapshot at the cited commit, accessed 2026-08-05).

### 2.2 GitHub Copilot: policy- and surface-filtered catalog with lifecycle metadata

#### GitHub Copilot: documented facts

- **[Documented fact]** GitHub's supported-model documentation lists models with
  provider and release status, then separately lists availability by Copilot
  client and plan. It states that availability can change and that organizations
  and enterprises can enable or restrict specific models.
- **[Documented fact]** The same catalog publishes extended capabilities: a
  one-million-token context option and configurable reasoning levels, with
  explicit client restrictions. A separate comparison document groups models by
  tasks such as agentic software development, deep reasoning, and visual input;
  these are selection guidance rather than a universal capability guarantee.
- **[Documented fact]** GitHub publishes retirement history with retirement dates
  and suggested alternatives. It also distinguishes base and LTS models, with a
  stated support window for LTS, and documents evaluation models that can be
  added, changed, or removed without notice.
- **[Documented fact]** Auto model selection considers task complexity and
  real-time health/availability, and is constrained by plan and administrator
  policy. The product displays which model was used for each response in several
  surfaces. For Copilot cloud agent, a model picker is available only at certain
  task entry points; where it is absent, Auto is used automatically.
- **[Documented fact]** Copilot CLI's BYOK path accepts an OpenAI-compatible,
  Azure, Anthropic, or local endpoint with an explicit `COPILOT_MODEL`. The model
  must support tool calling and streaming; GitHub recommends a context window of
  at least 128k tokens. This is a capability gate, not a provider catalog.

#### GitHub Copilot: boundary and evidence gap

- **[Evidence gap]** The public matrix establishes availability and lifecycle
  policy, but does not say whether a cloud-agent transcript stores a provider
  deployment ID separately from the display model name, or how a retired model
  already used by a running task is represented after fallback.

Evidence: [Copilot supported models][github-supported-models], [Copilot model
comparison][github-model-comparison], [Copilot auto model selection][github-auto],
[Copilot cloud-agent model selection][github-cloud-agent-model], and [Copilot
CLI BYOK][github-cli-byok] (accessed 2026-08-05).

### 2.3 Gemini CLI: curated Auto/manual picker with explicit subagent overrides

#### Gemini CLI: documented facts

- **[Documented fact]** Gemini CLI's `/model` dialog offers Auto choices for
  model families and a Manual option for a specific model "from those available."
  Its documentation describes Pro as a higher-reasoning choice and Flash as a
  faster choice, and says changes apply to subsequent interactions.
- **[Documented fact]** Gemini's model-routing documentation says the
  `ModelAvailabilityService` monitors model health, can prompt before switching
  to a fallback, and normally keeps the configured model unchanged for silent
  utility fallbacks. It documents precedence among the launch flag, environment,
  settings, local router, and default Auto model.
- **[Documented fact]** The main `/model` command and `--model` flag do not
  override subagents. Built-in agents can have a per-agent `modelConfig.model`
  override, and a visual browser agent has a separate `visualModel` setting.
  Subagents also have independent context windows and can have specialized tools.
- **[Documented fact]** Gemini's settings expose model name, context compression,
  tool-output summarization, plan-mode model routing, and model information in
  the footer. The public picker documentation does not present these settings as
  a complete provider capability schema.

#### Gemini CLI: boundary and evidence gap

- **[Evidence gap]** The docs explain health-based fallback and model precedence,
  but do not disclose the complete catalog source, refresh interval, alias versus
  provider deployment identity, or a full picker error/rollback contract for an
  unavailable manual model.

Evidence: [Gemini model selection][gemini-model], [Gemini model routing][gemini-routing],
[Gemini subagents][gemini-subagents], and [Gemini configuration reference][gemini-config]
(accessed 2026-08-05).

### 2.4 Aider: aliases and model metadata around an open-ended provider set

#### Aider: documented facts

- **[Documented fact]** Aider exposes `/model` to switch the main model and
  `/models` to search available models. It supports `alias:model-name` mappings,
  with command-line aliases taking precedence over config-file and built-in
  aliases. The displayed main model can therefore be a short alias while the
  request target is a longer provider model name.
- **[Documented fact]** Aider's OpenAI integration can list models with
  `--list-models openai/`, while its OpenAI-compatible path accepts a custom base
  URL and an explicit `openai/<model-name>`. The docs direct users to model
  warnings for unfamiliar models rather than implying that every custom endpoint
  has a discoverable catalog.
- **[Documented fact]** Unknown models receive warnings about unknown context
  window and token costs, and Aider may suggest a similarly named model. Users
  can provide model metadata containing input/output limits and costs, and model
  settings containing streaming, cache, reasoning, editor-model, and accepted
  setting information. Fully qualified provider/model names are required for
  metadata entries.
- **[Documented fact]** Aider distinguishes a main model from weak and editor
  models, and its advanced configuration can assign model-specific settings.
  This is a multi-model workflow boundary, although the cited docs do not call
  those models subagents.

#### Aider: boundary and evidence gap

- **[Evidence gap]** The docs do not establish whether an alias is persisted as
  the canonical model identity in a resumed chat, whether `/models` queries the
  provider or only local known metadata for every backend, or how a model removed
  from a provider list is handled after it has been selected.

Evidence: [Aider commands][aider-commands-catalog], [Aider model aliases][aider-aliases],
[Aider advanced model settings][aider-advanced-settings], [Aider OpenAI-compatible
APIs][aider-openai-compatible], and [Aider model warnings][aider-warnings]
(accessed 2026-08-05).

### 2.5 Cline: hosted catalog versus manual OpenAI-compatible configuration

#### Cline: documented facts

- **[Documented fact]** Cline's hosted usage-billing provider advertises access
  to more than 100 supported models and tells users to select from available
  models. It also marks rotating free models in the selector and says that the
  specific free models may change over time.
- **[Documented fact]** Cline's OpenAI-compatible configuration requires a base
  URL, API key, and model ID. The settings allow users to enter or choose the
  model and manually configure max output tokens, context-window size, image
  support, computer use/function calling, and prices. Cline tells users that
  model IDs differ by provider and directs them to provider documentation.
- **[Documented fact]** Cline's failure guidance distinguishes invalid API keys,
  model-not-found errors, and connection errors; for a model-not-found result it
  tells the user to check that the identifier is valid at the selected base URL.
- **[Documented fact]** Cline's CLI model-orchestration example uses separate
  configuration directories for different models and providers, then selects a
  configuration per phase. This makes model scope explicit across a workflow but
  does not document one shared catalog or one inherited subagent identity.

#### Cline: boundary and evidence gap

- **[Evidence gap]** The hosted-provider docs do not disclose how its catalog is
  refreshed or how a retired model is migrated. The custom-provider docs do not
  state whether Cline probes capabilities, trusts manually entered metadata, or
  caches a provider's model list.

Evidence: [Cline hosted provider][cline-hosted-provider], [Cline free models][cline-free-models],
[Cline OpenAI-compatible provider][cline-openai-compatible], and [Cline model
orchestration][cline-orchestration] (accessed 2026-08-05).

### 2.6 Claude Code: aliases and provider-specific deployment identity

#### Claude Code: documented facts

- **[Documented fact]** Claude Code documents model selection by `/model`, a
  startup `--model` flag, environment variables, and settings. Its model
  configuration describes aliases, effort levels that depend on the selected
  model, and provider-specific deployment identifiers.
- **[Documented fact]** Claude Code documents a different resume rule for some
  provider deployments: Bedrock, Google Cloud's Agent Platform, and Microsoft
  Foundry deployment IDs are exceptions to the normal transcript model restore
  behavior. This is direct evidence that a product-level model label and a
  provider deployment identity cannot always be treated as the same value.
- **[Documented fact]** Its interactive-mode documentation describes a model
  picker and a keyboard shortcut, while model configuration describes a
  confirmation/re-read consequence after prior output. These are selection and
  history boundaries, not proof of a public provider catalog API.

#### Claude Code: boundary and evidence gap

- **[Evidence gap]** The cited Claude Code docs do not publish the complete
  catalog-refresh source, all capability fields used by its picker, or the exact
  behavior of subagent model selection relative to the main session. They also
  do not reveal the internal mapping from aliases to deployment IDs.

Evidence: [Claude Code model configuration][claude-model-config], [Claude Code
interactive mode][claude-interactive], and [Claude Code subagents][claude-subagents]
(accessed 2026-08-05).

## 3. Mechanisms and tradeoffs

### 3.1 Catalog provenance and refresh

- **[Documented fact]** Products use more than one catalog source: Codex has an
  online-refreshing product model manager; GitHub maintains a policy-filtered
  supported-model matrix; Cline advertises a hosted supported-model set; Aider
  combines known model metadata with provider listing commands.
- **[Cross-product synthesis]** A picker needs a provenance label for each entry:
  hosted catalog, provider discovery, local metadata, or user-entered identifier.
  The same model string can be valid in one provider or plan and invalid in
  another, so a flat global cache is a poor availability authority.
- **[Evidence gap]** Most products do not publish freshness, cache invalidation,
  or atomic refresh semantics. A retry message alone does not tell the user
  whether the previous catalog remains safe to use.

### 3.2 Identity, alias, and deployment mapping

- **[Documented fact]** Codex separates ID, model, and display name; Aider
  supports user aliases and provider-qualified metadata; Cline asks for a
  provider-specific model ID; Copilot's Azure BYOK example uses a deployment name
  as the model identifier; Claude documents provider deployment exceptions.
- **[Cross-product synthesis]** A durable selection record should conceptually
  distinguish at least `display label`, `user alias`, `canonical model`,
  `provider/deployment`, and `catalog provenance`. This is a reusable industry
  abstraction, not a claim that any one product exposes exactly these fields.
- **[Evidence gap]** Public docs rarely say which of these values is written to
  transcripts, usage reports, or resume state. A usage report can show a model
  label without proving the exact deployment that served the request.

### 3.3 Reasoning, tools, context, and modality

- **[Documented fact]** Codex exposes supported reasoning efforts, input
  modalities, service tiers, and provider capabilities. GitHub exposes reasoning
  levels, extended context, client restrictions, and model-specific modality
  guidance. Cline exposes manual context, image, and computer-use/function-calling
  settings for custom endpoints. Copilot CLI requires tool calling and streaming.
- **[Cross-product synthesis]** Capability metadata should be treated as a
  compatibility matrix over model, provider, client surface, and task role. A
  model can support reasoning but not the selected effort; support images but not
  in a particular client; or answer chat requests but fail an agent requiring
  streaming tool calls.
- **[Evidence gap]** Vendor documentation does not consistently distinguish
  declared capability, entitlement, current health, and successful negotiation.
  A picker label is not a runtime capability probe.

### 3.4 No-catalog custom endpoints

- **[Documented fact]** Aider and Cline allow a custom OpenAI-compatible base URL
  with an explicit model identifier. Aider supplies conservative unknown-model
  warnings and lets users add metadata; Cline supplies explicit advanced model
  fields and a model-not-found troubleshooting path. Copilot CLI has explicit
  BYOK requirements for tool calling and streaming.
- **[Cross-product synthesis]** A no-catalog fallback should be visibly distinct
  from a catalog-backed picker: accept a free-form ID, show what is known versus
  user-declared, validate required agent capabilities before execution when
  possible, and retain provider context in errors. Hiding the absence of discovery
  creates false confidence in availability and capability badges.
- **[Evidence gap]** The cited products do not disclose a common capability
  negotiation protocol. Manual metadata may be stale, and a successful model-list
  response would not by itself prove support for every tool or reasoning mode.

### 3.5 Unavailable, retired, and picker-failure states

- **[Documented fact]** GitHub publishes retirement dates and alternatives;
  Gemini documents health-based fallback with possible user consent; Aider
  suggests names for unknown identifiers; Cline distinguishes model-not-found
  from endpoint errors; Codex reports a temporary catalog-update state.
- **[Cross-product synthesis]** User-visible state should distinguish:
  `not in this catalog`, `not entitled on this surface`, `retired with a suggested
  replacement`, `temporarily unhealthy with fallback`, `custom ID not found`, and
  `catalog refresh failed`. The appropriate action differs between omission,
  migration, retry, and user correction.
- **[Evidence gap]** No cited source gives a complete cross-layer rollback
  contract: whether a failed selection leaves the old model active, whether a
  fallback is persisted, and how a picker behaves while a refresh is in flight.

### 3.6 Main session versus subagent scope

- **[Documented fact]** Gemini states that main-session `/model` and `--model`
  changes do not override subagents and offers per-agent overrides. Its visual
  agent also has a separate model setting. Cline's examples select separate
  configuration directories per workflow phase. GitHub's cloud-agent model
  picker is limited to certain entry points and its model matrix is client-aware.
- **[Cross-product synthesis]** A model selector should state its scope next to
  the selection: main conversation, current turn, subagent role, visual/tool
  specialist, workflow phase, or entire process. "Current model" is otherwise
  underspecified in a multi-agent transcript.
- **[Evidence gap]** Public materials generally do not disclose whether a
  subagent inherits the parent's tool, context, reasoning, and deployment
  capability metadata as a snapshot or resolves them independently at launch.

## 4. Cross-product synthesis

The evidence suggests a layered model-selection contract:

```text
catalog source
  -> availability and policy filter
  -> identity resolution (alias -> canonical -> deployment)
  -> capability view (reasoning, tools, context, modality, surface)
  -> scope assignment (main, role, subagent, phase)
  -> health / entitlement validation
  -> selection or fallback with explicit user-visible state
```

- **[Cross-product synthesis]** Catalog-backed products optimize discoverability
  and safe defaults, but their entries are bounded by plan, surface, provider,
  and freshness. The catalog is therefore an availability claim with a timestamp
  or provenance, not a universal truth about an endpoint.
- **[Cross-product synthesis]** Free-form endpoint support optimizes reach and
  private deployment compatibility, but requires explicit IDs, manual metadata,
  and clearer failure messages. It should not silently inherit badges or
  reasoning/tool assumptions from a similarly named hosted model.
- **[Cross-product synthesis]** Capability-aware selection is more informative
  than a model-name picker, but capability data needs scope and confidence. A
  provider declaration, product policy, current health check, and successful tool
  negotiation answer different questions.
- **[Cross-product synthesis]** Lifecycle handling should preserve the distinction
  between a stable canonical identity and a recommended upgrade. GitHub's
  retirement/alternative tables and Codex's upgrade metadata show that an upgrade
  suggestion is not necessarily an automatic identity rewrite.
- **[Evidence gap]** The industry evidence is strongest at selection and display
  boundaries. It is weakest at durable identity, refresh races, capability
  negotiation, and the exact accounting of fallback turns across main and
  delegated agents.

## 5. Pitfalls and evidence gaps

- **[Evidence gap]** A catalog entry does not prove that the model is entitled,
  healthy, reachable through the configured provider, or compatible with the
  current client surface.
- **[Evidence gap]** An alias or friendly display label does not prove the
  deployment ID that served a request. This matters most for Azure, Bedrock,
  enterprise gateways, and other provider-specific deployments.
- **[Cross-product synthesis]** A stale catalog should fail differently from an
  invalid custom identifier. Retry, refresh, replacement, and endpoint correction
  are separate recovery actions.
- **[Cross-product synthesis]** Auto routing can improve availability while making
  model identity less obvious. Products that display the model used per response
  reduce that ambiguity, but public docs still do not establish durable transcript
  semantics for every fallback.
- **[Evidence gap]** Main-session capability metadata cannot safely be assumed for
  subagents, visual agents, editor/weak models, or workflow phases unless the
  product explicitly documents inheritance.
- **[Evidence gap]** The cited sources do not establish an industry-wide standard
  for model-list schemas, capability vocabularies, retirement signals, or picker
  error codes. Similar terms such as "Auto," "available," and "supported" have
  product-specific meanings.

## References

[codex-model-service]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server/src/models.rs
[codex-model-refresh]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server/src/models_refresh_worker.rs
[codex-model-protocol]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/src/protocol/v2/model.rs
[codex-model-picker]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/chatwidget/model_popups.rs
[codex-provider]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/model-provider-info/src/lib.rs
[github-supported-models]: https://docs.github.com/en/copilot/reference/ai-models/supported-models
[github-model-comparison]: https://docs.github.com/en/copilot/reference/ai-models/model-comparison
[github-auto]: https://docs.github.com/en/copilot/concepts/models/auto-model-selection
[github-cloud-agent-model]: https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/changing-the-ai-model
[github-cli-byok]: https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/use-byok-models
[gemini-model]: https://geminicli.com/docs/cli/model/
[gemini-routing]: https://geminicli.com/docs/cli/model-routing/
[gemini-subagents]: https://geminicli.com/docs/core/subagents/
[gemini-config]: https://geminicli.com/docs/reference/configuration/
[aider-commands-catalog]: https://aider.chat/docs/usage/commands.html
[aider-aliases]: https://aider.chat/docs/config/model-aliases.html
[aider-advanced-settings]: https://aider.chat/docs/config/adv-model-settings.html
[aider-openai-compatible]: https://aider.chat/docs/llms/openai-compat.html
[aider-warnings]: https://aider.chat/docs/llms/warnings.html
[cline-hosted-provider]: https://docs.cline.bot/getting-started/cline-provider
[cline-free-models]: https://docs.cline.bot/getting-started/free-models
[cline-openai-compatible]: https://docs.cline.bot/provider-config/openai-compatible
[cline-orchestration]: https://docs.cline.bot/cli/samples/model-orchestration
[claude-model-config]: https://code.claude.com/docs/en/model-config
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode
[claude-subagents]: https://code.claude.com/docs/en/sub-agents
