# MCP server enablement: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: preserving a configured MCP server while controlling whether it is activated for future coding-agent runtimes.
> Out of scope: live process control, project approval workflows, tool-level filters, remote authentication, and local product design.

## 1. Conclusions

- **Synthesis:** Enablement is a reversible configuration state, distinct from deletion. OpenCode documents `enabled: false` specifically for temporarily disabling a server without removing it, preserving the command and credentials for later use. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Static enablement must not be confused with a live connection state. A configuration value controls future activation; disconnecting an already-running process or checking health needs a separate lifecycle and observable status contract. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [Model Context Protocol transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Synthesis:** Product surfaces vary: some clients expose enablement in configuration/status rather than a dedicated subcommand. A CLI can offer a clear configuration edit as long as it states that scope and does not imply an unimplemented live-control capability. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** OpenCode documents `enabled: false` as temporary server disablement without deleting configuration. It also documents `enabled` as a local-server startup option and recommends limiting enabled MCP servers because tools add to model context. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).
- **Fact:** The installed Codex CLI 0.146.0 reports per-server `enabled` and `disabled_reason` in `mcp list --json`, but its observed `mcp` command group has no dedicated server enable/disable subcommand. Its global `--enable`/`--disable` flags refer to Codex feature flags, not an observed server-toggle command. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10; locally observed CLI behavior).
- **Fact:** The installed Claude Code CLI 2.1.220 documents approved and unapproved project MCP servers in `mcp list`/`get`, but its observed MCP command group has no dedicated server enable/disable subcommand. This establishes a separate approval/pending state, not a claim about generic configuration enablement. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10; locally observed CLI behavior).
- **Fact:** The MCP Streamable HTTP transport specification says a client that no longer needs a session should explicitly terminate it with HTTP DELETE where supported. This is an HTTP transport lifecycle recommendation, not an enablement setting for local subprocess configuration. [Model Context Protocol transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (version 2025-06-18; accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** A boolean enablement field has low cognitive cost and supports fast rollback, but it creates two user-visible reasons for a missing tool: disabled configuration or failed discovery. A status surface must expose the saved value without conflating it with health. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Disabling reduces startup work and tool-schema context use while retaining potentially sensitive environment references in local configuration. Deleting configuration removes the reference but costs setup/recovery time; the two operations should remain separately named. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A command that edits configuration after another agent process started cannot guarantee immediate effects on that process. Making that limit explicit avoids an operator assuming tool calls were revoked or a subprocess was terminated. [Model Context Protocol transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The products distinguish at least three concepts: saved configuration enablement (OpenCode), current per-server status visibility (Codex), and approval/pending state for project servers (Claude Code). They should not be flattened into one ambiguous “off” state. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/), [Codex repository](https://github.com/openai/codex), and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** The smallest predictable lifecycle surface lets users retain a server configuration, make a future-startup choice, inspect the saved state, and separately remove the configuration. Connection and authentication workflows can then be added without changing the meaning of enablement. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The official Codex developer documentation endpoint returned HTTP 403 during this research. Locally observed CLI output proves its current visible fields, not how it persists disabled state or reacts in a live process.
- **Evidence gap:** Direct retrieval of the Claude Code MCP web page timed out on 2026-08-10. Claude-specific claims above are limited to the locally installed 2.1.220 CLI help and linked documentation location.
- **Fact:** OpenCode's documentation covers its own configuration semantics; it does not establish an industry-wide subcommand name or an immediate session-stop guarantee. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** An enable/disable operation should be idempotent and fail clearly for an unknown name, otherwise scripts cannot distinguish a completed state change from a typo.

## References

- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), last updated 2026-08-09, accessed 2026-08-10.
- OpenAI Codex CLI 0.146.0, locally observed `codex mcp --help` and `codex mcp list --json`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- Model Context Protocol, [Transports specification, 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports), accessed 2026-08-10.
