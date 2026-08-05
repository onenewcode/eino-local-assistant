# Agent 分支与 workspace 恢复边界：行业实践

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Re-verify before adoption; product behavior and
> documentation change.
>
> Decision surface: how an AI coding application couples conversation branching,
> prompt re-proposal, and workspace/file restoration when a user wants to try
> another path.
>
> Scope: publicly documented behavior of deployed AI coding applications and
> inspectable product sources. Out of scope: this repository's implementation,
> private internals, and claims that files, provider state, or external effects
> form one atomic transaction.

## 1. Conclusions

- **Cross-product synthesis:** “try another prompt” and “restore the workspace”
  are separate dimensions. Cline and Roo Code expose file-only, task/history-only,
  and combined restoration choices; Codex exposes a source-preserving prompt
  branch; neither vocabulary should be treated as a universal rewind contract.
- **Documented fact:** Cline checkpoints after tool use in a shadow Git repository
  and lets the user restore files, the task history, or both. Its documentation
  says checkpoints include files outside the project's normal Git tracking and
  persist across editor sessions. ([Cline checkpoints](https://docs.cline.bot/core-workflows/checkpoints))
- **Documented fact:** Roo Code creates task-scoped shadow-Git checkpoints before
  file modifications, but explicitly says checkpoints are not automatically made
  before command execution. Its “Restore Files & Task” path requires confirmation
  and its documentation warns that restoration overwrites unsaved workspace work.
  ([Roo Code checkpoints](https://roocodeinc.github.io/Roo-Code/features/checkpoints/))
- **Documented fact:** Aider uses the project's Git history rather than a separate
  conversation checkpoint model: it commits AI edits, commits pre-existing dirty
  files before editing by default, and exposes `/undo` for the last AI change.
  ([Aider Git integration](https://aider.chat/docs/git.html))
- **Cross-product synthesis:** checkpoint timing is a product choice with a real
  recovery consequence. Before-mutation snapshots (Roo Code and Gemini) protect
  the next mutation; after-tool snapshots (Cline) provide a visible state after
  each action; Git commit boundaries (Aider and OpenCode) depend on repository
  state and do not by themselves define conversation semantics.
- **Evidence gap:** None of these public contracts establish rollback of provider
  usage, network/database writes, credentials, permissions, or already-running
  processes. A file snapshot is not an external-side-effect transaction.

## 2. Evidence from deployed applications

### Cline: three explicit restore scopes

**Documented facts.** Cline says a checkpoint is saved whenever it modifies a
file or runs a command, using a shadow Git repository separate from the project's
main Git history. The documentation says the snapshot includes the complete file
state, including files not tracked by the project's Git repository, and persists
across editor sessions. ([Cline checkpoints](https://docs.cline.bot/core-workflows/checkpoints))

The restore menu separates three actions:

| Action | Documented effect |
| --- | --- |
| Restore Files | Revert project files while keeping the conversation. |
| Restore Task Only | Delete messages after the checkpoint without changing files. |
| Restore Files & Task | Revert files and delete later messages. |

Cline also documents a message-editing flow where “Restore All” restores the
corresponding file checkpoint before resubmitting the edited message. This makes
prompt editing an explicit file-and-conversation transition, rather than silently
assuming that an edited message should reuse the current workspace.

**Not disclosed.** The page does not define rollback for network requests,
provider accounting, arbitrary process state, or command-side effects beyond the
documented project-file snapshot.

### Roo Code: before-mutation task checkpoints with overwrite warnings

**Documented facts.** Roo Code describes automatic checkpoints as task-scoped
snapshots in a shadow Git repository. It records a task-start checkpoint and
creates regular checkpoints before file modifications; the page explicitly says
it does not automatically create them before command execution. ([Roo Code checkpoints](https://roocodeinc.github.io/Roo-Code/features/checkpoints/))

The product offers “Restore Files Only” for changing code while preserving chat,
and “Restore Files & Task” for resetting both files and subsequent conversation.
The combined action requires confirmation because the page says it cannot be
undone. The same page warns that restoration overwrites unsaved workspace changes,
and says checkpoints only capture changes made during the active task.

The documentation also distinguishes checkpoint tracking from AI file access:
`.rooignore` controls what the AI can access, while checkpoint exclusions follow
Git-related rules such as `.gitignore`; a file may therefore be inaccessible to
the AI and still be included in a checkpoint.

**Boundary.** Roo Code's “before file modification” guarantee does not establish
that a command's process, network effects, or files changed indirectly by a
command can be restored. The documentation only supports the stated checkpoint
scope.

### Aider: Git history as a code-only recovery surface

**Documented facts.** Aider recommends a Git repository, commits each AI edit
with a descriptive message, and provides `/undo` to discard the last change. It
also documents a dirty-file policy: before editing files with pre-existing
uncommitted changes, Aider commits those changes first by default so the user's
edits remain separate from Aider's edits. ([Aider Git integration](https://aider.chat/docs/git.html))

Aider also exposes `/diff`, `/commit`, and `/git`, and says users can manage the
Git history outside Aider. This is a code-history integration, not evidence of a
conversation checkpoint that can independently delete or restore messages.

**Tradeoff.** Committing a dirty baseline preserves it in Git, but changes the
project's visible history and relies on Git repository availability. The
documented `/undo` scope is the last AI code change; it does not claim to undo
provider calls, shell side effects, or arbitrary task state.

### OpenCode: message undo coupled to Git-backed file restoration

**Documented facts.** OpenCode documents `/undo` as removing the latest user
message and later responses while also removing file changes, and `/redo` as
restoring the undone message and file changes. Both operations use Git internally
and require a Git repository. ([OpenCode TUI](https://opencode.ai/docs/tui/))

This is a deliberately coupled model: conversation and file state move together
for the user-facing action. The same command catalog lists session switching
separately, so session navigation is not presented as undo/redo.

**Not disclosed.** The public page does not specify the full dirty-worktree policy,
untracked-file coverage, command/network side effects, or concurrent-turn behavior.

### Gemini CLI: pre-tool checkpoint plus tool-call re-proposal

**Documented facts.** Gemini CLI documents an opt-in checkpoint taken before an
approved filesystem-modifying tool. It stores a project snapshot in a shadow Git
repository along with conversation and pending tool-call state. `/restore` restores
the files and conversation, then re-proposes the original tool call so the user
can edit, rerun, or skip it. ([Gemini checkpointing](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/checkpointing.md))

This is neither a simple code undo nor a source-preserving branch: it is a
pre-mutation retry boundary that keeps the original tool intent available.
The feature is documented as opt-in, and its file/conversation scope does not
establish rollback for external effects.

### Codex: source-preserving branch rather than file restore

**Documented/source facts.** Codex's public TUI source describes selecting a
previous prompt, forking before it, and reopening that prompt for editing. The
public rollback contract separately says history pruning does not revert local
file changes; the current rollback schema is deprecated. ([Codex backtrack source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app_backtrack.rs), [Codex rollback contract](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadRollbackParams.json))

The evidence supports a source-preserving conversation branch, not automatic
workspace restoration. That distinction is useful precisely because “fork” can
preserve the old history while leaving the current workspace shared or unchanged.

### Claude Code

The official Claude Code pages selected for this direction were not accessible
from this environment on 2026-08-04: `code.claude.com` connections timed out,
and the older Anthropic URLs redirected to that host. No checkpoint or rewind
behavior is inferred from remembered command names or another product's behavior.

## 3. Control-point comparison

| Control point | Cline | Roo Code | Aider | OpenCode | Gemini CLI | Codex |
| --- | --- | --- | --- | --- | --- | --- |
| Snapshot boundary | After each tool use. | Task start and before file modifications; not automatically before commands. | Git commit per AI edit; dirty baseline committed first by default. | Git-backed undo/redo boundary. | Before approved filesystem mutation. | Conversation branch before selected prompt. |
| File-only recovery | Fact: Restore Files. | Fact: Restore Files Only. | Fact: `/undo` targets last AI code change. | Fact: undo/redo couples files to message. | Fact: restore files with checkpoint. | Gap for TUI branch; rollback explicitly leaves files to client. |
| History/task-only recovery | Fact: Restore Task Only. | Fact: task reset is offered as combined restore; separate file-only path is documented. | Not established by Git page. | Fact: undo removes message and later responses. | Fact: conversation is restored with checkpoint. | Fact: fork preserves source and reopens prompt in child. |
| Combined recovery | Fact: Restore Files & Task. | Fact: Restore Files & Task, confirmation required. | Not a documented conversation operation. | Fact: undo/redo couples both. | Fact: file + conversation + tool re-proposal. | Not established; fork is source-preserving. |
| Dirty/unsaved policy | Not fully disclosed on page. | Fact: restoration overwrites unsaved work. | Fact: pre-existing dirty files committed first by default. | Gap. | Gap for arbitrary dirty state. | Gap for workspace state. |
| External effects | Gap. | Gap. | Gap. | Gap. | Gap. | Gap. |

## 4. Cross-product synthesis

The evidence supports five distinct user contracts:

1. **Code snapshot:** restore files while preserving the conversation. Cline and
   Roo Code make this an explicit action; Aider's `/undo` is a narrower Git-code
   version.
2. **Conversation/task rewind:** delete or hide later messages while preserving
   current files. Cline documents this as Restore Task Only; OpenCode instead
   couples message undo with file restoration.
3. **Combined reset:** restore both files and conversation. Cline, Roo Code,
   OpenCode, and Gemini document variants, with different confirmation and retry
   behavior.
4. **Prompt re-proposal:** restore a pre-mutation state and present the original
   tool call or prompt for editing. Gemini does this for a pending tool call;
   Codex does it as a source-preserving prompt branch.
5. **Code-history undo:** use Git commits as the primary safety boundary. Aider
   makes that boundary visible and user-manageable, while OpenCode uses Git as an
   implementation mechanism for a higher-level conversation action.

These mechanisms are not interchangeable. A product that offers a source branch
may intentionally leave the workspace untouched; a product that restores files
may intentionally preserve the conversation; a pre-tool checkpoint may preserve
enough state to retry but still cannot prove that the first attempt had no
partial external effect.

## 5. Pitfalls and evidence gaps

- **Dirty workspace ambiguity:** “restore” can overwrite user edits. Roo Code
  states this directly; Aider's dirty-baseline commit is a different tradeoff,
  not a universal solution.
- **Command boundary ambiguity:** Roo Code and Gemini distinguish filesystem
  mutation from command execution. A shadow Git repository cannot by itself
  prove that a command's process, generated files outside the tracked scope, or
  network effects are reversible.
- **Conversation/file mismatch:** task-only restore can leave current files that
  no longer match the model's visible history; file-only restore can leave history
  describing changes that are no longer present. The UI should name the selected
  scope rather than use a generic “undo.”
- **Checkpoint granularity:** per-tool snapshots improve recovery precision but
  increase storage and latency; per-turn or per-prompt snapshots reduce overhead
  but enlarge the blast radius of a restore.
- **Branch identity is not workspace identity:** a new conversation lineage may
  share a workspace, restore a snapshot, or use an isolated checkout. Product
  documentation must state which one applies.
- **Retry is not rollback:** Gemini's re-proposed tool call is a new attempt
  boundary. It does not establish that the original tool had no partial effect.
- **Evidence gap:** the inspected public sources do not establish billing,
  provider retention, permissions, running processes, database writes, or network
  calls as rewindable state. Claude Code remains inaccessible for this research
  date.

## References

All links were accessed or attempted on 2026-08-04.

1. Cline, [Checkpoints](https://docs.cline.bot/core-workflows/checkpoints).
2. Roo Code, [Checkpoints](https://roocodeinc.github.io/Roo-Code/features/checkpoints/).
3. Aider, [Git integration](https://aider.chat/docs/git.html).
4. OpenCode, [TUI command documentation](https://opencode.ai/docs/tui/).
5. Google Gemini CLI, [Checkpointing](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/checkpointing.md).
6. OpenAI Codex, [TUI backtrack source](https://github.com/openai/codex/blob/main/codex-rs/tui/src/app_backtrack.rs) and [thread rollback contract](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/schema/json/v2/ThreadRollbackParams.json).
7. Anthropic, [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode) and [common workflows](https://code.claude.com/docs/en/common-workflows) — no official response body was accessible on the research date.
