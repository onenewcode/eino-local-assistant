# TUI session resume picker: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-11; re-verify before adoption.
> Scope: an in-terminal picker that switches an already-running coding-agent TUI to a saved conversation.
> Out of scope: terminal shell launchers, session storage, transcript search, cloud synchronization, and branch/fork creation.

## 1. Conclusions

- **Fact:** Codex CLI 0.146.0 documents `resume` as “picker by default” and accepts an optional session ID. `--last` selects the most recent session without showing the picker, and `--all` widens picker scope. [Observed in Codex CLI 0.146.0 `codex resume --help`, 2026-08-11]
- **Fact:** Claude Code 2.1.220 documents `--resume [value]` as either resuming a conversation by ID or opening an interactive picker with an optional search term. Its display-name option says the name appears in the `/resume` picker. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-11]
- **Fact:** OpenCode documents an explicit `--session` ID and a separate `--continue` option for the last session. [OpenCode CLI documentation, accessed 2026-08-11](https://opencode.ai/docs/cli/)
- **Synthesis:** An in-TUI resume picker should mirror the direct-ID command rather than create a parallel restore mechanism: list only selectable active sessions, preserve the composer draft on open/cancel, and perform the actual resume through the same runtime callback after confirmation.

## 2. Evidence from deployed applications

### Codex CLI

**Fact:** The observed Codex `resume` help says picker-by-default, describes direct IDs or session names, and makes `--last` an explicit non-picker action. It also offers `--all` to disable current-directory filtering and expose a location column. [Observed in Codex CLI 0.146.0 `codex resume --help`, 2026-08-11]

**Synthesis:** Visibility scope belongs to the picker contract. A default view should not silently include sessions that are excluded from ordinary resume semantics; broader scope needs a visible, explicit control.

### Claude Code

**Fact:** The observed Claude Code help states that `--resume` with no ID opens an interactive picker with an optional search term. It also documents a session display name that appears in the resume picker. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-11]

**Synthesis:** Search belongs inside a picker because users often remember a topic/title rather than a durable ID. It must filter visible rows without changing the active session until Enter confirms one row.

### OpenCode

**Fact:** OpenCode's documented CLI flags differentiate a specific `--session` from `--continue` for the last session. [OpenCode CLI documentation, accessed 2026-08-11](https://opencode.ai/docs/cli/)

**Synthesis:** A picker is an interaction mode, not an alias for “latest.” Canceling, blank search, or a failed open must retain the existing session instead of implicitly using recency.

## 3. Mechanisms and tradeoffs

- **Fact:** Codex exposes session-name selection as well as IDs in its resume help. [Observed in Codex CLI 0.146.0 `codex resume --help`, 2026-08-11]
- **Synthesis:** Each picker row should include a stable ID and human title plus lightweight recency/message metadata. Stable IDs make duplicated titles understandable without fuzzy matching being a write target.
- **Synthesis:** Read the picker list before entering the overlay; do not create a model call, fork, or recovery operation merely to browse. The selected ID then uses the established open/resume boundary, which remains responsible for active-turn and pending-compaction checks.
- **Synthesis:** While a picker is open, keyboard events should be captured by the overlay so typing edits the query rather than sends a normal prompt. Esc should close the overlay and restore the untouched draft.

## 4. Cross-product synthesis

The mature interaction model has three separate states:

1. Direct ID/name input immediately requests an existing conversation.
2. No selector opens a human-oriented, searchable candidate list.
3. Explicit recency chooses the newest valid session without a visual selection step.

**Synthesis:** The picker should share the direct resume path's state-replacement behavior and failure feedback. This gives one source of truth for model binding, frozen session prompts, interrupted-session recovery, and transcript replay.

## 5. Pitfalls and evidence gaps

- **Evidence gap:** Publicly observable help does not specify Codex or Claude's exact picker shortcut keys, row truncation, or behavior on storage read errors.
- **Evidence gap:** The observed OpenCode CLI documentation establishes selection/continue flags but not an interactive resume-overlay design.
- **Synthesis:** A missing or stale selected ID is a recoverable picker error, not a reason to switch to a nearby list row. Keep the overlay open so the user can search again or cancel.
- **Synthesis:** Archived sessions should stay absent from the ordinary picker unless the product offers an explicit archive scope, because they are not valid normal-resume targets.

## References

- Codex CLI 0.146.0, `codex resume --help`, observed locally on 2026-08-11.
- Claude Code CLI 2.1.220, `claude --help`, observed locally on 2026-08-11.
- [OpenCode CLI documentation](https://opencode.ai/docs/cli/), accessed 2026-08-11.
