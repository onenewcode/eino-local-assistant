# Headless ephemeral resume: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. CLI behavior and help text change; re-verify the
> cited versions before adopting any contract.
>
> Scope: how deployed coding-agent CLIs distinguish resuming durable context,
> forking a session, and running a turn without durable session files. The
> focus is headless or scriptable invocation and the observable persistence
> boundary.
>
> Out of scope: private transcript formats, rollback of tool side effects,
> undocumented concurrency guarantees, and any one repository's storage or
> implementation design.

## 1. Conclusions

- **Cross-product synthesis.** "Do not persist this run" and "fork this session"
  are separate concepts. Claude exposes both controls explicitly; Codex exposes
  `exec resume --ephemeral` for the former and a separate interactive `fork`
  command for the latter. An ephemeral resume flag alone does not establish a
  new durable session identity or prove that the source session is untouched.
  [O1][C1]
- **Documented fact.** Codex's current headless help defines `--ephemeral` as
  running without persisting session files to disk. It does not say whether a
  resumed source transcript is copied, appended in memory, or otherwise
  protected from mutation. [O1]
- **Documented fact.** Claude's `--fork-session` explicitly creates a new
  session ID when resuming. Its `--no-session-persistence` is a separate
  print-mode option and says sessions are not saved and cannot be resumed.
  [C1][C2]
- **Evidence gap.** Gemini's current CLI exposes `--resume` and
  `--session-file`, but its help exposes no equivalent `--ephemeral` or
  `--fork-session` control. The presence of resume therefore does not establish
  a non-persistent or branch-preserving mode. [G1]
- **Cross-product synthesis.** A useful automation contract must name at least
  the durable input source, the persistence policy for the resumed turn, the
  resulting session identity, and the fate of artifacts or external tool
  effects. Product help usually specifies only the first two partially.

## 2. Evidence from deployed applications

### 2.1 Codex CLI 0.146.0

**Observed shipped behavior.** On 2026-08-04, the installed `codex-cli 0.146.0`
binary reported the following relevant interface:

- `codex exec resume [SESSION_ID] [PROMPT]` accepts a session ID or `--last`.
- `codex exec resume --ephemeral` is documented as "Run without persisting
  session files to disk."
- The same headless command separately exposes `--json` and
  `--output-last-message`; no `--fork-session` option appears in its help.
- The interactive `codex fork` command is a separate command, described as
  forking a previous interactive session; its help says the new session starts
  from the selected session and may receive an optional prompt.

These are versioned observations of the executable's public CLI contract, not
claims about its internal storage operations. In particular, the help does not
define whether resumed ephemeral execution writes the source session, whether
it returns a new ID, or whether tool-created files are isolated. [O1]

### 2.2 Claude Code 2.1.212

**Observed shipped behavior.** On 2026-08-04, the installed `2.1.212 (Claude
Code)` binary reported:

- `--fork-session`: "When resuming, create a new session ID instead of reusing
  the original (use with --resume or --continue)."
- `--no-session-persistence`: "Disable session persistence - sessions will not
  be saved to disk and cannot be resumed (only works with --print)."
- `--resume` and `--continue` select a prior conversation, while
  `--output-format` distinguishes final JSON from streaming JSONL.

Claude therefore makes the identity boundary (fork) and the storage boundary
 (no persistence) independently selectable at the CLI surface. The help does
 not, by itself, state whether a fork copies every configuration field or how
 external tool effects are isolated. [C1]

### 2.3 Gemini CLI 0.44.1

**Observed shipped behavior.** On 2026-08-04, the installed `0.44.1` binary
reported:

- `--resume` resumes a previous session and accepts `latest` or a numeric
  session index in addition to an identifier.
- `--session-file` loads a session from a JSON file.
- `--list-sessions` lists sessions for the current project.
- `--output-format` supports `text`, `json`, and `stream-json`.
- No `--ephemeral`, `--fork-session`, or `--no-session-persistence` option
  appears in the current top-level help.

This is evidence that Gemini exposes resume and import-like session loading,
not evidence that either operation is non-persistent or branch-safe. The help
does not establish the write boundary for a resumed headless turn or the
relationship between a loaded session file and a newly persisted session. [G1]

## 3. Mechanisms and tradeoffs

| Control point | Ephemeral resume | Forked resume | Ordinary durable resume |
| --- | --- | --- | --- |
| Context source | Existing durable session | Existing durable session | Existing durable session |
| New durable identity | Not implied | Expected by products that expose an explicit fork | Usually reuses the source identity |
| Session-file writes | Suppressed by the documented ephemeral contract | Product-defined; often writes the branch | Expected to append or update the source |
| Tool/workspace effects | Not addressed by session-file suppression | Not necessarily isolated by session branching | Usually occur in the active workspace |
| Safe assumption without more evidence | Read context, but treat write behavior as unknown | New transcript identity, but treat copied state as unknown | Single-writer and failure boundaries remain unknown |

The important boundary is that session persistence is not the same resource as
the workspace or external systems. Suppressing a transcript write can make a
run undiscoverable without undoing a file edit, network request, or other tool
side effect. Conversely, creating a new transcript ID does not prove that its
working directory, permissions, model settings, or tool state are independent.

For a product-neutral contract, the invocation should make these transitions
observable:

1. resolve and read the source session;
2. choose a persistence mode for the new turn;
3. assign or explicitly decline a durable child identity;
4. execute tools in the selected workspace and report their result;
5. report whether the transcript and final result were durably committed.

The last step matters for retries: a non-zero exit or missing final output can
mean that a turn was neither committed nor safely replayable. Public help for
the products above does not promise idempotent replay of partially executed
tools.

## 4. Cross-product synthesis

- **Identity is a separate axis from persistence.** Claude's two flags make
  this explicit, and Codex's separate `fork` command points in the same
  direction. A caller should not infer "fork" from the word "ephemeral".
- **A source session and a child transcript need different ownership rules.** A
  fork can preserve conversation context while changing who owns future
  messages. An ephemeral run can preserve context only for the lifetime of the
  process while deliberately producing no durable child. The product must say
  which model it implements.
- **Session-file suppression is narrower than execution isolation.** None of
  the observed help text says that workspace changes, network calls, process
  trees, or credentials are reverted or sandboxed by ephemeral mode.
- **Explicit absence is useful evidence.** Gemini's lack of a non-persistence
  or fork flag means its documented resume surface should not be treated as a
  drop-in equivalent for either control. Similar names across CLIs do not form
  a common standard.

## 5. Pitfalls and evidence gaps

- **Source mutation is unspecified.** Codex's ephemeral help says session files
  are not persisted, but does not state whether the source file is opened
  read-only, copied to memory, or updated and later discarded.
- **Fork contents are unspecified.** Claude documents a new session ID, but the
  observed help does not enumerate which transcript metadata, permissions,
  scheduled work, or launch configuration is copied.
- **Tool effects are not transcript effects.** A no-persistence flag cannot be
  used as evidence that file edits or external requests did not happen.
- **In-memory failure still needs a result contract.** Products do not expose a
  universal answer for whether a failed ephemeral turn may be retried with the
  same source context without duplicating an already-started action.
- **Version drift is material.** The evidence here is from three installed
  binaries on one date; feature names and semantics must be rechecked before a
  compatibility claim is made.

## References

All sources accessed 2026-08-04.

- **[O1] Local product observation:** `codex --version`, `codex exec --help`,
  `codex exec resume --help`, and `codex fork --help`; observed version
  `codex-cli 0.146.0`. This is a reproducible observation of the installed
  binary, not a public storage schema.
- **[C1] Local product observation:** `claude --version` and `claude --help`;
  observed version `2.1.212 (Claude Code)`. Canonical reference:
  https://code.claude.com/docs/en/cli-reference
- **[C2] Anthropic, “Manage sessions”** (official Claude Code documentation):
  https://code.claude.com/docs/en/sessions
- **[G1] Local product observation:** `gemini --version` and `gemini --help`;
  observed version `0.44.1`. Canonical references:
  https://geminicli.com/docs/cli/cli-reference/ and
  https://geminicli.com/docs/cli/headless/
