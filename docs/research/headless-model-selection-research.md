# Headless per-invocation model selection: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Model aliases, provider availability, and resume
> rules change across CLI releases; re-verify the observed versions before
> adopting a contract.
>
> Scope: how deployed coding-agent CLIs expose a model override for a single
> headless invocation, including fresh and resumed sessions.
>
> Out of scope: model quality, provider routing internals, pricing, token
> accounting, and undocumented behavior when a model is retired.

## 1. Conclusions

- **Documented fact.** Codex CLI 0.146.0 exposes `-m, --model <MODEL>` on both
  `exec` and `exec resume`, described as the model the agent should use. [O1]
- **Documented fact.** Claude Code 2.1.212 exposes `--model <model>` and
  describes it as the model for the current session; the help accepts aliases
  as well as full model names. [C1]
- **Documented fact.** Gemini CLI 0.44.1 exposes `-m, --model` in its top-level
  headless-capable CLI. [G1]
- **Cross-product synthesis.** A model override is a launch/session setting,
  not a new conversation identity and not a request to rewrite prior transcript
  content. It should be resolved before model startup and be observable in the
  invocation's effective configuration or diagnostics.
- **Evidence gap.** The observed help does not state whether a resumed turn
  permanently updates the source session's recorded model, how aliases are
  resolved, or whether a failed provider lookup leaves any durable metadata.

## 2. Evidence from deployed applications

### 2.1 Codex CLI 0.146.0

On 2026-08-04, the installed `codex-cli 0.146.0` binary reported `-m,
--model <MODEL>` for both `codex exec` and `codex exec resume`, with the
description "Model the agent should use." The option is presented alongside
other invocation controls, while the resume command still takes an explicit
session ID or `--last`. The help does not state whether a resumed session's
durable metadata is rewritten after the override. [O1]

### 2.2 Claude Code 2.1.212

The installed `2.1.212 (Claude Code)` binary reported `--model <model>` with
the description "Model for the current session." It documents aliases such as
the latest `opus` or `sonnet` line as well as full model names. The wording
connects the setting to the current session, but does not expose a stable
machine-readable alias-resolution result in help. [C1]

### 2.3 Gemini CLI 0.44.1

The installed `0.44.1` binary reported `-m, --model` as a string option on the
top-level CLI, alongside `--prompt` and `--resume`. Its help does not describe
accepted aliases, provider lookup errors, or whether a resumed session records
the selected model as new durable metadata. [G1]

## 3. Mechanisms and tradeoffs

| Decision | Per-invocation override | Config-only selection | New session/fork |
| --- | --- | --- | --- |
| Conversation identity | Reuses the requested fresh/resumed session | Reuses the requested session | Creates a new identity |
| Model selection | Effective for this launch | Effective for launches using that config | Product-defined for the child |
| Failure timing | Can fail before the first model call | Usually fails at launch or first use | Can fail while creating the child |
| Durable metadata | Product-defined; should not silently rewrite history | Configuration state may persist | Child metadata may record its model |
| Automation benefit | Easy A/B or fallback invocation | Stable operator default | Explicit branch ownership |

The safest observable boundary is to resolve the requested model before opening
the provider stream, then reject an unknown or unusable value as a startup/input
failure without committing a new turn. A resumed conversation should retain
its previous transcript; switching the model does not imply replaying old tool
calls or changing the session's identity.

## 4. Cross-product synthesis

- **Launch settings should not be confused with transcript data.** All three
  products put model selection at the CLI invocation surface; none of the
  observed help says to edit earlier messages or fork automatically.
- **Aliases are a UX layer, not a stable storage key.** Claude explicitly
  advertises aliases, while Codex and Gemini help is less specific. Automation
  should retain the user-supplied value separately from any provider-resolved
  identifier when a product exposes both.
- **Resume and override are orthogonal.** A caller can identify the context to
  reuse and independently choose which model handles the next turn. This is
  different from a fork, which changes session ownership/identity.
- **Startup failure must precede durable turn commit.** An invalid model should
  not produce a successful session continuation merely because the session ID
  was found.

## 5. Pitfalls and evidence gaps

- **Provider aliases drift.** A value accepted by one product version may be
  rejected or point to a different model after an upgrade.
- **Resume metadata is unspecified.** The public help does not settle whether a
  resumed source session's `model` field is updated, left unchanged, or recorded
  per turn.
- **Fallback is not override.** A requested model can be unavailable without
  implying permission to silently choose a different model; products should
  state fallback behavior separately.
- **Model selection does not isolate tools.** Changing the model does not by
  itself change the workspace, permissions, sandbox, or external side effects.
- **A successful lookup is not a successful turn.** Provider startup, auth,
  rate limits, and context limits remain separate failure surfaces.

## References

All sources accessed 2026-08-04.

- **[O1] Local product observation:** `codex --version`, `codex exec --help`,
  and `codex exec resume --help`; observed version `codex-cli 0.146.0`.
  This is a reproducible observation of the installed binary, not a provider
  or storage schema.
- **[C1] Local product observation:** `claude --version` and `claude --help`;
  observed version `2.1.212 (Claude Code)`. Canonical reference:
  https://code.claude.com/docs/en/cli-reference
- **[G1] Local product observation:** `gemini --version` and `gemini --help`;
  observed version `0.44.1`. Canonical reference:
  https://geminicli.com/docs/cli/cli-reference/
