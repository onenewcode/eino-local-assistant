# Headless resume scope: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. Session selection rules are version-sensitive;
> re-check the cited products before treating them as compatibility contracts.
>
> Scope: how deployed coding-agent CLIs bound `most recent` session selection
> to a directory, project, repository, or worktree, and how they expose an
> explicit opt-out such as `--all`.
>
> Out of scope: transcript schemas, model context reconstruction, concurrent
> writer guarantees, and the storage design of any particular repository.

## 1. Conclusions

- **Documented fact.** Codex CLI 0.146.0 exposes `--all` on `exec resume` and
  describes it as showing all sessions while disabling cwd filtering. Its
  default `--last` therefore has a narrower implicit scope than `--all`.
  [O1]
- **Documented fact.** Claude Code's `--continue` is explicitly the most recent
  conversation in the current directory. Its `--resume` path is an explicit
  conversation selector, and its separate `--fork-session` changes identity
  rather than selection scope. [C1][C2]
- **Documented fact.** Gemini CLI's `--list-sessions` is scoped to the current
  project, while `--resume` accepts `latest` or a numeric index. Its current
  top-level help does not expose an all-project selector. [G1][G2]
- **Cross-product synthesis.** A `most recent` shortcut is only reproducible
  when its implicit scope is part of the contract. An opt-out that broadens the
  candidate set is safe only when each session carries enough scope metadata to
  explain and intentionally select the broader set.
- **Evidence gap.** The observed help does not fully define how symlinked paths,
  nested repositories, detached worktrees, renamed directories, or equal
  timestamps affect candidate ordering. Those edge rules should not be inferred
  from the flag names.

## 2. Evidence from deployed applications

### 2.1 Codex CLI 0.146.0

**Observed shipped behavior.** On 2026-08-04, the installed `codex-cli 0.146.0`
binary reported:

- `codex exec resume [SESSION_ID] [PROMPT]` accepts an explicit UUID or thread
  name; when no ID is supplied, `--last` selects the most recent recorded
  session.
- `--all` is described as "Show all sessions (disables cwd filtering)."
- `--last` is described as resuming the most recent recorded session without an
  ID; the help does not state the complete definition of the default cwd
  filter, nor does it promise a stable project/worktree identity field.

The same help surface includes `--ephemeral`, but that is a persistence
control, not a scope selector. [O1]

### 2.2 Claude Code 2.1.212

**Observed shipped behavior.** On 2026-08-04, the installed `2.1.212 (Claude
Code)` binary reported:

- `--continue` continues the most recent conversation in the current
  directory.
- `--resume` resumes by session ID or opens a picker with an optional search
  term.
- `--fork-session` creates a new session ID when resuming; it does not broaden
  the candidate set.
- `--worktree` creates a separate git worktree for a session.

Claude's public session documentation additionally describes project and
repository-worktree boundaries for session lookup. That is a product-specific
scope rule, not a generic meaning of "current directory." [C1][C2]

### 2.3 Gemini CLI 0.44.1

**Observed shipped behavior.** On 2026-08-04, the installed `0.44.1` binary
reported:

- `--list-sessions` lists available sessions for the current project.
- `--resume latest` chooses the most recent session, while a numeric value such
  as `--resume 5` chooses a list index.
- `--session-file` loads a session from a JSON file.
- `--worktree` starts a session in a new git worktree.
- No `--all` or equivalent cross-project session-listing option appears in the
  current top-level help.

The help does not define whether a worktree is its own project scope for
session lookup or how an imported session file is assigned to a project.
[G1][G2]

## 3. Mechanisms and tradeoffs

| Selection mode | Candidate scope | Identity stability | Main tradeoff |
| --- | --- | --- | --- |
| Explicit session ID | Product-defined lookup boundary | Highest when the ID is opaque and durable | The caller must retain the ID and understand where it is valid |
| `most recent` / `latest` | Implicit cwd, project, or picker scope | Low under parallel work | Convenient, but can select a different task after another session updates |
| Broad `--all` selection | Multiple scopes, often with visible scope metadata | Depends on the chosen ordering | Discoverability improves, but accidental cross-project resume becomes easier |
| Numeric list index | Current listing scope and sort order | Low | Simple for a human picker; unstable for automation |

The products expose different boundaries because a directory can be a project
root, a repository checkout, or merely the process cwd. A broad selector is not
just a larger `ListThreads` call: it changes the safety meaning of an omitted
identity. A useful implementation therefore needs to make the scope visible in
the listing or require an explicit confirmation/selector before crossing it.

## 4. Cross-product synthesis

- **Scope and identity are separate axes.** Codex's `--all` broadens discovery;
  Claude's `--fork-session` changes the destination identity; Gemini's numeric
  index changes the reference's stability. Similar resume vocabulary hides
  different axes.
- **Default shortcuts are intentionally narrow.** Codex, Claude, and Gemini all
  provide a recent-session convenience, but each binds it to a product-specific
  local scope. This reduces accidental selection at the cost of requiring a
  deliberate broadening step.
- **Explicit IDs do not erase scope.** A durable ID may still be valid only in a
  project or repository namespace. Products differ on whether they search by
  ID outside that namespace, and public help is not enough to assume one rule.
- **A broad selector needs explainability.** At minimum, an operator should be
  able to see which directory/project/worktree caused a candidate to win. None
  of the three help surfaces alone specifies a complete cross-product listing
  schema, so that visibility is an evidence-backed design principle rather than
  a universal field contract.

## 5. Pitfalls and evidence gaps

- **`--all` is not a synonym for "continue."** Codex uses it to alter filtering;
  Claude's `--continue` remains directory-bound and Gemini has no reviewed
  all-project equivalent.
- **Current cwd is not necessarily project scope.** Claude documents a
  repository/worktree relationship, while Codex's help only names cwd
  filtering and Gemini names the current project. Do not transfer one product's
  scope algorithm to another.
- **Indexes are not durable identities.** Gemini's numeric resume value depends
  on the current list and sort. The same warning applies to any future picker
  that exposes ordinal positions.
- **Ordering is under-specified.** The observed interfaces do not settle tie
  handling, clock skew, interrupted sessions, archived sessions, or concurrent
  updates. A caller should not treat "newest" as a reproducible task key.
- **Cross-scope resume can be a security boundary.** A broad selector may expose
  context or credentials from another project. The product must define which
  configuration, permissions, and workspace are reattached; help text alone
  does not establish that behavior.

## References

All sources accessed 2026-08-04.

- **[O1] Local product observation:** `codex --version`, `codex exec resume
  --help`; observed version `codex-cli 0.146.0`. This is a reproducible
  observation of the installed binary, not a public storage schema.
- **[C1] Local product observation:** `claude --version`, `claude --help`;
  observed version `2.1.212 (Claude Code)`. Canonical reference:
  https://code.claude.com/docs/en/cli-reference
- **[C2] Anthropic, "Manage sessions"** (official Claude Code documentation):
  https://code.claude.com/docs/en/sessions
- **[G1] Local product observation:** `gemini --version`, `gemini --help`;
  observed version `0.44.1`. Canonical references:
  https://geminicli.com/docs/cli/cli-reference/ and
  https://geminicli.com/docs/cli/headless/
- **[G2] Google, "Gemini CLI cheatsheet"** (official Gemini CLI documentation):
  https://geminicli.com/docs/cli/cli-reference/
