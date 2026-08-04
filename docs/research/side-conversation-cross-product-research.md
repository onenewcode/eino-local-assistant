# 旁路对话（side conversation / btw）：行业实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Re-verify before adoption because products and
> source branches change.
>
> Decision surface: how a terminal agent can answer a short question beside an
> active task without confusing the primary turn, context, ledger, permissions,
> or cleanup lifecycle.
>
> Scope: shipped interactive terminal behavior documented by product sources or
> inspectable public source. Out of scope: this repository's implementation,
> private internals, and conclusions from an undocumented command's absence.

## 1. Conclusions

- **Documented fact:** Codex exposes `/side` and `/btw` as an ephemeral fork
  available during a task. Its child gets an explicit boundary: inherited
  history is reference-only, and only post-boundary input is active. ([Codex
  slash commands](https://github.com/openai/codex/blob/main/codex-rs/tui/src/slash_command.rs),
  [Codex side thread](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/side.rs))
- **Documented fact:** Codex keeps the parent status visible, interrupts and
  unsubscribes the child during cleanup, and surfaces fork/cleanup failures.
  Its side instructions prohibit mutation, escalation, and sub-agents unless a
  user explicitly asks for a mutation after the boundary. ([Codex side thread](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/side.rs))
- **Cross-product synthesis:** A side conversation is a semantic mode, not
  merely `/new`, `/resume`, a durable fork, or a sub-agent. The contract must
  state what happens to the primary turn, which inherited instructions are
  inactive, and whether the child can write.
- **Cross-product synthesis:** “Ephemeral” and “not in the primary ledger” are
  different claims. Codex marks a child ephemeral; Gemini documents automatic
  saving of complete sessions, including tool executions. Cleanup and retention
  therefore need an explicit contract. ([Gemini session management](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/session-management.md))
- **Evidence gap:** OpenCode documents session creation/switching and a granular
  permission system, but the inspected public sources do not document `/side`,
  `/btw`, or a side-specific context/ledger lifecycle. ([OpenCode TUI](https://opencode.ai/docs/tui/),
  [OpenCode permissions](https://opencode.ai/docs/permissions/))
- **Evidence gap:** Claude Code's official pages were unavailable during this
  research window. No Claude behavior is inferred from memory or from another
  product's vocabulary.

## 2. Evidence from deployed applications

### Codex

**Documented facts.** The public TUI source lists `Side` and `Btw`, describes
them as starting a side conversation in an “ephemeral fork,” allows them during
a task, and restricts the slash commands available inside the side. ([`slash_command.rs`](https://github.com/openai/codex/blob/main/codex-rs/tui/src/slash_command.rs))

The side module forks the current thread, injects a boundary saying that old
history, instructions, tool calls, and approvals are reference-only, and sends
the new message to the child. It tracks parent states such as needs input,
needs approval, failed, interrupted, closed, and finished. This supports
attention beside a parent task, although the source does not expose the server
scheduler's exact concurrency guarantee. ([`side.rs`](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/side.rs))

The child configuration is marked ephemeral. Closing it interrupts an active
turn and unsubscribes it; background cleanup is attempted when navigation moves
away. Failed cleanup remains visible and reports an error. Fork/bootstrap errors
restore the pending user message, including a distinct error when no initial
conversation exists. ([`side.rs`](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/side.rs))

**Evidence gap.** The public TUI source does not say whether provider-side
rollout data is physically deleted, retained for telemetry, or recoverable from
a session browser. It also does not prove that a parent model request always
continues rather than merely retaining a status while the child is active.

### Claude Code

The requested official sources are [Interactive mode](https://code.claude.com/docs/en/interactive-mode)
and [Common workflows](https://docs.anthropic.com/en/docs/claude-code/common-workflows).

**Access record (2026-08-04).** IPv4 requests to `code.claude.com`, including
the interactive-mode page and alternate path forms, timed out during
connection. The Anthropic URL returned an HTTP redirect to
`https://code.claude.com/docs/en/common-workflows`; the target also timed out.
No page body was available.

**Evidence gap.** There is no verified Claude fact here for side/btw entry,
primary-turn interruption, context inheritance, durable-ledger inclusion,
tools/writes, cleanup, or error handling.

### OpenCode

**Documented facts.** OpenCode's official TUI catalog documents `/new` for a new
session and `/sessions` (aliases `/resume`, `/continue`) for listing/switching
sessions. It also documents `!` shell commands whose output is added to the
conversation, plus export/share controls. ([OpenCode TUI](https://opencode.ai/docs/tui/),
[`tui.mdx`](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/tui.mdx))

Its permission model has `allow`, `ask`, and `deny`; `edit` covers file
modifications, and rules can be overridden per agent. The documented review
agent example uses `edit: deny` and `bash: ask`, showing that a low-side-effect
profile is expressible independently of a conversation label. ([OpenCode permissions](https://opencode.ai/docs/permissions/),
[`permissions.mdx`](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/permissions.mdx))

**Evidence gap.** The inspected catalog does not list `/side` or `/btw`, but
that omission cannot prove nonexistence. No inspected source specifies a
reference-only child, whether the primary turn continues, side-specific ledger
exclusion, cleanup, or side-fork error handling.

### Gemini CLI

**Documented facts.** Gemini CLI automatically saves the complete conversation,
tool executions, token usage, and available reasoning summaries; its
documentation says this background save preserves work even after an
interruption. It provides `--resume`/`-r`, an interactive `/resume` browser,
named checkpoints, deletion, and automatic retention cleanup (default 30 days).
When a session turn limit is reached, interactive mode stops sending requests
and requires a new session. ([Gemini session management](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/session-management.md))

The CLI reference documents `--sandbox`, approval modes (`default`,
`auto_edit`, `yolo`, `plan`), and `--worktree` for a separate Git worktree.
These are session/approval isolation controls, not evidence of a side/btw
context contract. ([Gemini CLI reference](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/cli-reference.md))

**Evidence gap.** The inspected official material does not document a temporary
side child, reference-only inheritance, primary-turn concurrency, side-specific
read-only permissions, or exclusion from automatic session saving.

## 3. Control-point comparison

| Control point | Codex | OpenCode | Gemini CLI | Claude Code |
| --- | --- | --- | --- | --- |
| Entry / primary turn | **Fact:** `/side` and `/btw` are available during a task; exact scheduler behavior is unknown. | **Fact:** new and switch-session commands; side concurrency undocumented. | **Fact:** resume/worktree options; side concurrency undocumented. | **Gap:** official pages unavailable. |
| Context | **Fact:** forked history is reference-only after a boundary. | **Gap:** no side inheritance contract found. | **Gap:** no side inheritance contract found. | **Gap:** unavailable. |
| Main ledger / lifetime | **Fact:** child marked ephemeral and unsubscribed/discarded on close; provider retention unknown. | **Gap:** session navigation is documented, temporary-child retention is not. | **Fact:** normal sessions auto-save completely and retain by policy; side exclusion unknown. | **Gap:** unavailable. |
| Tools / writes | **Fact:** side policy prohibits mutation/escalation/sub-agents unless explicitly requested; current permissions still matter. | **Fact:** `allow`/`ask`/`deny`, including `edit: deny`; no side profile. | **Fact:** sandbox and approval modes; no side profile. | **Gap:** unavailable. |
| Cleanup / errors | **Fact:** interrupt, unsubscribe, visible cleanup failure, restored input on fork failure. | **Gap:** no side flow found; generic attention events are documented. | **Fact:** retention and turn-limit outcomes; no side-fork flow. | **Gap:** unavailable. |

## 4. Cross-product synthesis and narrow boundary

**Cross-product synthesis:** A useful side/btw contract answers seven questions:

1. What explicit action starts it, and when is entry rejected?
2. Does the primary turn continue, pause, queue, or cancel?
3. Which inherited messages are reference-only, and which old instructions or
   approvals are inactive?
4. Which read, write, shell, network, sub-agent, and escalation operations are
   allowed?
5. Does the exchange join the primary ledger, a separate durable ledger, an
   ephemeral server object, or no recoverable ledger?
6. What interrupts and cleans up the child, and what is visible if cleanup
   fails?
7. Can the user recover the unsent question and see parent/child status after a
   fork, submission, or transport error?

The smallest product-neutral slice supported by the evidence is: explicit
side/btw entry; inherited context marked reference-only; no implicit primary
turn cancellation; a read-only or low-side-effect tool profile; no accidental
primary-ledger merge; deterministic return/cleanup; and visible errors. This
research boundary deliberately excludes durable branch browsing, nested sides,
and general sub-agent orchestration. It is a synthesis, not a local change
plan.

## 5. Pitfalls and evidence gaps

- A missing command in public documentation is not proof of missing behavior.
- “Ephemeral” does not prove provider-side deletion or zero telemetry retention.
- Prompt restrictions are weaker than hard tool permissions; a side label alone
  cannot guarantee read-only behavior.
- A child thread and a parent-status indicator do not prove simultaneous model
  execution without a scheduler contract or runtime observation.
- `/new`, `/resume`, checkpoints, and worktrees solve adjacent persistence or
  isolation problems; they are not automatically side conversations.
- Claude remains an evidence gap because its official pages were unreachable on
  the research date; recheck the two official URLs before using it as evidence.
- The requested Codex `codex-rs/tui/src/app/slash_command.rs` path returned 404;
  current `main` places the file at `codex-rs/tui/src/slash_command.rs`.

## References

All links were accessed or attempted on 2026-08-04.

1. OpenAI Codex, [side-thread source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app/side.rs).
2. OpenAI Codex, [slash-command source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/slash_command.rs).
3. Anthropic, [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode) — connection timeout.
4. Anthropic, [Claude Code common workflows](https://docs.anthropic.com/en/docs/claude-code/common-workflows) — redirected to `code.claude.com`, then timed out.
5. OpenCode, [TUI](https://opencode.ai/docs/tui/) and [permissions](https://opencode.ai/docs/permissions/) documentation.
6. OpenCode, public docs source [`tui.mdx`](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/tui.mdx) and [`permissions.mdx`](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/permissions.mdx).
7. Google Gemini CLI, [session management](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/session-management.md).
8. Google Gemini CLI, [CLI reference](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/cli-reference.md).
