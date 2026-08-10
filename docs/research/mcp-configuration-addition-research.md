# MCP configuration addition: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: adding explicitly named local stdio MCP server configuration from coding-agent CLIs, including command framing, environment variables, transport limits, and immediate side effects.
> Out of scope: remote HTTP/SSE configuration, OAuth, project-scope trust, server discovery, local TOML writer design, and credential-storage policy.

## 1. Conclusions

- **Synthesis:** Coding-agent CLIs use an explicit server name and command boundary for local MCP addition. Codex requires `mcp add <name> -- <command>...` for stdio, while Claude documents a named command plus trailing arguments and examples that use `--` to separate subprocess flags. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Environment input belongs to the local-process configuration boundary, not normal command output. Both Codex and Claude expose repeatable environment-variable input, while OpenCode's local server configuration has a separate environment object. This makes a command useful without requiring process startup at add time. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Transport selection should be explicit. Codex distinguishes a local command from `--url`; Claude offers stdio/SSE/HTTP transport selection; a stdio-only client should accept only the command form and not accept URL-looking input as an unsupported promise. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** The installed Codex CLI 0.146.0 documents `mcp add <name> (--url <url> | -- <command>...)`. Its `--env KEY=VALUE` option is “only valid with stdio servers.” This is observable shipped behavior, recorded locally on 2026-08-10. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** The installed Claude Code CLI 2.1.220 documents `mcp add [options] <name> <commandOrUrl> [args...]`, repeatable `-e/--env`, and examples for stdio commands, HTTP, and SSE. Its transport option defaults to stdio. This is observable shipped behavior, recorded locally on 2026-08-10. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Fact:** OpenCode documents local MCP configuration as a named `type: "local"` entry with a command array, optional environment object, working directory, enablement control, and a tool-fetch timeout. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** A `--` separator removes ambiguity between CLI flags and subprocess arguments. It lets users pass flags such as `--stdio` to the child command without making the agent CLI guess which parser owns them. Codex's command syntax and Claude's examples use this boundary. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** `KEY=VALUE` is a direct, portable environment representation, but values can end up in shell history and persisted configuration. The interface is ergonomic for a local subprocess while requiring documentation and redacted normal output. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Configuring a server and connecting it are distinct states. OpenCode documents automatic tool availability after a configured server is added, but its server options also include enablement and discovery timeout; a client should not report a successful configuration edit as a successful connection. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The shared minimum contract is: select an identity, unambiguously frame a local command, persist optional process environment, and defer external effects to the separate runtime lifecycle. Codex and Claude expose command-line forms for this; OpenCode exposes the equivalent configuration structure. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Feature parity is not accepting every transport flag. A client can make its supported stdio path predictable while leaving HTTP/SSE/OAuth commands absent and documented as unsupported, rather than silently writing an unusable configuration. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The official Codex developer documentation endpoint returned HTTP 403 during this research. The locally observed CLI help establishes current command syntax and flags, but not hidden validation, persistence, or secret-redaction internals.
- **Evidence gap:** Direct retrieval of the Claude Code MCP web page timed out on 2026-08-10. Claude-specific claims above are limited to the locally installed 2.1.220 CLI help and linked documentation location.
- **Fact:** OpenCode documents working-directory and timeout options for local server configuration, but this does not establish that every CLI add command presents identical flags. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A successful add operation only proves a configuration was accepted. Executable availability, protocol handshake, tool discovery, and permissions must remain independently observable at runtime.

## References

- OpenAI Codex CLI 0.146.0, locally observed `codex mcp add --help`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp add --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), last updated 2026-08-09, accessed 2026-08-10.
