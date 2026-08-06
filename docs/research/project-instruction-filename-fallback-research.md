# Project instruction filename fallbacks: industry practice

> Status: research note, not an implementation plan.
>
> Research date: 2026-08-05. Product documentation changes; re-verify the
> cited behavior against a pinned release before treating it as a compatibility
> contract.
>
> Decision surface: how deployed coding agents discover project instruction
> files when a repository uses a product-specific filename instead of the
> agent's canonical filename.
>
> Scope: project-level filename discovery, fallback ordering, explicit
> instruction attachments, same-directory composition, and refresh/snapshot
> boundaries across deployed coding-agent applications.
>
> Out of scope: permissions, sandbox enforcement, semantic memory, generic
> agent frameworks, undocumented prompt assembly, and this repository's
> implementation.

## 1. Conclusions

1. **Filename compatibility is not one industry contract.** Gemini CLI lets
   users configure one or more context filenames. OpenCode treats `CLAUDE.md`
   as a compatibility fallback after `AGENTS.md`, while Aider does not infer a
   convention file at all and asks the user to attach it. These are distinct
   discovery models, not interchangeable spellings of one standard.
2. **A fallback should be distinguishable from composition.** OpenCode's local
   compatibility behavior is first-match within its rule category. Claude Code
   itself loads its base and local project files as separate layers, with local
   content appended after the base. A product that calls a second filename a
   fallback must not silently imply that it implements Claude's complete
   `CLAUDE.local.md` hierarchy.
3. **The canonical filename should remain the stable default.** OpenCode gives
   `AGENTS.md` precedence over `CLAUDE.md`, and its compatibility switch can be
   disabled. This makes an opt-in or explicitly configured fallback safer for
   repositories that contain multiple instruction ecosystems.
4. **Arbitrary names need an observable, bounded contract.** Gemini's setting
   demonstrates the value of configuration, while Aider's explicit file list
   demonstrates the value of user-visible attachment. A useful compatibility
   contract therefore needs an ordered name list, same-directory scope, clear
   empty/non-file behavior, and source-name provenance; it should not become an
   unrestricted recursive import mechanism.
5. **Discovery timing remains separate from filename selection.** Claude
   documents session-start loading for root/user project context and lazy
   loading for nested context; Gemini exposes an explicit memory reload; Aider
   uses explicit read/configuration. A filename fallback does not by itself
   justify hot reload or changing an active session's immutable context.

## 2. Evidence from deployed applications

### 2.1 Gemini CLI: configurable context filename

**Documented facts.** Gemini CLI uses `GEMINI.md` as its default context file.
It searches a global location, configured workspace/ancestor directories, and
trusted-root just-in-time locations, concatenating discovered context files for
the model. The documentation exposes `/memory show` and `/memory reload`, and
the footer reports the number of loaded context files. It also documents a
`context.fileName` setting that can name a different file or a list such as
`AGENTS.md`, `CONTEXT.md`, and `GEMINI.md`. [Gemini context files][gemini-md]

The page establishes configurability but does not fully specify whether a list
of names is a same-directory fallback, an ordered replacement set, or a set of
independently concatenated candidates at every hierarchy level. That selection
detail is an evidence gap; the setting should not be treated as proof of a
universal precedence rule.

### 2.2 OpenCode: canonical AGENTS with Claude compatibility fallback

**Documented facts.** OpenCode discovers local `AGENTS.md` files by traversing
upward from the current directory. For users migrating from Claude Code, it
also recognizes project `CLAUDE.md` as a fallback and `~/.claude/CLAUDE.md` as
a global fallback. Its documented precedence says local candidates are checked
before global candidates, `AGENTS.md` wins over `CLAUDE.md` in the local
category, and the first matching file wins in each category. Compatibility can
be disabled with environment variables. [OpenCode rules][opencode-rules]

OpenCode separately supports an `instructions` configuration list containing
local files, globs, and remote URLs; those instructions are combined with the
discovered rule files. This is an explicit composition mechanism, not the same
as the one-file compatibility fallback. The docs also say external references
inside `AGENTS.md` are not automatically parsed. [OpenCode rules][opencode-rules]

### 2.3 Claude Code: native base/local composition

**Documented facts.** Claude Code's project instruction model uses
`CLAUDE.md`, with project and user scopes, and also supports a gitignored
`CLAUDE.local.md` for local project instructions. The documented model places
local instructions after the shared project instructions. Claude also supports
project locations such as `./.claude/CLAUDE.md` and imports or symlinks as ways
to reuse another instruction source. [Claude memory][claude-memory]

The official prompt-caching guidance says root and user `CLAUDE.md` context is
read at session start rather than being silently replaced whenever the file is
edited. It describes reload points such as `/compact`, `/clear`, or a new
session, while nested instructions can be discovered when their path is
encountered. [Claude prompt caching][claude-cache]

This is not evidence that a product using `AGENTS.override.md` and
`AGENTS.md` should load a `CLAUDE.local.md` equivalent. Claude's local/base
pair is an append-oriented hierarchy; a first-match fallback is a different
compatibility tradeoff.

### 2.4 Aider: explicit convention attachment

**Documented facts.** Aider's coding-conventions documentation asks the user to
create a small convention file and attach it with `/read` or `--read`. It also
supports one or more always-read files through the `read` setting in
`.aider.conf.yml`. The document does not describe automatic ancestor discovery
or a reserved filename fallback. [Aider conventions][aider-conventions]

This model makes the source explicit to the user and supports arbitrary names,
but it shifts discovery responsibility to the user. It also shows that
filename compatibility is optional product behavior rather than a prerequisite
for useful project instructions.

## 3. Mechanisms and tradeoffs

| Model | Trigger | Same-directory behavior | Main tradeoff |
| --- | --- | --- | --- |
| Configured filename set | Startup or explicit reload | Product-specific; list semantics may be under-specified | Portable, but needs ordering and provenance rules |
| Canonical plus fallback | Startup discovery | Canonical candidate wins; fallback is considered only when absent/invalid | Preserves existing repositories, but can hide a second file |
| Base plus local composition | Startup plus scope discovery | Both files contribute in documented order | Expressive, but conflicts and budgets become more complex |
| Explicit attachment | User command or config | All named attachments can be included | Clear and flexible, but less automatic |

Cross-product evidence supports several reusable boundaries:

- **Candidate selection is not import.** A fallback filename names one file in a
  directory; it does not recursively parse references, globs, URLs, or nested
  rules unless a separate feature explicitly promises that behavior.
- **One source per category reduces ambiguity.** Where a product chooses a
  canonical rule over a compatibility rule, it should report which source won
  and avoid presenting the loser as partially active.
- **Empty and non-file candidates need deterministic handling.** The public
  products do not all document this edge, but a compatibility layer that skips
  missing, blank, or non-regular candidates must distinguish those cases from
  actual read failures so users can diagnose configuration problems.
- **The configured name list must be bounded.** Arbitrary filenames are useful;
  arbitrary relative paths would turn filename compatibility into an accidental
  path/import feature with different trust and security implications.
- **Source metadata matters.** Gemini exposes loaded-file counts, Claude offers
  an `InstructionsLoaded` hook, and OpenCode documents precedence and disable
  controls. A compatibility loader should retain the selected path/name and
  whether content was truncated or refreshed, even if the UI does not yet
  expose all of that metadata.

## 4. Cross-product synthesis

The most portable abstract contract is a **canonical-first, ordered fallback
set**:

1. Start with a product-owned canonical filename.
2. Consider configured fallback names only in the same discovery directory.
3. Select at most one non-empty regular candidate for that directory.
4. Preserve the selected source name and the exact selection order in metadata.
5. Treat fallback selection as soft context; keep hard permissions and sandbox
   controls independent.
6. Define refresh as an explicit lifecycle event rather than an incidental file
   watcher side effect.

This model aligns with OpenCode's compatibility precedence while retaining
Gemini's configurability and Aider's emphasis on explicit source identity. It
does not claim that Claude's base/local composition can be reduced to a
fallback, nor that Gemini's multi-name setting has the same precedence.

## 5. Pitfalls and evidence gaps

- Public documentation does not consistently specify whether an ordered list
  of configured names means first-match, concatenation, or replacement of the
  default name. Implementations should document this instead of inferring it
  from the setting's shape.
- Claude documents multiple project instruction locations and local/base
  composition, but the relative behavior when every supported location exists
  is not fully specified on the memory page. An `InstructionsLoaded` event or
  an observed release should be the compatibility authority.
- The reviewed products do not publish one shared rule for blank files,
  dangling symlinks, or read errors across all discovery modes. Those cases need
  product-specific tests and user-visible diagnostics.
- A compatibility filename can contain sensitive local instructions. Public
  fallback behavior does not turn instruction text into a permission boundary;
  hard enforcement must remain in the host/tool policy.
- Session reload behavior is not implied by a filename change. A loader can
  support fresh-session discovery while an active thread keeps its original
  prompt, or it can offer an explicit reload; these are separate contracts.

## References

- [Gemini CLI: Project context (`GEMINI.md`)][gemini-md] (official docs,
  updated 2026-06-18; accessed 2026-08-05).
- [OpenCode: Rules][opencode-rules] (official docs; accessed 2026-08-05).
- [Anthropic Claude Code: How Claude remembers your project][claude-memory]
  (official docs; previously captured 2026-08-04, re-verify before adoption).
- [Anthropic Claude Code: Prompt caching][claude-cache] (official docs;
  previously captured 2026-08-04, re-verify before adoption).
- [Aider: Specifying coding conventions][aider-conventions] (official docs;
  accessed 2026-08-05).

[gemini-md]: https://geminicli.com/docs/cli/gemini-md/
[opencode-rules]: https://opencode.ai/docs/rules/
[claude-memory]: https://code.claude.com/docs/en/memory
[claude-cache]: https://code.claude.com/docs/en/prompt-caching
[aider-conventions]: https://aider.chat/docs/usage/conventions.html
