# Interactive input admission and draft retention: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Product behavior and public source change; re-check
> the cited revisions before treating them as a compatibility contract.
>
> Decision surface: how deployed coding agents separate an input draft from an
> accepted steer or queued message, and what users can observe when admission,
> execution, or cancellation fails.
>
> Scope: active-task steering, queued follow-ups, admission acknowledgement,
> target/run validation, error settlement, and user-visible retention or
> editability of not-yet-executed input.
>
> Out of scope: model APIs, generic agent frameworks, permission enforcement,
> workspace rollback, and private UI implementation details that products do
> not disclose.

## 1. Conclusions

1. **Admission and execution are separate lifecycle points.** Gemini
   acknowledges a steering hint before the next model call, while OpenCode
   records an admitted input before promoting it to execution. A receipt or
   acknowledgement should not claim that the model has already seen the text.
2. **Steer needs a target and a rejection path.** Current Codex source checks
   whether an active turn exists and whether an expected turn ID matches. An
   invalid target is rejected rather than silently attached to another run;
   only the explicit no-active-turn case falls through to starting a new task.
3. **Queue is a distinct user contract.** OpenCode stores steer and queue
   delivery separately and promotes them with different rules. Roo documents a
   visible FIFO queue whose messages can be edited or deleted and remain queued
   when processing encounters an error. A failed steer should not be converted
   into an unrelated FIFO item.
4. **Products rarely publish the editor-draft rule.** The reviewed public
   materials describe accepted-input acknowledgement, durable admission, or
   queue retention, but do not consistently state whether a local text draft is
   cleared before an admission call or restored after rejection. This is an
   evidence gap, not permission to assume that clearing is safe.
5. **The safest cross-product synthesis is lossless admission.** Keep the local
   draft until the intended delivery path has accepted it; after admission,
   track the accepted input independently so cancellation or failed execution
   can settle it without duplicating it. Preserve the distinction between
   “not admitted”, “admitted but not consumed”, and “consumed/committed”.

## 2. Evidence from deployed applications

### 2.1 Gemini CLI: acknowledgement before next model turn

**Documented facts.** Gemini CLI's experimental model steering treats text
typed while an agent is working as a steering hint. Pressing Enter submits it;
the CLI gives an immediate acknowledgement and injects the hint into the model
context for the very next turn. The documentation presents steering as a way
to correct a path, skip a step, add context, or redirect the effort, rather
than as a second independent task. [Gemini model steering][gemini-steering]

The page does not document an admission error taxonomy, a target-turn ID, or
what happens to the editor contents if the active task ends between typing and
submission. It establishes an acknowledgement-before-model-boundary pattern,
not a complete draft-retention contract.

### 2.2 Codex CLI: active-turn identity and explicit fallback boundary

**Documented source behavior.** The current public Codex source defines
distinct steering errors for no active turn, expected-turn mismatch, and
non-steerable turn kinds. The request handler first attempts to steer; it falls
back to a new regular task only for the explicit no-active-turn result. A
stale expected turn or a non-steerable turn is reported as an error instead of
being redirected to another active run. [Codex handlers][codex-handlers]
  [Codex session][codex-session]

This source establishes target validation and admission outcome semantics. It
does not establish how every Codex surface manages its local text editor after
an error, so editor draft retention remains an evidence gap.

### 2.3 OpenCode: durable input admission and separate delivery modes

**Documented source behavior.** OpenCode's current session input module first
checks whether an input ID was already admitted, then publishes a durable
`PromptAdmitted` event and obtains an aggregate sequence. Its projection keeps
the delivery mode and separates promotion of admitted `steer` inputs from
promotion of `queue` inputs; steer promotion is ordered and queue promotion
selects a FIFO item. The module also handles duplicate admission by returning
the existing record rather than creating another logical input. [OpenCode
input][opencode-input]

This is stronger than a UI-only “sent” flag: an accepted input has an identity
and durable admission state before it is promoted. The source does not promise
that a failed local request editor is restored, but it makes accidental loss
after admission distinguishable from rejection before admission.

### 2.4 Roo Code: visible FIFO retention under errors

**Documented facts.** Roo Code documents message queueing while a task is
working. Queued messages appear to the user, are processed FIFO, and can be
edited or deleted before processing. Its FAQ explicitly says queued messages
remain in the queue when an error occurs, allowing the user to cancel them or
continue processing. The same documentation warns that queued messages
implicitly approve the next pending action, so queueing is not merely a visual
buffer. [Roo message queueing][roo-queue]

Roo's behavior is queue-specific, not evidence that it has a steer mailbox or
that a rejected steer should enter the queue. It does show that a deployed
coding agent can make unprocessed user input visible and recoverable after an
execution error.

### 2.5 Claude Code: public draft boundary not established here

Claude Code's interactive and checkpoint documentation is relevant to
interruption and recovery, but the current access attempt for the official
interactive-mode page timed out. This note therefore does not claim a current
Claude-specific rule for editor contents after failed steering admission. The
absence of a verified public rule is itself an evidence gap; it should not be
filled with assumptions from another product.

## 3. Mechanisms and tradeoffs

| Stage | User-visible meaning | Safe state transition |
| --- | --- | --- |
| Editing | Text exists only in the local composer | Keep local draft; no task state changes |
| Admission attempt | Product is validating target/run and delivery mode | Do not erase draft yet |
| Admitted | Input has a receipt/identity and belongs to a target run or queue | Clear or move the draft only after success |
| Consumed | Model/tool loop crossed the declared delivery boundary | Mark receipt consumed; do not claim commit yet |
| Committed | Target turn or queued turn durably committed | Settle receipt as committed |
| Rejected | No target accepted the input | Keep draft available and show reason |
| Discarded | Admitted input cannot be consumed/committed | Show settlement; product may restore it according to explicit policy |

The products expose different slices of this state machine:

- Gemini exposes acknowledgement and next-turn delivery.
- Codex exposes target identity and error categories.
- OpenCode exposes durable admission and delivery modes.
- Roo exposes a recoverable, editable queue and error retention.

No single product page proves every transition. The table is a
cross-product synthesis, not a claim about a shared internal API.

## 4. Cross-product synthesis

The most reusable contract is **lossless until admission, explicit after
admission**:

1. Validate the active target and delivery mode before clearing the local draft.
2. On rejection, preserve the exact draft, leave queue and active-turn state
   unchanged, and display a reason that distinguishes no target, stale target,
   unsupported mode, and core failure where possible.
3. On successful admission, give the input a receipt or durable identity and
   only then clear or transfer the local draft.
4. Acknowledge admission separately from model-boundary consumption and final
   commit.
5. Never silently change a rejected steer into a FIFO follow-up; offer a
   separate queue action if the product wants that recovery.

This synthesis is intentionally narrower than “all input must be durable”. A
local composer draft may remain process-local; the important invariant is that
the UI does not destroy it before the delivery operation has succeeded. Once
accepted, durable or receipt-tracked settlement becomes the relevant contract.

## 5. Pitfalls and evidence gaps

- A UI can clear the composer before calling the core and still display an
  error; that makes a stale-turn rejection look like a user typo and loses the
  only copy of the intended correction.
- A successful admission acknowledgement must not say the model has already
  consumed the input. Gemini explicitly places delivery at the next turn, and
  OpenCode distinguishes admission from promotion.
- Reusing the same fallback path for steer and queue hides materially different
  semantics: steer targets the current run, while queue waits for a later run.
- Admission receipts without a target/run identity are not enough to settle
  late events or prevent a stale response from being attached to a new run.
- Public product materials do not consistently disclose whether local drafts
  survive process crashes, reconnects, or editor replacement. Do not infer
  durable recovery from a visible queue or a model acknowledgement.
- Claude-specific draft retention remains unverified in this research pass;
  current public evidence should be checked again before claiming parity.

## References

- [Gemini CLI: Model steering][gemini-steering] (official docs; accessed
  2026-08-05).
- [OpenAI Codex `handlers.rs`][codex-handlers] (public source at commit
  `11e390bb10bb74960cf144309bc677cc3513f240`; accessed 2026-08-05).
- [OpenAI Codex `session/mod.rs`][codex-session] (public source at commit
  `11e390bb10bb74960cf144309bc677cc3513f240`; accessed 2026-08-05).
- [OpenCode session input][opencode-input] (public source at commit
  `f0afb6750e63ee0a60b052914531bde0afb9bc2b`; accessed 2026-08-05).
- [Roo Code: Message Queueing][roo-queue] (official docs, last updated
  2026-05-15; accessed 2026-08-05).
- [Claude Code: Interactive mode][claude-interactive] (official docs; access
  timed out during this research pass, re-verify before adoption).

[gemini-steering]: https://raw.githubusercontent.com/google-gemini/gemini-cli/ac42fb0a24fe7349e9968e2359ef5232f1cb6e72/docs/cli/model-steering.md
[codex-handlers]: https://github.com/openai/codex/blob/11e390bb10bb74960cf144309bc677cc3513f240/codex-rs/core/src/session/handlers.rs
[codex-session]: https://github.com/openai/codex/blob/11e390bb10bb74960cf144309bc677cc3513f240/codex-rs/core/src/session/mod.rs
[opencode-input]: https://github.com/anomalyco/opencode/blob/f0afb6750e63ee0a60b052914531bde0afb9bc2b/packages/core/src/session/input.ts
[roo-queue]: https://roocodeinc.github.io/Roo-Code/features/message-queueing
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode
