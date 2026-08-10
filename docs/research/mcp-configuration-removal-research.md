# MCP configuration removal: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: explicit removal of a configured MCP server from coding-agent CLI configuration, including selection, scope, confirmation, and process side effects.
> Out of scope: local file-editing design, remote server deletion, credential revocation, project trust, and MCP transport behavior.

## 1. Conclusions

- **Synthesis:** A named `mcp remove <name>` operation is a standard configuration-management primitive alongside list/get/add. It makes deletion an explicit user action rather than overloading enablement or forcing manual configuration edits. Codex and Claude Code both expose this command form. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Removal and logout solve different lifecycle problems. Codex exposes separate `remove` and `logout` commands, while OpenCode separately documents removing stored authentication credentials; deleting a saved server should not be represented as proof that external credentials were revoked. [Codex repository](https://github.com/openai/codex) and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A command that changes configuration scope must make the target predictable. Claude Code documents user, project, and local scopes for removal; a client with one user-owned configuration scope can retain predictable behavior by naming that scope rather than guessing among files. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** The installed Codex CLI 0.146.0 lists `mcp remove <name>` with the description “Name of the MCP server configuration to remove.” Its command help does not expose a confirmation flag. This is observable shipped behavior, recorded locally on 2026-08-10. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** The installed Claude Code CLI 2.1.220 lists `mcp remove [options] <name>`. Its help says the optional `--scope` selects local, user, or project configuration and otherwise removes from the scope where the server exists. This is observable shipped behavior, recorded locally on 2026-08-10. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Fact:** OpenCode documents configuration-level `enabled: false` for temporarily disabling a server without removing it, and a distinct `mcp logout <server>` for deleting stored OAuth credentials. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** Disable preserves command/credential references for later reactivation; remove eliminates the selected configuration entry. OpenCode documents the former, while Codex and Claude expose the latter as a named CLI action. They have different recovery and audit expectations. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/), [Codex repository](https://github.com/openai/codex), and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Explicit command selection is an authorization boundary: a short, named removal request is easier for humans and automation to review than arbitrary configuration text editing. The tradeoff is that clients need a clear missing-name failure rather than silently treating it as successful deletion. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** A configuration operation should not imply a runtime teardown contract for another process. The documented product commands manage saved configuration; neither cited command help establishes that an already-running agent instantly unloads an active server. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The common management surface separates four states: configured/enabled, configured/disabled, removed configuration, and logged-out credentials. Codex's separate remove/logout commands and OpenCode's documented enable/logout controls make those boundaries visible. [Codex repository](https://github.com/openai/codex) and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Scope is the main product-level variation. Claude exposes multiple source locations, whereas a single user-owned configuration has one fixed target and should state that fact. Removing the ambiguity is more important than matching every flag name. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The official Codex developer documentation endpoint returned HTTP 403 during this research. The locally observed CLI help proves the public command shape, not its internal atomic-write, comment-preservation, or lock behavior.
- **Evidence gap:** Direct retrieval of the Claude Code MCP web page timed out on 2026-08-10. Claude-specific claims above are limited to the locally installed 2.1.220 CLI help and linked documentation location.
- **Fact:** OpenCode documents OAuth logout, but its MCP page does not establish a universal expectation for whether remove also revokes every kind of credential. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Removing a server from configuration cannot recall data already sent to that server or stop a different live process unless the client has an explicit process/session lifecycle contract.

## References

- OpenAI Codex CLI 0.146.0, locally observed `codex mcp --help` and `codex mcp remove --help`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp --help` and `claude mcp remove --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), last updated 2026-08-09, accessed 2026-08-10.
