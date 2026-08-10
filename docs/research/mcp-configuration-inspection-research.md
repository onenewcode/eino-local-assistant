# MCP configuration inspection: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: read-only inspection of configured MCP servers in coding-agent CLIs, including selection, output shape, process side effects, and secret exposure.
> Out of scope: adding or removing server configuration, project-scoped trust, transport authentication, health checks, and local product design.

## 1. Conclusions

- **Synthesis:** Mainstream coding-agent CLIs expose both a collection view and a named-server view. This gives scripts a stable lookup path without forcing them to parse a complete registry. Codex exposes `mcp list` and `mcp get <name>`; Claude Code exposes the same command nouns. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Configuration inspection and connectivity inspection are separate operations. Codex's observed `get` displays saved transport fields, while Claude documents health checking for approved servers; clients should label whether output is static configuration or a live status rather than treating the two as interchangeable. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Read-only output must not inadvertently disclose credentials. OpenCode documents environment-backed MCP configuration and separate authentication management, which supports exposing server identity/transport while keeping environment values and stored tokens out of ordinary inspection output. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** The installed Codex CLI 0.146.0 lists `mcp list`, `mcp get`, `mcp add`, `mcp remove`, `mcp login`, and `mcp logout`. Its `mcp get <name> --json` returns a single named configuration object; its text form reports enabled state, transport and configured transport fields. This is observable shipped behavior, recorded locally on 2026-08-10. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** The installed Claude Code CLI 2.1.220 lists `mcp list` and `mcp get <name>`. Its help says list and get show unapproved project servers as pending and health-check approved servers. This is observable shipped behavior, recorded locally on 2026-08-10. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Fact:** OpenCode documents named local and remote MCP configuration, automatic tool availability after adding a server, and command-level authentication/status management. Its local configuration example supplies the subprocess command and environment separately. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** `get <name>` is a deterministic selection boundary when names are unique. It avoids a consumer needing to choose among multiple list entries and makes a missing configuration an explicit error rather than an empty result. Codex and Claude expose this selection form. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** A JSON single-object response is suited to automation, while a vertical text response makes transport fields scannable for an operator. Codex presents both output styles through its `--json` switch. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Synthesis:** Health checking can improve diagnosis but turns an inspection command into a network/process action and may need trust or approval state. Claude's help distinguishes approved from unapproved project servers; a static view remains valuable where avoiding those side effects is the priority. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The shared `list`/`get` command topology is a predictable minimal management surface. Products differ over whether a lookup also includes health state: Codex's observable configuration output is transport-focused, while Claude advertises approval-aware health checks. [Codex repository](https://github.com/openai/codex) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** An operator-facing view should preserve identity, enabled state, and transport metadata while treating authentication material as a separate lifecycle. OpenCode's documented OAuth/login/logout flow illustrates why a generic server detail response should not become a credential dump. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Evidence gap:** The official Codex developer documentation endpoint returned HTTP 403 during this research. The observed CLI behavior is strong evidence for its command contract, but it does not reveal undocumented internal storage or health-check behavior.
- **Evidence gap:** Direct retrieval of the Claude Code MCP web page timed out on 2026-08-10. Claude-specific claims above are limited to its locally installed 2.1.220 CLI help and linked documentation location.
- **Fact:** OpenCode's MCP page documents both local and remote/OAuth paths; it does not state that every CLI's `get` command uses the same output fields or live-check policy. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A status field without a freshness or side-effect contract can mislead users. A client should distinguish saved `enabled` configuration from a connection that was actually tested, especially for external services.

## References

- OpenAI Codex CLI 0.146.0, locally observed `codex mcp --help`, `codex mcp get --help`, and `codex mcp get openaiDeveloperDocs --json`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp --help` and `claude mcp get --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), last updated 2026-08-09, accessed 2026-08-10.
