# Invocation-scoped tool filtering: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: controls that narrow the model-visible tool set for one coding-agent invocation, and how they relate to execution permissions.
> Out of scope: persistent organization policy, command-argument allowlists, MCP server installation, and sandbox implementation.

## 1. Conclusions

- **Fact:** Claude Code exposes an invocation-scoped `--tools` control that accepts a built-in-tool list, `default` for all built-ins, and an empty string to disable tools. Its same invocation also supports `--allowedTools` and `--disallowedTools`, with names or constrained command patterns. This makes a deliberate tool-free request a normal agent mode rather than a malformed tool loop. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-10]
- **Fact:** OpenCode describes permission decisions independently from agent and global configuration, including explicit `allow`, `ask`, and `deny` outcomes and per-agent overrides. A tool being configured is therefore not equivalent to unconditional execution authority. [OpenCode permissions documentation, updated 2026-08-09, accessed 2026-08-10](https://opencode.ai/docs/permissions/)
- **Fact:** Codex CLI exposes separate invocation controls for sandbox mode and approval policy. Its help describes both as execution controls, rather than a selector for which tool schemas are sent to the model. [Observed in Codex CLI 0.146.0 `codex --help`, 2026-08-10; [Codex documentation](https://developers.openai.com/codex/), accessed 2026-08-10]
- **Synthesis:** A useful one-shot tool selector should be a visibility/capability reduction only: validate names before the model request, select from the final discovered names, make denial override allowance, and leave approval, command policy, and sandbox checks intact.

## 2. Evidence from deployed applications

### Claude Code

**Fact:** The installed Claude Code 2.1.220 help lists all of these process options:

- `--tools <tools...>`: a list from the built-in set; `""` disables every tool and `default` restores all tools.
- `--allowedTools` / `--allowed-tools`: a comma- or space-separated allow list.
- `--disallowedTools` / `--disallowed-tools`: a comma- or space-separated deny list.

The help examples include both whole tool names and a constrained shell form such as `Bash(git *)`. This supports two distinct scopes: selecting a tool capability and restricting allowed invocations of an otherwise available capability. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-10]

### OpenCode

**Fact:** OpenCode's permissions documentation gives a global `permission` object whose entries resolve to `allow`, `ask`, or `deny`; it documents granular rules such as allowing `git *` but denying `git push *`. It also states that agent permissions are merged with global configuration and that agent rules take precedence. [OpenCode permissions documentation, updated 2026-08-09, accessed 2026-08-10](https://opencode.ai/docs/permissions/)

**Fact:** The same documentation notes that an `always` approval applies only for the current OpenCode session, which distinguishes an ephemeral decision from a durable configuration edit. [OpenCode permissions documentation, updated 2026-08-09, accessed 2026-08-10](https://opencode.ai/docs/permissions/)

### Codex CLI

**Fact:** The installed Codex CLI 0.146.0 offers `--sandbox` with `read-only`, `workspace-write`, and `danger-full-access`, plus `--ask-for-approval` with `untrusted`, `on-request`, and `never`. Its help describes whether a proposed command executes automatically or escalates; it does not claim that those switches remove the corresponding model tool schema. [Observed in Codex CLI 0.146.0 `codex --help`, 2026-08-10]

## 3. Mechanisms and tradeoffs

- **Fact:** Claude Code's empty `--tools` value establishes a usable direct-model mode for prompt-only tasks. The resulting agent cannot collect fresh workspace evidence, so callers must consciously choose the narrower surface. [Observed in Claude Code 2.1.220 `claude --help`, 2026-08-10]
- **Fact:** OpenCode's granular rules distinguish a tool category from arguments passed to that category. A global tool-name selector cannot safely substitute for argument-level policy. [OpenCode permissions documentation, updated 2026-08-09, accessed 2026-08-10](https://opencode.ai/docs/permissions/)
- **Synthesis:** Name resolution should happen against the final tool set after extensions are discovered. Resolving before discovery makes external tools impossible to select and makes an error's available-name list incomplete.
- **Synthesis:** A deny rule should have higher precedence than an allow rule. This lets a caller begin with a broad set and remove a known hazardous capability without needing to enumerate every remaining tool.
- **Synthesis:** An explicit empty allow set needs a model path that never binds tools. Passing an empty tool list to a ReAct constructor that requires tools would turn a valid user request into a startup error.

## 4. Cross-product synthesis

Mainstream agents make it useful to keep three controls orthogonal:

1. The model-visible tool set determines which capability schemas the model can request.
2. Permission rules decide whether a specific requested call is allowed, denied, or needs confirmation.
3. Sandbox and workspace controls bound the operating-system effects of an allowed call.

**Synthesis:** An invocation selector is safest when it can only remove the first layer. It should neither grant an absent approval nor relax a sandbox. Unknown names should fail before a provider request, because silently ignoring a typo can broaden the effective capability surface.

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The observed Claude Code help does not specify a precedence rule when the same whole tool is in both `--allowedTools` and `--disallowedTools`; it is not evidence for an undocumented precedence claim.
- **Evidence gap:** The observed Codex CLI help documents sandbox and approval controls but does not document a public per-invocation whole-tool allowlist. No equivalence with Claude's `--tools` is inferred.
- **Synthesis:** Glob and argument patterns are valuable for command-specific policy but are an unsafe first format for an extension-rich tool registry: naming rules, escaping, and MCP-generated names all need a separate compatibility contract.
- **Synthesis:** Persisting an invocation selector into a session would change later resumes unexpectedly. A process-local selector is easier to reason about, while durable policy should remain an explicit configuration action.

## References

- Claude Code CLI 2.1.220, `claude --help`, observed locally on 2026-08-10.
- Codex CLI 0.146.0, `codex --help`, observed locally on 2026-08-10.
- [OpenAI Codex documentation](https://developers.openai.com/codex/), accessed 2026-08-10.
- [OpenCode permissions documentation](https://opencode.ai/docs/permissions/), updated 2026-08-09 and accessed 2026-08-10.
