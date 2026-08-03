# Headless CLI execution: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-03. Re-verify before adopting; CLI behavior changes
> with releases.
>
> Scope: non-interactive invocation, stdin composition, machine-readable
> output, persistence, exit handling, and tool-approval controls for coding
> agents in scripts and CI.
>
> Out of scope: local-repository design, undocumented internal architecture,
> provider authentication, and the quality of agent-produced code.

## 1. Conclusions

- **Cross-product synthesis.** The three observed products expose an explicit
  non-interactive entry point and support a piped-input workflow. A CI caller
  should therefore treat its prompt and piped payload as separate inputs whose
  composition must be documented, rather than assuming stdin replaces a
  positional prompt. [O1][O2][S1][S3]
- **Cross-product synthesis.** Human-readable terminal output and
  machine-consumable output are separate contracts. Claude Code and Gemini CLI
  document `text`, `json`, and `stream-json`; the observed Codex command
  exposes JSONL events with `--json`. A script needs to choose one format and
  parse it as a protocol, not scrape terminal prose. [O1][S1][S2][S3]
- **Cross-product synthesis.** Headless does not mean unrestricted. The
  products retain explicit control points for sandboxing or permissions;
  automation must select an appropriately constrained mode instead of relying
  on the absence of a terminal prompt. [O1][S1][S3]
- **Cross-product synthesis.** Persistence is a distinct policy from
  non-interactivity. Codex `--ephemeral` and Claude Code
  `--no-session-persistence` make one-off, non-resumable runs explicit, while
  Claude otherwise records sessions that can be continued or resumed. [O1][S1][S4]

## 2. Evidence from deployed applications

### Codex CLI 0.146.0: installed-command observation

- **Documented fact (local versioned observation).** On 2026-08-03, the
  installed `codex-cli 0.146.0` described `codex exec` as “Run Codex
  non-interactively.” It accepts an optional positional `PROMPT`; when no
  prompt is supplied (or it is `-`), it reads instructions from stdin. If both
  are present, piped stdin is appended as a `<stdin>` block. [O1]
- **Documented fact (local versioned observation).** The same help output lists
  `--ephemeral` (“without persisting session files to disk”), `--json` (“Print
  events to stdout as JSONL”), and `-s, --sandbox` with `read-only`,
  `workspace-write`, and `danger-full-access` values. It also labels the
  approval-and-sandbox bypass option as extremely dangerous and intended only
  for externally sandboxed automation. [O1]
- **Evidence boundary.** These are CLI-help observations from one installed
  binary, not a claim about all Codex versions or unlisted runtime semantics.

### Claude Code: official print-mode documentation

- **Documented fact.** The official headless guide says `claude -p` / `--print`
  runs non-interactively, reads piped stdin, exits `0` on success and non-zero
  on failure, and makes `text` (default), `json`, and `stream-json` available
  through `--output-format`. [S1]
- **Documented fact.** That guide says `--continue` continues the most recent
  conversation and `--resume` takes a session ID. Its CLI reference says
  `--no-session-persistence` is print-mode-only and prevents sessions being
  saved or resumed; this establishes normal print-mode persistence as the
  counterpart unless that option (or the documented environment variable) is
  selected. [S1][S4]
- **Documented fact.** The guide documents `--allowedTools` and permission
  modes. In particular, `dontAsk` denies actions outside allow rules and a
  read-only command set, while `acceptEdits` auto-approves edits and selected
  filesystem operations but not arbitrary shell or network requests. [S1]
- **Applicability boundary.** Claude's `--bare` is a reproducibility control:
  it skips discovery of hooks, skills, plugins, MCP servers, auto memory, and
  `CLAUDE.md`. It does not by itself establish a general cross-product session
  policy. [S1]

### Gemini CLI 0.44.1: installed-command observation and official reference

- **Documented fact (local versioned observation).** On 2026-08-03, installed
  `gemini 0.44.1 --help` said `-p` / `--prompt` runs in non-interactive
  headless mode, and that the provided prompt is appended to stdin input when
  present. It listed `text`, `json`, and `stream-json` output choices and
  `default`, `auto_edit`, `yolo`, and `plan` approval modes. [O2]
- **Documented fact.** Gemini's official CLI reference corroborates that
  `--prompt` forces non-interactive execution and is appended to stdin. It
  describes `--approval-mode=default|auto_edit|yolo|plan`, `--sandbox` for
  safer execution, and the same three output-format choices. [S3]
- **Documented fact.** Gemini's headless reference says non-TTY execution or
  `-p` triggers headless mode; its streaming form is JSONL and its documented
  headless exit codes include `0` success, `1` general/API error, `42` input
  error, and `53` turn-limit exceeded. [S2]

## 3. Mechanisms and tradeoffs

| Decision surface | Documented product behavior | Operational tradeoff |
| --- | --- | --- |
| Input composition | Codex and Gemini explicitly append piped data when a prompt flag/argument is also present; Claude documents stdin input but the cited page does not define a prompt-plus-stdin delimiter. [O1][O2][S1][S3] | A caller needs size limits, framing, and unambiguous provenance for untrusted logs or diffs. |
| Output contract | Claude and Gemini offer final JSON and streaming JSONL; observed Codex exposes JSONL events. [O1][S1][S2][S3] | Final JSON is simpler for a job result; event streams provide progress but need ordered, incremental parsing and a terminal-result check. |
| State lifetime | Codex has explicit ephemeral execution; Claude exposes an explicit print-mode persistence opt-out plus continuation/resume. [O1][S1][S4] | Persistence enables follow-ups but may retain task context; ephemeral runs reduce retained session state but remove resume. |
| Action authority | Codex exposes a sandbox policy; Claude exposes allow rules and permission modes; Gemini exposes approval modes and a sandbox flag. [O1][S1][S3] | A non-interactive process cannot answer a surprise approval prompt, so authority needs to be deliberately pre-scoped. |
| Process result | Claude documents success/non-success semantics; Gemini publishes a small exit-code taxonomy. [S1][S2] | CI can gate on exit status, but should retain structured output because a process failure alone does not categorize the agent outcome across products. |

## 4. Cross-product synthesis

- **Cross-product synthesis.** A safe headless invocation has four separate
  contracts: input composition, tool authority, output protocol, and state
  lifetime. Collapsing any pair produces avoidable ambiguity: for example,
  machine-readable output does not make a run ephemeral, and no terminal UI
  does not grant tool authority. [O1][O2][S1][S2][S3][S4]
- **Cross-product synthesis.** The mature products make destructive autonomy
  opt-in or policy-shaped: Codex provides a chosen sandbox and warns on full
  bypass; Claude scopes tool approval; Gemini distinguishes default, edit,
  all-action, and read-only planning modes. The common pattern is a declared
  authority boundary, not an inference from whether stdin is attached. [O1][S1][S3]
- **Cross-product synthesis.** Stream formats are useful for observability,
  but a consumer must not regard the first successful-looking event as job
  success. Gemini identifies a final `result` event and Claude says the final
  stream line is a `result` message; the observed Codex help only promises
  JSONL events, so its terminal-event schema must be version-checked before
  automated acceptance is based on it. [S1][S2][O1]

## 5. Pitfalls and evidence gaps

- **Evidence gap.** The cited Codex `--help` lists JSONL events but does not
  specify event types, final-event semantics, or a stable exit-code taxonomy.
  This note does not infer them.
- **Evidence gap.** The cited Claude stdin guidance confirms that stdin is
  read, but does not state an equivalent to Codex's `<stdin>` block or
  Gemini's documented append ordering when both an explicit prompt and stdin
  are supplied.
- **Evidence gap.** None of the cited materials establish a shared guarantee
  for atomic workspace edits, rollback after a failed headless run, or an
  identical interpretation of approval modes. The similar labels are not
  evidence of interchangeable security semantics.
- **Pitfall.** Claude documents that a failed run can place its failure result
  on stdout. Scripts should retain and parse the selected structured output and
  check the process exit status; neither channel alone is a complete portable
  result contract. [S1]
- **Pitfall.** Claude documents a 10 MB cap for piped stdin in print mode.
  Other products' limits were not established by this source set, so callers
  should not project that number onto Codex or Gemini. [S1]

## References

All sources accessed 2026-08-03.

- **[O1] Local observation:** `codex --version` and `codex exec --help` on the
  research machine; observed version `codex-cli 0.146.0`. This is reproducible
  only against that installed binary and is intentionally not an external URL.
- **[O2] Local observation:** `gemini --version` and `gemini --help` on the
  research machine; observed version `0.44.1`. This is reproducible only
  against that installed binary and is intentionally not an external URL.
- **[S1] Anthropic, “Run Claude Code programmatically”** (official Claude Code
  documentation): https://code.claude.com/docs/en/headless.md
- **[S2] Google, “Headless mode reference”** (official Gemini CLI
  documentation): https://geminicli.com/docs/cli/headless/
- **[S3] Google, “CLI cheatsheet”** (official Gemini CLI documentation):
  https://geminicli.com/docs/cli/cli-reference/
- **[S4] Anthropic, “CLI reference”** (official Claude Code documentation):
  https://code.claude.com/docs/en/cli-reference.md
