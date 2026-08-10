# Agent CLI session archive: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-10; re-verify before adoption.
>
> Scope: non-destructive saved-session archiving from coding-agent CLIs, including selectors, recovery boundaries, and user-visible lifecycle state.
>
> Out of scope: retention policy, server-side deletion, cloud synchronization, and unpublished session-file internals.

## 1. Conclusions

- **Synthesis:** Archive and permanent delete solve different user problems. Archive should retain recoverable session data and offer a reversible state transition; delete needs a separate, unmistakably destructive path.
- **Synthesis:** A shell-level archive lifecycle needs a stable session selector and a visible inverse operation. Hiding sessions without an explicit unarchive control turns organization into accidental loss.
- **Synthesis:** A session that might still append model or compaction state is not a safe archive target. The archive transition must observe durable activity under the same serialization boundary as writers.

## 2. Evidence from deployed applications

### Codex CLI

**Fact (locally installed product observation):** Codex CLI `0.146.0` reports `codex archive [OPTIONS] <SESSION>` and describes it as "Archive a saved session by id or session name." `codex unarchive [OPTIONS] <SESSION>` is the symmetric command and describes restoring a saved session by ID or name. Both help surfaces state that UUIDs take precedence when a selector parses as a UUID. Observed 2026-08-10.

**Fact (locally installed product observation):** `codex --help` groups `archive`, `unarchive`, `delete`, `fork`, and `resume` among session lifecycle commands. This makes the non-destructive lifecycle discoverable independently from a TUI and distinguishes it from permanent deletion. Observed 2026-08-10. The public source project is [openai/codex](https://github.com/openai/codex), and the product reference is [Codex CLI reference](https://developers.openai.com/codex/cli/reference/).

### OpenCode and Claude Code

**Fact:** OpenCode's current CLI documentation documents session continuation and `--fork`; the accessible portion did not establish an archive/unarchive command, so it is not evidence for archive semantics. Source: [OpenCode CLI documentation](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/cli.mdx), accessed 2026-08-10.

**Evidence gap:** The locally installed Claude Code `2.1.220` help documents resume, continue, display names, and fork-on-resume, but this observation did not establish a permanent or non-destructive shell archive command. No claim is made about possible UI-only or remote lifecycle controls.

## 3. Mechanisms and tradeoffs

| Mechanism | User-visible behavior | Tradeoff |
| --- | --- | --- |
| Archive state in session metadata | Ordinary lists and recent-session selectors can omit inactive work | Readers must preserve an explicit archived view |
| Separate unarchive command | Recovery is discoverable and scriptable | Adds another lifecycle command to document and test |
| Durable state transition | Crash/restart still sees the same archival decision | Requires revision/conflict handling rather than a transient UI filter |
| Reject live work | Avoids hiding a session that a writer or provider operation may still change | User must first recover or finish interrupted work |

## 4. Cross-product synthesis

**Synthesis:** The observable Codex command surface treats archive as a reversible lifecycle operation alongside delete, not as an export format or a hidden UI preference. A minimal dependable implementation therefore needs: an explicit selector, an archived-state view, normal selectors that avoid archived sessions, a symmetric restore action, and clear behavior for sessions that cannot be considered inert.

## 5. Pitfalls and evidence gaps

- **Pitfall:** Treating archive as filesystem deletion loses the reversible contract users expect from an archive command.
- **Pitfall:** Selecting "latest" implicitly for archive is race-prone: the item can change after the user inspected a list. A stable explicit selector is safer for state-changing shell actions.
- **Pitfall:** Allowing a currently active session to become hidden can split user expectations from ongoing provider/tool lifecycle writes.
- **Evidence gap:** The direct Codex help does not disclose on-disk event schema, catalog rebuilding, cancellation behavior, or server-side synchronization semantics; those must not be inferred from the command wording.

## References

- Codex CLI `0.146.0`: local `codex --help`, `codex archive --help`, and `codex unarchive --help`, observed 2026-08-10; [Codex CLI reference](https://developers.openai.com/codex/cli/reference/) and [openai/codex](https://github.com/openai/codex).
- OpenCode CLI docs on GitHub `dev`, [cli.mdx](https://github.com/anomalyco/opencode/blob/dev/packages/web/src/content/docs/cli.mdx), accessed 2026-08-10.
- Claude Code `2.1.220`: local `claude --help`, observed 2026-08-10; [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/).
