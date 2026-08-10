# Remote MCP transport: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: how coding-agent clients configure and operate remote MCP endpoints, including transport selection, session lifecycle, credentials, and discovery failure boundaries.
> Out of scope: local client design, server implementation, credential-store selection, and OAuth protocol implementation details.

## 1. Conclusions

- **Synthesis:** Remote MCP is an explicit transport choice, not a URL-shaped variant of a local process command. Codex exposes `--url` separately from a stdio command; Claude Code requires an HTTP or SSE transport selection; OpenCode requires a remote type and URL. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A remote session has a lifecycle beyond the initial `tools/list`: the Streamable HTTP protocol permits JSON or SSE responses, carries a server session identifier across requests, and recommends an explicit HTTP `DELETE` when the client is done. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Synthesis:** Authentication is a separate product boundary. Mainstream clients expose bearer-token, header, or OAuth controls, but those controls require secret redaction, persistence, browser/callback handling, and recovery semantics that are not implied by simply connecting to an unauthenticated endpoint. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** Installed Codex CLI 0.146.0 documents `mcp add <name> (--url <URL> | -- <COMMAND>...)`; it describes `--url` as a Streamable HTTP server and offers a bearer-token environment-variable option and OAuth options only for that transport. This is observable shipped behavior recorded locally on 2026-08-10. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** Installed Claude Code 2.1.220 documents `mcp add --transport http` examples, supports stdio, SSE, and HTTP transport labels, and exposes header plus OAuth client options. This is observable shipped behavior recorded locally on 2026-08-10. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Fact:** OpenCode's MCP-server documentation says it supports local and remote servers. Its remote configuration requires `type: "remote"` and `url`, and documents optional headers, OAuth, enablement, and tool-fetch timeout fields. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Fact:** The MCP Streamable HTTP specification describes POST request delivery with JSON or SSE response options. It specifies `Mcp-Session-Id` reuse when assigned by a server and says a client that no longer needs a session should send `DELETE` to terminate it. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** A URL-only endpoint is a useful unauthenticated baseline because it creates an observable connection/discovery path without committing to a secret-storage model. The tradeoff is that protected endpoints remain intentionally unavailable until authentication is designed as a full user flow. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** The protocol supports stateful and sessionless server behavior. When a server supplies `Mcp-Session-Id`, clients must include it in subsequent requests; a sessionless server does not create that header requirement. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Synthesis:** Connection and tool discovery need a bounded failure mode even for remote endpoints. OpenCode documents a fetch timeout, while the protocol describes reconnect and session recovery mechanics but does not prescribe a CLI startup wait budget. A client therefore needs a visible product timeout policy instead of inheriting an unbounded network wait. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Synthesis:** Static headers and URL query credentials have different leakage paths, but both are sensitive configuration. Product surfaces that list or inspect server configuration must avoid turning those fields into diagnostic output. Codex's bearer option is an environment-variable name rather than a literal token, while OpenCode separately documents headers and OAuth. [Codex repository](https://github.com/openai/codex) and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** The common remote-MCP control flow is: declare a named remote transport and endpoint; establish a protocol session; discover tools under a time bound; expose approved tools to the agent; and explicitly close the session. Configuration success, protocol availability, authentication, and tool-call approval are distinct states. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports), [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/), and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Authentication needs an independently testable lifecycle: initial unauthenticated response, user-facing login or credential lookup, durable secure storage, redacted inspection, expiry/revocation, and logout. The accessible product documentation establishes these controls exist but does not justify treating raw configuration as a secure credential store. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Fact:** The Streamable HTTP specification recommends server authentication for all connections, but it does not define a coding-agent credential UI, local secret storage, or a single retry budget. [MCP transports specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) (accessed 2026-08-10).
- **Evidence gap:** Direct retrieval of the official Codex developer MCP page returned HTTP 403 on 2026-08-10. The locally observed CLI help establishes public flags, not Codex's internal secret handling or retry behavior.
- **Evidence gap:** Direct retrieval of the Claude Code MCP web page reset during research on 2026-08-10. Claude-specific assertions above are limited to the locally installed 2.1.220 CLI help and the linked documentation location.
- **Evidence gap:** OpenCode documents remote OAuth, headers, and diagnostic commands, but its documentation does not prove the internal token-storage or browser callback details of every supported provider.

## References

- Model Context Protocol, [Transports specification, 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports), accessed 2026-08-10.
- OpenAI Codex CLI 0.146.0, locally observed `codex mcp add --help`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp add --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), accessed 2026-08-10.
