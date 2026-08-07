---
name: research
description: >
  Research current industry practice in deployed AI applications and agentic
  systems. Study a problem or decision, not a vendor list; use external
  evidence only.
---

# research

Explain how real applications solve one industry problem: mechanisms,
tradeoffs, limits, and unknowns. Do not write a product tour or a local plan.

## Coding-agent references

When the direction concerns coding agents, use these as primary reference
cases, then add other relevant products; they are not a fixed vendor roster:

- **Codex**: sessions/resume, `AGENTS.md`, tool loops, approvals, sandbox, and
  context/status UX. Sources: [docs](https://developers.openai.com/codex/) and
  [repository](https://github.com/openai/codex).
- **Grok Build**: TUI, sessions/rewind, queue/background work, subagents,
  permissions, and sandbox. Source: [repository](https://github.com/xai-org/grok-build).
- **OpenCode**: sessions/compaction, tools, permissions, providers, and TUI/ACP.
  Sources: [docs](https://opencode.ai/docs/) and
  [repository](https://github.com/anomalyco/opencode).

Compare documented or observable behavior, pin source versions/commits where
possible, and do not infer undisclosed internals.

## Rules

- Define one decision surface and 2-4 boundaries before searching. Product
  names are leads/cases; if the user gives only names, state the direction and
  broaden the evidence set.
- Prefer shipped behavior and primary docs, engineering posts, release notes,
  or recorded interactions. Use independent sources to challenge claims. Aim
  for three sources when available and record publication/update plus access
  dates.
- Treat frameworks, SDKs, APIs, and source interfaces as evidence, not the
  research subject, unless the user explicitly asks about them. Never infer
  private internals from a UI or API.
- Label each material claim **fact**, **synthesis**, or **evidence gap**; mark
  vendor self-reports and preview/experiment/roadmap status.
- Extract trigger, control flow, state/context movement, persistence,
  interruption, failure handling, user-visible behavior, and explicit limits.
- Stay outside this repository's product code. Existing `docs/research/*` may
  provide scope/style only; do not use `internal/`, `cmd/`, tests, or packages
  as industry evidence.
- Write only `docs/research/<topic>-research.md`; do not create issues or a
  repository migration plan.

## Process

1. Frame the question and exclusions; separate terms that are easy to conflate.
2. Search broadly across relevant applications and categories, including
   divergent tradeoffs.
3. Extract source-backed mechanisms, cross-check claims, then write conclusions
   before evidence, tradeoffs, synthesis, risks, and open questions.
4. Before handoff, check that the note contains no roster-only comparison,
   local mapping, or speculation presented as fact.

## Note shape

```markdown
# <Direction>: industry practice

> Status: research note, not an implementation plan.
> Research date: YYYY-MM-DD; re-verify before adoption.
> Scope: ...
> Out of scope: ...

## 1. Conclusions
## 2. Evidence from deployed applications
## 3. Mechanisms and tradeoffs
## 4. Cross-product synthesis
## 5. Pitfalls and evidence gaps
## References
```

Write in the user's language when useful. Cite every material fact with a link
and access date; state what remains unknown. Do not end with repository changes
unless the user separately requests a local design step.
