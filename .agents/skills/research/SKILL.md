---
name: research
description: >
  Industry research for coding agents and related systems. Use when the user asks
  to research, survey, compare, or investigate industry practice; mentions Codex,
  Claude Code,Grok Build , Cursor, Aider, OpenHands, Continue, or similar CLI/IDE agents;
  wants vendor/API behavior on tools, context, permissions, sessions, compaction,
  or termination; or wants a writeup under docs/research/. Prefer this skill over
  reading local product code for design inspiration. Research external systems
  only — do not audit or map this repository's implementation.
---

# research

Produce high-signal **industry** research: how mature products and APIs actually
do a thing, what tradeoffs they make, and which patterns are efficient/reasonable.

This is not a local codebase audit, not an issue writeup, and not a generic
"how to research" tutorial.

## Hard boundary

Stay outside this repo's product code while researching.

- **Do research**: vendor docs, official blogs, public specs, release notes,
  reputable engineering posts, and **other** open-source implementations.
- **Do not open** local product paths for "how we do it today"
  (`internal/`, `cmd/`, app packages, tests of product behavior).
- **Local writes only**: `docs/research/<topic>-research.md`.
- **Optional local read**: existing `docs/research/*` only for tone/structure,
  never as evidence of industry practice.
- **Do not** end with "how this repo should change" or "minimal local MVP".
  Implementation mapping is a later, separate step if the user asks.

If the user explicitly asks to compare industry findings to this repo, finish the
industry research first, then ask before touching product code.

## Workflow

1. **Frame the question**
   - One concrete decision surface (e.g. tool-call termination, command
     permissions, context compaction).
   - In scope / out of scope in 2–4 bullets.
   - Prefer mechanisms and failure modes over product marketing.

2. **Search widely (no memory-only conclusions)**
   Cover at least:
   - Mature coding agents: Codex CLI, Claude Code,Grok Build and 1–2 peers when relevant
     (Cursor, Aider, OpenHands, Continue, etc.)
   - Provider/API semantics when the topic depends on them (OpenAI, Anthropic,
     others as needed)
   - At least one additional independent source class: OSS implementation,
     engineering post, or formal docs/spec

   Prefer primary sources. Note the research date; products drift.

3. **Cross-check claims**
   - Separate observed behavior / documented API from inference.
   - When sources disagree, show the disagreement instead of forcing a false
     consensus.
   - Mark weak or single-source claims.

4. **Synthesize for reuse**
   Focus on:
   - Dominant industry patterns
   - Efficient/reasonable defaults and why they work
   - Tradeoffs and applicability boundaries
   - Common pitfalls and anti-patterns

   Avoid empty landscape tours with no decision value.

5. **Write the note**
   Path: `docs/research/<topic>-research.md`  
   Do **not** put research under `docs/issues/`.

## Output template

Use this structure unless the topic truly needs a tighter shape:

```markdown
# <Topic>: industry practice

> Status: research note, not an implementation plan.
>
> Research date: YYYY-MM-DD. Re-verify before adopting; vendor behavior changes.
>
> Scope: ...
> Out of scope: ...

## 1. Summary
- 3–6 bullets: main patterns, key tradeoffs, sharp edges

## 2. Problem boundary
- Terms, layers, and what is often confused

## 3. Industry mechanisms
- Compare mature systems/APIs by mechanism, not brand slogans
- Use tables or short subsections when helpful

## 4. Efficient / reasonable patterns
- What good systems converge on
- When a pattern fits vs when it does not

## 5. Pitfalls
- Failure modes seen in real products/APIs

## 6. Open questions
- What remains uncertain or needs re-check

## References
- Dated links / sources
```

Writing bar:
- Chinese is fine when the user writes in Chinese; keep terms precise.
- Every important claim should be traceable to a source or explicitly marked as
  inference.
- Prefer concrete mechanics (`tool_choice`, `max_turns`, stop reasons, approval
  gates) over vague advice ("be careful with context").

## Quality bar (reasonable research)

A good note is reasonable when it is:

- **Scoped**: one decision surface, explicit non-goals
- **Multi-source**: not a single blog paraphrased
- **Mechanistic**: explains control points and runtime behavior
- **Comparative**: shows where serious systems converge or diverge
- **Actionable later**: a reader could design from it without reading this repo
- **Non-prescriptive to this repo**: no local migration plan unless requested

Reject these failure modes:

- Reading local `internal/`/`cmd/` to reverse-engineer "the answer"
- Turning research into an issue, rewrite plan, or PR outline
- Tutorial filler ("what is an agent?")
- Undated vendor claims with no source
- Fake certainty where docs are ambiguous

## Quick triggers

Use this skill for prompts like:

- "调研一下 Codex/Claude Code 怎么做 X"
- "research industry approaches to Y"
- "对比主流 CLI agent 的权限/会话/压缩/终止"
- "写一份 docs/research/..."

Do not use it for:

- Debugging this repo from local code
- Writing `docs/issues/*`
- Implementing a feature in-tree
