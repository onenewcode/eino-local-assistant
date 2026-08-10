# Agent CLI session display-name selection: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-10; revalidated on 2026-08-11 before deletion-selector adoption.
>
> Scope: display names as an ergonomic session selector in deployed coding-agent CLIs, including identity precedence, ambiguity, and lifecycle boundaries.
>
> Out of scope: fuzzy search/picker ranking, cloud account identity, and private session database schemas.

## 1. Conclusions

- **Synthesis:** Display names improve human session recall but cannot replace stable machine identity. A command must retain an unambiguous ID path for scripts, incidents, and duplicate-name recovery.
- **Synthesis:** Exact matching and explicit ambiguity failure are safer than fuzzy matching for state-changing commands such as archive and delete.
- **Synthesis:** A name is useful only when it is durable and exposed in the session picker/listing context where users expect to retrieve it later.

## 2. Evidence from deployed applications

### Codex CLI

**Fact (locally installed product observation):** Codex CLI `0.146.0` reports `codex resume [SESSION_ID] [PROMPT]`. Its help says `SESSION_ID` accepts a session UUID or session name, that UUIDs take precedence when a selector parses as one, and that `--last` selects the most recent recorded session when no ID is supplied. Observed 2026-08-10. The public source project is [openai/codex](https://github.com/openai/codex), with the product reference at [Codex CLI reference](https://developers.openai.com/codex/cli/reference/).

**Fact (locally installed product observation):** The same Codex installation describes `archive` and `unarchive` selectors as "Session id (UUID) or session name. UUIDs take precedence if it parses." This shows name selection is used for both navigation and non-destructive lifecycle actions, not only initial chat creation. Observed 2026-08-10.

**Fact (locally installed product observation):** Codex CLI `0.146.0` now describes `codex delete <SESSION>` as deleting by ID or name, with the same UUID-precedence wording. The separate `--force` shortcut requires a UUID. Revalidated 2026-08-11.

### Claude Code

**Fact (locally installed product observation):** Claude Code `2.1.220` describes `-n, --name <name>` as setting a display name shown in the prompt box, `/resume` picker, and terminal title. Its `--resume [value]` help accepts a session ID or opens an interactive picker with an optional search term. Observed 2026-08-10; official reference: [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/).

## 3. Mechanisms and tradeoffs

| Mechanism | User-visible behavior | Tradeoff |
| --- | --- | --- |
| Durable display name | A familiar label survives restart and appears in resume affordances | Names can collide and be renamed |
| ID precedence | Scripts retain a stable target even if a name changes | A string that looks like a valid ID cannot be forced to mean a name |
| Exact full-name match | State-changing commands avoid surprising nearest-match actions | Users must type/copy the complete name |
| Ambiguity error with IDs | Users can recover by selecting a concrete target | Duplicate titles remain allowed instead of being rejected at creation |

## 4. Cross-product synthesis

**Synthesis:** Codex makes ID/name selection part of session lifecycle commands, while Claude Code makes display names visible in resume-oriented UI. The common reliable shape is not fuzzy global search: it is a durable human label combined with a stable ID escape hatch. A minimal CLI can implement the same safety boundary without reproducing a full picker.

## 5. Pitfalls and evidence gaps

- **Pitfall:** Substring or case-folded matching can change a destructive command's target after another session is created or renamed.
- **Pitfall:** Making names globally unique breaks existing free-form title behavior and does not solve conflicts with historic sessions; selection-time ambiguity is more compatible.
- **Pitfall:** Letting an archived name silently select an active-only resume path obscures why a normal resume fails. Selector scope should be explicit.
- **Evidence gap:** Product help does not define title normalization, duplicate display behavior, remote-session synchronization, or picker search ranking. Those semantics must not be inferred from the advertised name selector alone.

## References

- Codex CLI `0.146.0`: local `codex resume --help`, `codex archive --help`, `codex unarchive --help`, and `codex delete --help`, observed 2026-08-10 and revalidated 2026-08-11; [Codex CLI reference](https://developers.openai.com/codex/cli/reference/) and [openai/codex](https://github.com/openai/codex).
- Claude Code `2.1.220`: local `claude --help`, observed 2026-08-10; [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/).
