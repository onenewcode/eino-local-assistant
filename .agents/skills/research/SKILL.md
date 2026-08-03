---
name: research
description: >
  Research a user-specified industry direction in deployed AI applications and
  agentic systems. Use when the user asks to research, survey, compare, or
  investigate current industry practice, product behavior, or engineering
  tradeoffs. Treat the direction as the unit of research: search broadly across
  relevant real applications, not a preselected vendor list, framework, SDK, or
  API. Research external systems only; do not audit or map this repository's
  implementation.
---

# research

Produce high-signal industry research about how deployed applications handle a
specific problem. The research should reveal mechanisms, tradeoffs, boundaries,
and evidence gaps -- not create a product tour or a local implementation plan.

## Scope and boundaries

- Treat the user's question as an **industry direction**. Examples: long-running
  task interruption, multi-agent user routing, context transfer, approval UX,
  or agent reliability.
- Select evidence from relevant, publicly deployed applications and their public
  engineering material. Do not start from a canned list of companies or tools.
- If the user names a product, use it as one lead when relevant; do not let it
  define the entire landscape or replace comparison with other applications.
- Do not make frameworks, SDKs, APIs, or source-code interfaces the subject of
  a direction-level study. Use them only when the user explicitly asks about
  that interface itself.
- When an application does not disclose an internal mechanism, say so. Do not
  fill the gap with API behavior, UI speculation, or an invented architecture.
- Stay outside this repository's product code. Read local `docs/research/*`
  only for an existing note's scope or style; do not read `internal/`, `cmd/`,
  tests, or application packages as industry evidence.
- Write research only under `docs/research/<topic>-research.md`. Do not create
  issue documents or turn the note into a local migration plan.

## Evidence strategy

Search broadly before writing. Choose sources because they illuminate the
question, not because they are familiar.

1. Define the decision surface in one sentence and state 2-4 scope boundaries.
2. Find multiple independent deployed applications relevant to that surface.
   Prefer public engineering articles, product documentation describing observed
   behavior, release notes, and recorded product interactions. Aim for at least
   three independent sources when the market supports it; disclose a narrower
   evidence base when it does not.
3. Prefer primary sources for a product's own behavior. Use independent sources
   to challenge marketing claims, establish adoption context, or surface a
   disagreement -- never to invent private internals.
4. Look for concrete control points: what triggers a behavior, who owns a user
   interaction, what information moves, what can be interrupted, what remains
   persistent, and what the product explicitly does not promise.
5. Check whether evidence describes an actual shipped behavior, a preview, an
   experiment, a roadmap, or a generic recommendation. Label it accordingly.

For a broad direction, source selection should normally span different product
categories or vendors. A single product's blog can be a useful case study, but
it cannot establish an industry norm by itself.

## Reasoning discipline

Separate every important statement into one of these classes:

- **Documented fact**: a source directly states or demonstrates the behavior.
- **Cross-product synthesis**: a reasoned pattern supported by multiple facts.
- **Evidence gap**: products do not disclose enough to make the claim.

Do not equate a product's UI with its internal consistency, cancellation,
routing, or security semantics. Do not call a pattern "standard" merely because
two products use related vocabulary. When applications diverge, explain the
applicability boundary instead of forcing a consensus.

If the user asks for a design question, describe a reusable abstract model only
after presenting the evidence. Mark it as synthesis, keep it product-neutral,
and do not map it to this repository unless the user separately requests that.

## Workflow

1. **Frame the direction**
   - Name the concrete decision surface.
   - State in scope and out of scope.
   - Identify terms that must not be conflated.
2. **Build the source set**
   - Search for relevant live applications rather than familiar products.
   - Include cases that converge and cases that make different tradeoffs.
   - Record publication/update and access dates.
3. **Extract mechanisms**
   - Capture input, control flow, state/context movement, user-visible behavior,
     failure handling, and stated limitations.
   - Keep each claim traceable to a source.
4. **Cross-check and classify**
   - Mark single-source claims and vendor self-reports.
   - Separate facts, synthesis, and unknowns.
5. **Write the note**
   - Lead with conclusions, then evidence, synthesis, pitfalls, and open
     questions.
   - Prefer short case cards and narrow tables over dense landscape matrices.
   - Include links and a research date for every source set.
6. **Validate before handoff**
   - Check that no preselected product roster, framework, SDK, or API became the
     de facto research subject.
   - Check that the note contains no local implementation mapping and does not
     claim undisclosed internals as fact.

## Output shape

Use this structure unless the direction calls for a tighter one:

```markdown
# <Direction>: industry practice

> Status: research note, not an implementation plan.
>
> Research date: YYYY-MM-DD. Re-verify before adopting; products change.
>
> Scope: ...
> Out of scope: ...

## 1. Conclusions

## 2. Evidence from deployed applications

## 3. Mechanisms and tradeoffs

## 4. Cross-product synthesis

## 5. Pitfalls and evidence gaps

## References
```

Writing bar:

- Write in the user's language when appropriate; keep terms precise.
- Cite every material factual claim and attach access dates.
- Use applications as evidence, not as a substitute for reasoning.
- State what remains unknown rather than using a framework/API as a proxy for
  unobserved product behavior.
- Do not end with "how this repository should change" unless the user asked for
  a separate local design step.
