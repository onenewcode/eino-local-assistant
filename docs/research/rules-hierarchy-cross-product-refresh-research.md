# 规则层级与运行边界：跨产品刷新研究

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-04. The cited product documentation is rolling
> documentation; re-verify the behavior against a pinned release before using
> it as a compatibility contract.
>
> Decision surface: how deployed coding agents decide which instructions,
> paths, worktrees, and runtime budgets belong to a session, and how users can
> see or reload those decisions.
>
> Scope: Gemini CLI, OpenCode, and Aider as deployed coding-agent
> applications; instruction hierarchy, nested files and imports, Git
> worktrees, symlink/path boundaries, token and context budgets, reload
> behavior, and user-visible observability.
>
> Out of scope: this repository's implementation, a local migration plan,
> undocumented internals, and general-purpose agent frameworks or APIs.

## 1. Conclusions

1. **There is no single industry hierarchy model.** Gemini CLI automatically
   composes global, workspace/ancestor, and just-in-time child-directory
   `GEMINI.md` context. OpenCode combines a precedence-ordered rule lookup with
   separately configured instruction files. Aider's documented convention is
   deliberately explicit: the user adds a file with `/read`, `--read`, or a
   config `read` entry. These are different contracts, not interchangeable
   names for an `AGENTS.md` loader. [Gemini hierarchy][gemini-md] [OpenCode
   rules][opencode-rules] [Aider conventions][aider-conventions]

2. **Worktree identity and instruction scope are separate dimensions.** Gemini
   has a first-class, explicitly marked experimental worktree flow: a named
   worktree gets a directory and branch under `.gemini/worktrees/`, remains
   after exit, and can be resumed from that directory. OpenCode and Aider's
   public documentation describes starting in a directory/current Git project,
   but does not document an equivalent agent-owned worktree lifecycle. A Git
   worktree therefore cannot be assumed to imply a new session, a new rule
   root, or automatic cleanup. [Gemini worktrees][gemini-worktrees]
   [OpenCode CLI][opencode-cli] [Aider Git][aider-git]

3. **Symlink handling is the least specified boundary.** OpenCode recommends
   symlinks or submodules as a way to share rule files, and separately requires
   permission for paths outside the working directory. Neither document states
   whether rule discovery, permission matching, snapshots, or imports resolve
   symlink targets canonically. Gemini documents relative and absolute imports
   and a trusted-root limit for just-in-time discovery, but not symlink target
   semantics. Aider's public usage docs explain how files are read, not how
   symlinked files or directories are classified. Sharing guidance is not a
   security guarantee. [OpenCode rules][opencode-rules] [OpenCode
   permissions][opencode-permissions] [Gemini hierarchy][gemini-md] [Aider
   conventions][aider-conventions]

4. **“Budget” is a family of controls, not one work quota.** Gemini exposes
   compression, token statistics, and provider-dependent cached-token savings.
   OpenCode exposes automatic compaction, optional tool-output pruning, a
   reserved compaction buffer, and session cost statistics. Aider exposes
   separate chat-history, repository-map, and thinking-token controls while
   explicitly saying that provider token limits are not enforced by Aider.
   None of these documents establishes a universal maximum for wall-clock
   time, tool steps, or total repository changes. [Gemini commands][gemini-commands]
   [Gemini caching][gemini-caching] [OpenCode config][opencode-config] [OpenCode
   CLI][opencode-cli] [Aider options][aider-options] [Aider token limits][aider-tokens]

5. **Reload and inspection contracts are uneven.** Gemini has named commands
   to show/list/refresh hierarchical memory and reload commands, and its
   footer exposes the number of loaded context files. Aider has commands for
   reading files, refreshing its repository map, showing settings/prompts, and
   reporting tokens, but its convention docs do not promise automatic rule
   reload. OpenCode documents resolved-config debugging, logs, stats, a file
   watcher, and session export, but the public docs do not promise that editing
   an `AGENTS.md` or an instruction URL re-evaluates the active prompt. The
   observable “effective context” boundary is consequently product-specific.
   [Gemini hierarchy][gemini-md] [Gemini commands][gemini-commands] [OpenCode
   CLI][opencode-cli] [OpenCode config][opencode-config] [Aider commands][aider-commands]
   [Aider analytics][aider-analytics]

## 2. Evidence from deployed applications

### 2.1 Gemini CLI: automatic hierarchy with explicit refresh points

**Documented facts.** Gemini CLI's `GEMINI.md` documentation describes three
layers: a global `~/.gemini/GEMINI.md`, files found in configured workspace
directories and their parents, and just-in-time files discovered when a tool
accesses a directory. It says the files are concatenated and sent with every
prompt, and that JIT discovery stops at a trusted root. The same page supports
relative and absolute `@file.md` imports and allows the context filename to be
configured to one or more names such as `AGENTS.md`. The footer reports the
number of loaded context files. [Gemini hierarchy][gemini-md]

The command reference gives the hierarchy a user-facing control surface:
`/memory list` shows source paths, `/memory show` shows the concatenated loaded
content, and `/memory refresh` rescans the configured locations. It separately
documents `/commands reload` and `/agents reload`, so custom commands and agent
registries are not implicitly the same thing as instructional memory. The
reference also documents `/compress` as replacing the entire chat context with
a summary, and project-scoped save/resume behavior for chat checkpoints.
These are documented operations, not claims about hidden prompt assembly.
 [Gemini commands][gemini-commands]

Git worktrees are explicitly labeled experimental. When enabled in settings,
`--worktree [name]` creates a separate directory and branch under
`.gemini/worktrees/`; a missing name is generated. On `/quit` or `Ctrl+C`, the
worktree, including uncommitted and committed work, is retained rather than
automatically deleted. Resuming requires entering the worktree directory and
using the session ID. This gives the user a visible lifecycle and a clear
cleanup responsibility. [Gemini worktrees][gemini-worktrees]

For cost and context observability, Gemini documents token caching for API-key
and Vertex AI authentication, explicitly excluding OAuth/Code Assist users,
and exposes cached-token savings through `/stats`. The same command reference
exposes `/compress`; the caching page does not equate cached tokens with a
larger context window. [Gemini caching][gemini-caching] [Gemini commands][gemini-commands]

Gemini's experimental Auto Memory is another useful boundary case. It mines
idle local transcripts, keeps proposed patches and skills in a review inbox,
does not apply them automatically, and says approved memory patches reload
memory for the current session. Enabling the extraction service requires a
restart, while the extracted candidates are not automatically loaded into a
session. This separates durable-memory lifecycle from ordinary project
instructions. [Gemini auto memory][gemini-auto-memory]

**What the evidence does not establish.** The hierarchy page names a trusted
root but does not specify how a symlinked directory, a symlinked `GEMINI.md`, an
import cycle, or an imported absolute path is canonicalized or authorized. The
worktree page also does not state whether the current worktree changes the
project identity used for all saved sessions, nor whether instructions are
snapshotted at worktree creation. These are evidence gaps, not inferred
behavior.

### 2.2 OpenCode: precedence layers plus explicit instruction composition

**Documented facts.** OpenCode's rules page says local `AGENTS.md` files are
found by traversing upward from the current directory, with a global
`~/.config/opencode/AGENTS.md` and a Claude Code compatibility fallback. Its
precedence section says the first matching file wins in each category: local
`AGENTS.md` over local `CLAUDE.md`, and OpenCode's global rule over the global
Claude fallback. The same page supports an `instructions` array in
`opencode.json`, including local files, globs, and remote URLs; those files are
combined with `AGENTS.md`. It says OpenCode does not automatically parse file
references in `AGENTS.md`; a user-authored instruction can instead ask the
agent to load references lazily. [OpenCode rules][opencode-rules]

OpenCode's broader configuration hierarchy is a separate layer from prompt
rules. The docs list remote organizational config, global config, custom paths,
project config, `.opencode` directories, inline environment config, and
managed settings, with later layers overriding conflicts while preserving
non-conflicting settings. Project config is found from the current directory
up to the nearest Git directory. The docs also expose `debug config` to inspect
the resolved configuration. [OpenCode config][opencode-config]

The public CLI surface includes `--cwd`/`--dir`, session continuation and
forking, `opencode stats` for token/cost statistics, session export/import,
`--print-logs`, and selectable log levels. These controls make runtime state
and accounting inspectable, but they do not by themselves promise that the
effective instruction text or its provenance is shown. [OpenCode CLI][opencode-cli]

OpenCode's path boundary is expressed primarily as a permission boundary. A
tool touching a path outside the startup working directory triggers the
`external_directory` permission, which defaults to asking; a permitted
external path inherits workspace defaults unless more specific rules override
them. Permission patterns are last-match-wins, and explicit denies remain in
force under `--auto`. The docs separately say snapshots track file changes by
an internal Git repository, are enabled by default, and can be disabled for
large repositories; a watcher can ignore noisy glob patterns. [OpenCode
permissions][opencode-permissions] [OpenCode config][opencode-config]

For context budgets, OpenCode documents automatic compaction when context is
full, optional pruning of old tool output, and a reserved token buffer for
compaction. Its CLI also exposes session-level stats and an experimental
maximum output-token environment variable. The docs do not describe these as
a fixed per-task step or wall-clock budget. [OpenCode config][opencode-config]
 [OpenCode CLI][opencode-cli]

**What the evidence does not establish.** The docs do not describe an
agent-managed `--worktree` lifecycle, a worktree-specific session identity, or
what happens when a project config, `AGENTS.md`, or remote instruction changes
while a session is active. They also do not specify symlink canonicalization
for the upward rule walk, `external_directory` matching, snapshots, or remote
instruction imports. The rules page's suggestion that shared rules may use
symlinks is useful composition guidance, but it is not evidence of a secure
path-resolution contract. [OpenCode rules][opencode-rules]

### 2.3 Aider: explicit context attachments around a Git-centric session

**Documented facts.** Aider's convention guide tells users to create a
`CONVENTIONS.md` (or similar) and add it to the chat with `/read` or
`--read`; it recommends read-only loading so the file is cached when prompt
caching is enabled. The same guide supports one or more always-read files via
the `read` setting in `.aider.conf.yml`. The documented mechanism is therefore
an explicit attachment, not an automatically discovered ancestor/child rule
hierarchy. [Aider conventions][aider-conventions]

Aider's Git integration is designed around the current Git repository. Its
docs say edits are normally committed with descriptive messages, pre-existing
dirty changes may be committed first, and `/undo`, `/diff`, `/commit`, and
`/git` operate on that history. The option reference includes `--aiderignore`
and `--subtree-only`, but does not list an agent-owned worktree flag or a
worktree cleanup/resume contract. [Aider Git][aider-git] [Aider options][aider-options]

Its context budget is split into named controls. `--max-chat-history-tokens`
limits the history budget; `--map-tokens` controls the repository-map budget;
and `/think-tokens` or `--thinking-tokens` controls reasoning tokens where the
model supports them. The repository-map documentation says the map is selected
to fit the active token budget, defaults to 1k map tokens, and may expand when
no files are attached. The token-limit guide explicitly says Aider does not
enforce provider token limits; it reports errors from the provider and its
token counts are estimates. [Aider options][aider-options] [Aider repo map][aider-repomap]
 [Aider token limits][aider-tokens]

Aider offers granular, user-invoked observability and refresh controls: `/tokens`
reports current context usage, `/map` and `/map-refresh` expose or refresh the
repository map, `/ls` shows known files and chat membership, `/settings` shows
settings, `/show-prompts` is available in the options reference, and
`--analytics-log` writes an inspectable analytics log. `/clear`, `/drop`, and
`/reset` change context state explicitly. `/read` is the documented way to add
or re-add a convention file. [Aider commands][aider-commands] [Aider
options][aider-options] [Aider analytics][aider-analytics]

**What the evidence does not establish.** The public usage pages do not state
that convention files are discovered from ancestors, automatically reloaded
after edits, or tied to a Git worktree identity. They also do not document
whether a symlink is treated as the link path or target path for read-only
classification, ignore rules, repository mapping, or Git safety checks. The
documented `--watch-files` option should not be interpreted as an instruction
reload guarantee without product evidence that says so. [Aider options][aider-options]

## 3. Mechanisms and tradeoffs

| Boundary | Gemini CLI | OpenCode | Aider |
| --- | --- | --- | --- |
| Instruction admission | Automatic global/workspace/ancestor plus JIT child discovery; imports are explicit in a loaded context file. | Upward local rule lookup plus global fallback; additional files are named in `instructions`, and manual references are user-authored. | Explicit `/read`, `--read`, or configured `read`; no documented automatic hierarchy. |
| Precedence/composition | Documents concatenation order and configured filenames; imported-file precedence/cycles are not specified. | “First matching file” per rule category; configured instruction files are combined; config layers are merged with later conflict winners. | Attachment order and prompt treatment are not fully specified in the usage guide. |
| Worktree/session boundary | First-class experimental worktree, named directory and branch, preserved on exit, resume from inside it. | Current directory/nearest Git project and session `--continue`/`--fork`; no documented agent-owned worktree lifecycle. | Current Git repository and Git history; no documented agent-owned worktree lifecycle. |
| Symlink/path evidence | Trusted-root wording for JIT discovery; symlink behavior not stated. | Symlinks suggested for sharing; external paths require permission; canonicalization not stated. | File reading and Git options documented; symlink treatment not stated. |
| Context/accounting budget | `/compress`, `/stats`, and authentication-dependent token caching. | Auto-compaction, pruning, reserved buffer, output-token setting, and session stats/cost. | Chat-history, repo-map, and thinking-token controls; provider limits are reported, not enforced. |
| Reload/inspection | `/memory list/show/refresh`, footer file count, plus separate command/agent reloads. | `debug config`, logs, stats, export, and watcher controls; active instruction reload is not promised. | `/read`, `/map-refresh`, `/tokens`, `/settings`, `/show-prompts`, and logs; automatic convention reload is not promised. |

The table shows three useful design tradeoffs without turning any one product
into a normative standard:

- **Automatic discovery buys low-friction locality at the cost of provenance
  complexity.** Gemini's JIT model can avoid loading every nested component's
  rules up front, but the user needs source listing and refresh controls to
  know why a prompt changed. Aider makes the opposite tradeoff: attachment is
  explicit and predictable, but the user must maintain the context list.

- **Precedence is not the same as inheritance.** OpenCode's first-match rule
  categories and merged config layers show that “nearest rule wins” and
  “concatenate all parent rules” have different conflict and audit semantics.
  A product can combine them, as OpenCode does for rule files versus
  `instructions`, but documentation must name the boundary for each source.

- **Worktree isolation is stronger when the lifecycle is observable.** Gemini
  names the directory, branch, retention, resume command, and cleanup owner.
  The other two products' documented current-directory model leaves worktree
  naming, session association, and cleanup to Git/user workflows. The absence
  of a first-class feature is not evidence that Git worktrees are unsupported;
  it is evidence that the product docs do not promise their semantics.

- **A path policy needs a canonicalization statement.** “External directory,”
  “trusted root,” “current Git project,” and “read-only file” are lexical or
  logical labels until a product explains how symlinks, `..`, mounts, and
  renames are resolved. OpenCode's symlink-sharing advice and its external
  directory permission are adjacent controls, not one documented invariant.

- **Budget UX works best when it distinguishes accounting from enforcement.**
  Aider's explicit estimate/provider-error distinction, OpenCode's compaction
  buffer, and Gemini's cached-token stats answer different questions: how much
  context is assembled, when it is compressed, what the provider bills, and
  whether a task may continue. The product docs do not support collapsing
  those questions into a single “token budget.”

- **Reload should identify both the source set and the active prompt.** Gemini
  is the clearest example because `/memory show` and `/memory refresh` expose
  both. OpenCode's resolved configuration and Aider's settings/tokens tools
  are valuable, but neither cited documentation guarantees that those views
  equal the exact instruction payload sent on the next model request.

## 4. Cross-product synthesis

The evidence supports a product-neutral boundary model with five separately
named objects:

1. **Instruction sources:** global, project/ancestor, nested/JIT, explicit
   attachments, imports, remote URLs, and generated commands. Each source
   needs provenance and a stated composition/precedence rule.
2. **Workspace identity:** the startup directory, nearest Git root, an explicit
   external path, or a named Git worktree. This identity determines where
   discovery begins, but does not by itself determine session history or
   durable memory.
3. **Path authority:** the set of paths a tool may read/write, including the
   canonical target of a symlink and the result of a path rename. A sharing
   mechanism must not be mistaken for an authorization rule.
4. **Runtime budget:** context window occupancy, output/reasoning allowance,
   tool-output retention, compression/pruning thresholds, provider accounting,
   and any actual step/time limit. Products should state which layer enforces
   which limit.
5. **Live-session view:** the loaded source set, effective instruction text or
   a faithful redacted representation, workspace/path identity, budget counters,
   and reload version. A “reload” operation is only auditable if its scope and
   activation point are visible.

Across the three products, the most reliable user-facing invariant is not a
particular filename. It is the ability to answer, before a consequential tool
call: “Which instructions are active, which directory/path boundary applies,
which context has been retained or compressed, and what changed since the last
request?” The products provide different pieces of that answer, and the public
documentation does not establish that any one provides all of it.

## 5. Pitfalls and evidence gaps

- **Symlink and rename tests are absent from the cited contracts.** None of the
  reviewed documentation specifies canonical path resolution across a symlink,
  a symlink swap, a mount point, or a renamed worktree. It is unsafe to infer
  security behavior from a display path or from the phrase “trusted root.”

- **Nested instruction precedence remains incomplete.** Gemini documents
  concatenation and JIT scope but not import cycle handling, duplicate
  suppression, import size limits, or whether an imported file inherits the
  trust boundary of its parent. OpenCode documents local-category precedence
  and explicit instruction combination but not the exact order between every
  glob, remote URL, and discovered rule. Aider documents explicit inclusion
  but not all prompt ordering details.

- **Reload boundaries are not equivalent to file watching.** A file watcher
  can detect source changes without changing an already-built prompt, and a
  refresh command can update one registry without touching session history,
  permissions, or memory. Only Gemini's cited memory commands explicitly state
  the active-memory refresh effect. OpenCode and Aider leave several live
  session cases undocumented.

- **Budget counts are not necessarily enforcement.** Gemini's cached-token
  visibility depends on authentication; Aider reports estimated counts and
  provider errors; OpenCode's reserved buffer is a compaction control. None of
  these is evidence of a universal task-step, wall-clock, or spend cap.

- **Worktree persistence can outlive the UI.** Gemini intentionally keeps
  worktrees and uncommitted files after exit. A user-visible “session ended” or
  “context reloaded” state therefore should not be conflated with filesystem
  cleanup, branch deletion, or transcript deletion. The cited OpenCode and
  Aider pages do not define comparable ownership semantics.

- **Remote instructions introduce a separate supply-chain boundary.** OpenCode
  permits remote instruction URLs and documents a fetch timeout, but the cited
  rules page does not establish pinning, integrity verification, or a visible
  revision in the effective-context view. This is a documented capability with
  an important evidence gap, not a claim that it is unsafe in every deployment.

- **The source set is mostly vendor documentation.** These pages establish
  shipped/documented behavior and user controls, but they do not independently
  verify implementation consistency across platforms or releases. No claim
  above should be read as a black-box interoperability guarantee.

## References

All sources below were accessed on 2026-08-04.

### Gemini CLI

- [GEMINI.md context hierarchy](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/gemini-md.md) — global, workspace/ancestor, JIT discovery, imports, trusted root, and footer visibility.
- [Git worktrees](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/git-worktrees.md) — experimental worktree creation, retention, resume, and cleanup ownership.
- [CLI commands](https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/commands.md) — memory, reload, compression, stats, and project-scoped session controls.
- [Token caching and cost optimization](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/token-caching.md) — authentication-dependent caching and `/stats` visibility.
- [Auto Memory](https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/auto-memory.md) — review inbox, restart boundary, candidate persistence, and current-session reload behavior.

### OpenCode

- [Rules](https://opencode.ai/docs/rules/) — rule lookup, precedence, `instructions`, remote URLs, and symlink-sharing guidance.
- [Config](https://opencode.ai/docs/config/) — merged configuration layers, project/Git-root lookup, snapshots, compaction, watcher settings, and `debug config`.
- [Permissions](https://opencode.ai/docs/permissions/) — last-match patterns, external-directory approval, path permissions, and agent overrides.
- [CLI](https://opencode.ai/docs/cli/) — cwd/session controls, stats, export/import, logs, compaction-related environment variables, and output-token settings.
- [TUI](https://opencode.ai/docs/tui/) — session, compaction, share, and attention controls.

### Aider

- [Specifying coding conventions](https://aider.chat/docs/usage/conventions.html) — explicit read-only convention files and always-read configuration.
- [Options reference](https://aider.chat/docs/config/options.html) — chat-history, map, thinking, Git/path, watch, prompt, and analytics options.
- [In-chat commands](https://aider.chat/docs/usage/commands.html) — `/read`, `/tokens`, `/map-refresh`, `/settings`, `/show-prompts`, context reset, and interruption behavior.
- [Repository map](https://aider.chat/docs/repomap.html) — ranked map selection and map-token budget behavior.
- [Git integration](https://aider.chat/docs/git.html) — current-repository integration, commits, dirty-file handling, and undo semantics.
- [Token limits](https://aider.chat/docs/troubleshooting/token-limits.html) — provider errors, estimated token counts, and the explicit non-enforcement boundary.
- [Analytics](https://aider.chat/docs/more/analytics.html) — opt-in telemetry and local analytics-log inspection.

[gemini-md]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/gemini-md.md
[gemini-worktrees]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/git-worktrees.md
[gemini-commands]: https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/commands.md
[gemini-caching]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/token-caching.md
[gemini-auto-memory]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/auto-memory.md
[opencode-rules]: https://opencode.ai/docs/rules/
[opencode-config]: https://opencode.ai/docs/config/
[opencode-permissions]: https://opencode.ai/docs/permissions/
[opencode-cli]: https://opencode.ai/docs/cli/
[opencode-tui]: https://opencode.ai/docs/tui/
[aider-conventions]: https://aider.chat/docs/usage/conventions.html
[aider-options]: https://aider.chat/docs/config/options.html
[aider-commands]: https://aider.chat/docs/usage/commands.html
[aider-repomap]: https://aider.chat/docs/repomap.html
[aider-git]: https://aider.chat/docs/git.html
[aider-tokens]: https://aider.chat/docs/troubleshooting/token-limits.html
[aider-analytics]: https://aider.chat/docs/more/analytics.html
