# 交互式 rewind / checkpoint 回滚：行业实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Re-verify before adoption because product behavior
> and source branches change.
>
> Decision surface: how an interactive agent lets a user revisit a prior prompt,
> restore conversation state, and/or revert agent-mediated workspace changes
> without confusing a history branch with a filesystem transaction.
>
> Scope: shipped or publicly documented terminal-agent behavior and inspectable
> product source. Out of scope: this repository's implementation, private
> internals, and claims about external side effects that the products do not
> disclose.

## 1. Conclusions

- **Cross-product synthesis:** "rewind" is not one operation. The evidence
  separates at least conversation-history rollback, workspace/file restoration,
  and source-preserving branch/fork. A product may combine them, but the user
  contract must name each boundary.
- **Documented fact:** OpenCode documents `/undo` as removing the latest user
  message and all later responses plus file changes, and `/redo` as restoring an
  undone message and file state through Git. Both require the project to be a
  Git repository. ([OpenCode TUI](https://opencode.ai/docs/tui/))
- **Documented fact:** Gemini CLI documents an opt-in checkpoint taken before
  an AI file-modification tool. The checkpoint stores a project snapshot in a
  shadow Git repository plus conversation/tool-call state; `/restore` restores
  files and conversation and re-proposes the original tool call. ([Gemini
  checkpointing](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/checkpointing.md))
- **Documented fact:** Codex's current TUI exposes backtracking as transcript
  selection followed by a source-preserving fork before the selected prompt,
  reopening that prompt for editing. Its public `thread/rollback` protocol
  explicitly says it only changes thread history and does not revert local file
  changes; the protocol marks that method deprecated. ([Codex backtrack
  source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app_backtrack.rs),
  [Codex rollback contract](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadRollbackParams.json))
- **Cross-product synthesis:** The safest user-visible model is an explicit
  state transition with a recoverable branch or checkpoint, a clear workspace
  baseline, and visible failure/re-proposal behavior. A prompt-only instruction
  to "undo" is weaker than a state boundary owned by Git or an equivalent
  snapshot mechanism.
- **Evidence gap:** None of the inspected sources establish rollback of
  provider usage/cost, external network or database side effects, permission
  grants, or arbitrary processes. Files and conversation can be restored while
  those effects remain.

## 2. Evidence from deployed applications

### OpenCode

**Documented facts.** The official TUI documentation defines `/undo` as undoing
the last conversation message: the most recent user message, all subsequent
responses, and any file changes are removed. It separately defines `/redo` as
available only after `/undo`, restoring the previously undone message and file
changes. The documentation says both operations use Git internally and require
the project to be a Git repository. ([OpenCode TUI](https://opencode.ai/docs/tui/))

The same command catalog presents `/sessions` as session listing/switching,
which is a different control from undo/redo. The documentation therefore gives
an explicit distinction between navigating sessions and rewinding the current
conversation. ([OpenCode TUI source](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/tui.mdx))

**Not disclosed.** The inspected documentation does not specify whether usage
accounting, provider-side state, shell/network side effects, untracked files, or
concurrent turns participate in the undo/redo transaction. Git is a file-state
mechanism, not evidence that those other effects are reversible.

### Gemini CLI

**Documented facts.** Gemini CLI documents automatic session saving for prompts,
responses, tool executions, token usage, and available reasoning summaries. It
also documents named chat checkpoints (`/resume save`, `/resume list`, and
`/resume resume`), which are history branch points rather than the file-change
restore flow. ([Gemini session management](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/session-management.md))

Its separate Checkpointing feature is disabled by default and enabled through
`settings.json`. Before an approved filesystem-modifying tool runs, the CLI
creates a project snapshot in a shadow Git repository under the user's home
directory, saves conversation history and the pending tool call, and exposes
`/restore <checkpoint_file>`. Restoring reverts project files and conversation
history, then re-proposes the original tool call so the user can rerun, edit, or
skip it. ([Gemini checkpointing](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/checkpointing.md))

**Boundary.** The docs describe a local project snapshot and conversation/tool
state. They do not claim that provider billing, external services, network
requests, or already-started processes are rolled back. The docs also make the
feature opt-in, which is a different tradeoff from OpenCode's Git prerequisite.

### Codex

**Documented/source facts.** The current Codex TUI's `app_backtrack` module
describes a two-stage interaction: the first `Esc` primes backtrack, the next
opens a transcript overlay, and `Enter` requests a fork before the selected user
prompt. The selected prompt is restored in the new branch's composer for editing.
The source rejects editing previous prompts while a side conversation is active
and restores the prompt with a visible error if branching fails. ([Codex
backtrack source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app_backtrack.rs))

The public app-server fork schema supports a new thread forked from a stored
thread, an optional inclusive last-turn boundary, and per-fork model,
instructions, approval, sandbox, and ephemeral settings. This is evidence of a
branching control surface, not proof that every option is exposed by the
terminal UI. ([Codex thread fork schema](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadForkParams.json))

The public rollback schema is deliberately narrower: it drops a number of turns
from the end of a thread and explicitly leaves local file changes to the client.
The current schema labels `thread/rollback` deprecated, and its test suite
checks that the pruned history persists after resume. ([Codex rollback schema](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadRollbackParams.json),
[Codex rollback tests](https://github.com/openai/codex/blob/main/codex-rs/app-server/tests/suite/v2/thread_rollback.rs))

**Boundary.** The inspected TUI source establishes source-preserving branching
and prompt recovery, but does not establish that the branch automatically
restores filesystem state. The rollback protocol explicitly does not do so.

### Claude Code

The official pages selected for this direction were
[Interactive mode](https://code.claude.com/docs/en/interactive-mode) and
[Common workflows](https://docs.anthropic.com/en/docs/claude-code/common-workflows).

**Access record (2026-08-04).** The interactive-mode URL timed out from the
research environment with no response body. The Anthropic URL redirected toward
`code.claude.com`, whose target was also unreachable. No Claude Code behavior is
inferred from memory, command-name similarity, or another product's docs.

**Evidence gap.** Rewind entry, conversation/file coupling, checkpoint storage,
dirty-worktree handling, redo behavior, and external-side-effect guarantees all
require re-verification from an accessible official source.

## 3. Control-point comparison

| Control point | OpenCode | Gemini CLI | Codex | Claude Code |
| --- | --- | --- | --- | --- |
| History action | **Fact:** `/undo` removes the latest user message and later responses; `/redo` restores it. | **Fact:** named chat checkpoints can be saved, listed, and resumed. | **Fact:** backtrack forks before a selected prompt and reopens it for editing; rollback drops turns and is deprecated. | **Gap:** official docs unavailable. |
| File state | **Fact:** undo/redo also use Git to revert/restore file changes. | **Fact:** opt-in pre-tool shadow-Git snapshot and `/restore`. | **Gap for TUI backtrack:** branch behavior does not establish file restoration; rollback explicitly does not restore files. | **Gap:** unavailable. |
| Branch identity | **Fact:** undo/redo operate on current conversation; sessions are separate controls. | **Fact:** named checkpoints are session history points; worktrees are documented as a parallel-task isolation tool. | **Fact:** selected-prompt editing creates a new source-preserving branch. | **Gap:** unavailable. |
| Failure/retry | **Gap:** docs do not specify dirty-state or restore failure UX. | **Fact:** restore re-proposes the original tool call for rerun/edit/skip. | **Fact:** branch failure restores the selected prompt and shows an error. | **Gap:** unavailable. |
| Accounting/side effects | **Gap:** no usage/external-side-effect rollback contract found. | **Gap:** no provider/external-side-effect rollback contract found. | **Fact:** rollback schema only promises history pruning; file rollback is client-owned. | **Gap:** unavailable. |

## 4. Cross-product synthesis

The evidence supports these distinctions:

1. **Conversation rewind:** remove or hide recent user/assistant history and
   continue from an earlier context boundary. Codex's deprecated rollback is the
   narrowest example; OpenCode couples this with file restoration.
2. **Workspace restore:** return files to a captured baseline. Gemini captures
   before a mutating tool and OpenCode relies on Git. Neither mechanism proves
   reversal of non-file side effects.
3. **Source-preserving branch:** create a new conversation lineage before an
   earlier prompt, preserving the old path while editing a new prompt. Codex's
   backtrack is the clearest example.
4. **Named checkpoint/resume:** mark a history point for later navigation. Gemini
   documents this separately from its file-change checkpointing.

These operations have different safety and UX costs. Destructive history/file
undo is convenient but needs a redo or recovery story. Branching preserves the
old path but creates session identity and workspace-consistency questions.
Pre-tool checkpoints make a mutation recoverable but add storage and may not
cover tools that act outside the project filesystem. Named history checkpoints
are cheap to browse but do not imply workspace rollback.

## 5. Pitfalls and evidence gaps

- A Git commit or shadow repository is a file-state boundary, not a transaction
  over network calls, databases, secrets, running processes, provider state, or
  billing.
- Reverting conversation without reverting files can cause the next model turn
  to reason from history that no longer describes the workspace; reverting files
  without pruning conversation can create the inverse mismatch.
- A dirty worktree needs an explicit policy: preserve user edits, refuse the
  operation, snapshot them, or offer a three-way restore. The inspected OpenCode
  and Codex sources do not disclose the complete policy.
- Restoring a pending tool call is a retry boundary, not proof that the original
  attempt had no partial side effects.
- A branch/fork must define whether it inherits the parent's approvals, sandbox,
  model, instructions, and workspace identity. Codex's fork schema exposes these
  as explicit configuration points; a UI label alone is not enough.
- Session usage, journal events, and provider-side retention were not documented
  as rewindable by any inspected source. Treat them as separate ledgers until a
  product states otherwise.
- Claude Code remains an evidence gap for this research date; do not use its
  absent or remembered command names as design evidence.

## References

All links were accessed or attempted on 2026-08-04.

1. OpenCode, [TUI command documentation](https://opencode.ai/docs/tui/) and
   [public TUI source](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/tui.mdx).
2. Google Gemini CLI, [checkpointing](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/checkpointing.md).
3. Google Gemini CLI, [session management](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/session-management.md).
4. OpenAI Codex, [TUI backtrack source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app_backtrack.rs).
5. OpenAI Codex, [thread fork schema](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadForkParams.json).
6. OpenAI Codex, [thread rollback schema](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadRollbackParams.json) and [rollback tests](https://github.com/openai/codex/blob/main/codex-rs/app-server/tests/suite/v2/thread_rollback.rs).
7. Anthropic, [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode) — connection timeout.
8. Anthropic, [Claude Code common workflows](https://docs.anthropic.com/en/docs/claude-code/common-workflows) — redirected target unavailable.
