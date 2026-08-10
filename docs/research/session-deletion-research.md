# Agent CLI session deletion: industry evidence

> Status: research notes, not an implementation design or migration plan.
>
> Research date: 2026-08-10. CLI behavior changes over time and must be verified again before adopting more surface area.
>
> Scope: permanent saved-session removal in deployed coding-agent CLIs, especially confirmation and identifier behavior.

## Evidence

### Codex CLI: explicit ID plus force confirmation

**Fact (locally installed product observation):** Codex CLI `0.146.0` reports `codex delete [OPTIONS] <SESSION>` in `codex delete --help`. Its `--force` flag is described as "Delete without prompting. SESSION must be a UUID." The command therefore treats deletion as a shell-visible lifecycle action, accepts a stable session identifier, and distinguishes an explicit no-prompt confirmation path. Observed 2026-08-10.

**Fact (locally installed product observation):** `codex --help` lists `delete` alongside `resume`, `fork`, `archive`, and `unarchive`. This makes destructive session lifecycle control discoverable outside the TUI. The public project is [openai/codex](https://github.com/openai/codex); the local observation is the source for the behavioral claim.

### Other products: evidence unavailable for this iteration

**Evidence gap:** Claude Code and OpenCode permanent shell-deletion behavior was not independently verified in this environment. This note makes no factual claim about their commands, confirmation wording, or archive semantics.

## Derived product constraints

- Permanent deletion needs an unmistakable confirmation boundary. A command must not treat an ID alone as permission to irreversibly erase durable user context.
- A stable identifier is safer than an implicit "latest" selector for destructive actions: concurrent sessions can change what is newest after the user inspected a list.
- The command should be headless and avoid model/TUI startup so it remains predictable in scripts and recovery workflows.
- A durable session with an active turn or pending provider-backed compaction is not safely inert. The storage layer needs to reject it under the same writer lock used for mutations, not rely only on UI state.
- Export is a non-destructive recovery alternative; documentation should direct users to export before a permanent action when they need a transcript.

## References

- Codex CLI `0.146.0`: local `codex --help` and `codex delete --help`, observed 2026-08-10; [Codex CLI reference](https://developers.openai.com/codex/cli/reference/).
- Codex source project: [openai/codex](https://github.com/openai/codex).
