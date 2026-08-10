# Interactive session picker: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-11; re-verify before adoption.
> Scope: starting or branching a saved coding-agent conversation from an interactive terminal when the caller does not provide a durable session identifier.
> Out of scope: transcript search, cloud-session synchronization, session storage format, and non-interactive automation.

## 1. Conclusions

- **Fact:** Codex CLI 0.146.0 documents `resume` and `fork` as “picker by default”; both accept an optional session identifier, and `--last` explicitly avoids the picker. Its `resume` command also distinguishes whether the picker should show all sessions and whether to include non-interactive sessions. [Observed in Codex CLI 0.146.0 `codex resume --help` and `codex fork --help`, 2026-08-11]
- **Fact:** Claude Code 2.1.220 documents `--resume [value]` as accepting a conversation ID or opening an interactive picker with an optional search term. It separately documents `--continue` for the most recent conversation in the current directory. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-11]
- **Fact:** OpenCode documents `--continue` as continuing the last session, `--session` as selecting one session ID, and `--fork` as creating a fork when paired with either continuation form. [OpenCode CLI documentation, accessed 2026-08-11](https://opencode.ai/docs/cli/)
- **Synthesis:** A terminal picker is the safe default for an omitted interactive selector. The unambiguous fast path should be an explicit “last” option, while scripts should keep using IDs rather than picker input.

## 2. Evidence from deployed applications

### Codex CLI

**Fact:** Codex CLI's `resume` usage is `codex resume [OPTIONS] [SESSION_ID] [PROMPT]` and its description says “picker by default; use --last to continue the most recent.” Its help states that a provided UUID or session name selects directly, while an omitted ID shows the picker unless `--last` is supplied. [Observed in Codex CLI 0.146.0 `codex resume --help`, 2026-08-11]

**Fact:** Codex CLI gives `fork` the same picker/default and `--last` bypass structure. This makes branching a prior conversation an explicit selection operation rather than assuming that a free-form argument is a new task prompt. [Observed in Codex CLI 0.146.0 `codex fork --help`, 2026-08-11]

### Claude Code

**Fact:** Claude Code's `--resume [value]` help says it resumes a conversation by ID or opens an interactive picker with an optional search term. Its display-name help says that names appear in the `/resume` picker. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-11]

**Fact:** Claude Code exposes `--continue` separately and scopes it to the most recent conversation in the current directory. The distinction avoids making “latest” an accidental consequence of entering no selector. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-11]

### OpenCode

**Fact:** OpenCode's CLI documentation lists `--continue` / `-c` for the last session, `--session` / `-s` for a session ID, and `--fork` for a fork of either selected continuation source. [OpenCode CLI documentation, accessed 2026-08-11](https://opencode.ai/docs/cli/)

## 3. Mechanisms and tradeoffs

- **Fact:** Codex exposes picker inclusion and scope switches in addition to direct IDs. This indicates that list membership is part of the picker contract, rather than an implementation detail. [Observed in Codex CLI 0.146.0 `codex resume --help`, 2026-08-11]
- **Synthesis:** A first picker should read durable session metadata without changing session state. It must not repair storage, start a model, create a fork, or recover an interrupted turn until the user has selected an entry.
- **Synthesis:** Present a stable ordinal list with title, ID, message count, and update time; accept only an ordinal plus an explicit cancel value. A direct ID stays available in the command syntax, avoiding ambiguous fuzzy matching in a scriptable interface.
- **Synthesis:** Limit the initial terminal list to a bounded recent set. Search and cross-workspace scope filters are useful later enhancements, but a huge unbounded prompt hides the cancellation and selection controls.

## 4. Cross-product synthesis

Interactive tools converge on three modes that should remain distinguishable:

1. A direct identifier gives automation and copy-paste a stable durable target.
2. An omitted identifier opens a human-oriented selection interface.
3. An explicit latest-session flag chooses recency without displaying a list.

**Synthesis:** The picker should list only sessions valid for the requested operation by default. Archived sessions, sessions that cannot resume, or unrelated retention classes need an explicit scope control rather than silently appearing among normal choices.

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The observable Codex help does not disclose picker keyboard handling, list truncation, or cancellation text; no hidden UI behavior is inferred.
- **Evidence gap:** The observable Claude Code help establishes picker entry but does not document how duplicate display names are rendered or resolved.
- **Synthesis:** Do not use an implicit latest session when input reaches EOF or is blank. Treat both as cancellation so redirected stdin cannot unexpectedly reopen or fork a durable conversation.
- **Synthesis:** A picker is inherently TTY-only. Headless commands require an explicit session selector or an explicit latest flag to remain deterministic.

## References

- Codex CLI 0.146.0, `codex resume --help` and `codex fork --help`, observed locally on 2026-08-11.
- Claude Code CLI 2.1.220, `claude --help`, observed locally on 2026-08-11.
- [OpenCode CLI documentation](https://opencode.ai/docs/cli/), accessed 2026-08-11.
