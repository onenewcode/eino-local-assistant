# MCP runtime lifecycle: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: lifecycle boundaries for configured MCP servers in coding-agent clients: activation, discovery, tool exposure, time bounds, and user control.
> Out of scope: a local product design, MCP server implementation, remote OAuth design, and permission-policy details not documented by the cited products.

## 1. Conclusions

- **Synthesis:** A practical MCP client treats server configuration as an activation boundary: a configured server can be disabled without removal, and an enabled server becomes a source of tools during agent startup. OpenCode documents both the boolean control and automatic availability of discovered tools. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Discovery requires an explicit latency boundary. OpenCode exposes a per-server tool-fetch timeout with a five-second default; a client should surface an equivalent bounded startup/failure behavior rather than leaving the agent indefinitely blocked on an external process. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Tool exposure is also a context-budget decision. OpenCode warns that MCP tools add schema/context tokens and recommends enabling only needed servers; server lifecycle control alone is insufficient when many tools are installed. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** OpenCode's current MCP-server documentation states that it supports local and remote MCP servers, and that added MCP tools are automatically available to the LLM alongside built-in tools. It also documents a unique configuration name for each server. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).
- **Fact:** OpenCode documents `enabled: false` as a way to temporarily disable a configured server without removing it. Its local-server options include command, working directory, environment, `enabled`, and a `timeout` for fetching tools that defaults to 5,000 ms. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (last updated 2026-08-09; accessed 2026-08-10).
- **Fact:** The Model Context Protocol's 2025-06-18 transport specification describes a transport-agnostic JSON-RPC lifecycle and specifies session handling for Streamable HTTP; it is protocol evidence rather than a statement about a particular client UI. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (version 2025-06-18; accessed 2026-08-10).
- **Fact:** Codex's public repository directs configuration readers to the official Codex configuration documentation. It is evidence that configuration is a public product surface, but the repository page does not establish a detailed MCP startup or failure contract. [Codex configuration entrypoint](https://github.com/openai/codex/blob/main/docs/config.md) (accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** An `enabled` flag separates retaining credentials/command details from spending startup time and context on a server. The benefit is reversible control; the cost is that a disabled configuration needs clear status reporting so an unavailable tool is not mistaken for an outage. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A per-server discovery timeout makes startup failure attributable to one external dependency. A short default limits waiting, while a configurable value accommodates slow local processes; a fixed universal timeout risks either false failures or stalled interactive startup. OpenCode exposes this value rather than hiding it. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Automatic registration gives one agent tool namespace, but each discovered schema consumes context. OpenCode's documented global and per-agent tool filtering illustrates the complementary control needed when server-level enabling is too coarse. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Fact:** For remote Streamable HTTP servers, the MCP transport specification says clients that no longer need a session should send an HTTP DELETE request to explicitly terminate it, subject to server support. This is a protocol lifecycle recommendation; local-process cleanup is transport-specific. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The accessible product evidence favors a split between a side-effect-free configuration/status view and an execution lifecycle that connects, discovers, exposes, and later disposes of tools. OpenCode documents startup enablement and tool availability, while the MCP specification describes explicit session termination for its HTTP transport. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Synthesis:** Namespacing and filtering address distinct risks. OpenCode prefixes registered MCP tools with the server name for server-wide matching, while its documentation separately warns about schema/context growth. A robust client should not use either mechanism as a substitute for the other. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Fact:** OpenCode's page covers both remote OAuth and local servers, but its documented timeout is specifically described as time for fetching tools; it does not prove a universal timeout or retry policy for every connection phase. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Evidence gap:** Direct retrieval of the Claude Code MCP documentation timed out on 2026-08-10, so no Claude-specific behavior is asserted here.
- **Evidence gap:** Direct retrieval of the official Codex developer MCP page returned HTTP 403 on 2026-08-10. The public Codex repository's configuration entrypoint is retained only as limited evidence; it does not substitute for the inaccessible official behavior documentation.
- **Evidence gap:** The cited MCP transport specification covers protocol obligations, not user-facing approval prompts, process supervision, reconnect policy, or the behavior of individual stdio SDKs. Those require separate product or implementation evidence.

## References

- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), last updated 2026-08-09, accessed 2026-08-10.
- Model Context Protocol, [Transports specification, 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports), accessed 2026-08-10.
- OpenAI Codex, [configuration documentation entrypoint](https://github.com/openai/codex/blob/main/docs/config.md), accessed 2026-08-10.
