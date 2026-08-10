# MCP OAuth lifecycle: industry practice

> Status: research note, not an implementation plan.
> Research date: 2026-08-10; re-verify before adoption.
> Scope: OAuth lifecycle behavior for remote MCP clients: discovery, client registration, browser callbacks, token use, persistence, refresh, and revocation.
> Out of scope: a local client design, a credential-store choice, server-side authorization implementation, and provider-specific identity configuration.

## 1. Conclusions

- **Synthesis:** An MCP OAuth command is a lifecycle rather than a one-time browser launch. It must connect a protected MCP resource to authorization-server discovery, client registration, authorization-code/PKCE handling, token use bound to the MCP resource, and a durable logout/revocation story. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) and [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html) (accessed 2026-08-10).
- **Synthesis:** Coding-agent CLIs present explicit login and logout controls because configured transport and authenticated availability are distinct. Codex exposes `mcp login <name>` with optional scopes; Claude Code exposes `login` and `logout`; OpenCode documents automatic OAuth on a protected remote server and separate auth status/logout commands. [Codex repository](https://github.com/openai/codex), [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** Browser callbacks are a security boundary. A native client should use authorization code plus PKCE, validate state and issuer when advertised, and bind the request to the MCP resource; merely copying a redirect URL or code into configuration omits those protections. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) and [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html) (accessed 2026-08-10).

## 2. Evidence from deployed applications

- **Fact:** Installed Codex CLI 0.146.0 documents `mcp login <name>` as authentication with an MCP server using OAuth and accepts optional comma-separated scopes. Its remote add command separately supports an OAuth client ID and resource parameter. This is observable shipped behavior recorded locally on 2026-08-10. [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).
- **Fact:** Installed Claude Code 2.1.220 lists `mcp login <name>` for HTTP, SSE, or connector authentication and `mcp logout <name>` to clear stored OAuth credentials. This is observable shipped behavior recorded locally on 2026-08-10. [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Fact:** OpenCode documents automatic OAuth authorization when a remote server returns `401`, plus `mcp auth list`, per-server authentication, and logout controls. Its remote configuration distinguishes URL, optional headers, OAuth configuration, enablement, and timeout. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Fact:** The MCP authorization specification requires a client to use protected-resource metadata to discover authorization servers and authorization-server metadata for server configuration. It says MCP clients must implement resource indicators, including the resource parameter in both authorization and token requests. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) (accessed 2026-08-10).
- **Fact:** The MCP specification says access tokens must use the `Authorization: Bearer` header and must not appear in URI query strings. It requires clients to implement PKCE and calls for secure token storage. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) (accessed 2026-08-10).
- **Fact:** RFC 9700 says public clients must use PKCE for authorization-code protection and recommends the `S256` challenge method. It describes strict redirect URI handling and refresh-token protection as security requirements. [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html) (published 2025-01; accessed 2026-08-10).

## 3. Mechanisms and tradeoffs

- **Synthesis:** Automatic authorization on a `401` is convenient in an interactive client but needs a non-interactive failure mode. Otherwise a runtime, CI invocation, or background turn can unexpectedly wait for browser completion. OpenCode's documented automatic flow and Codex/Claude's explicit commands show both automation and user control are useful. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/), [Codex repository](https://github.com/openai/codex), and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).
- **Synthesis:** Dynamic client registration lowers setup friction but creates registered-client state and does not remove the need to persist or otherwise recover client identity and token refresh context. A pre-registered client has less discovery flexibility but is necessary for providers that do not offer dynamic registration. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) (accessed 2026-08-10).
- **Synthesis:** Refresh-token support is only useful when the client can protect, rotate, persist, and revoke the updated token. A client that stores only an access token can temporarily connect but will lose continuity at expiry and cannot offer a truthful logout/recovery contract. [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html) and [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) (accessed 2026-08-10).
- **Synthesis:** Auth inspection should report state and credential source without printing tokens, authorization codes, callback query parameters, client secrets, or refresh tokens. OpenCode documents auth status separately, and Claude documents explicit credential clearing. [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp) (accessed 2026-08-10).

## 4. Cross-product synthesis

- **Synthesis:** A complete remote-MCP OAuth flow has these observable phases: user requests login; client discovers protected-resource and authorization-server metadata; it obtains a client identity; it launches or prints an authorization URL with state, PKCE, and resource; it validates the callback; it exchanges the code; it stores only protected credentials; future runtime connections refresh and use them; status is redacted; logout clears or revokes them. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html), and [OpenCode MCP servers](https://opencode.ai/docs/mcp-servers/) (accessed 2026-08-10).
- **Synthesis:** A remote endpoint configured with an environment-sourced bearer token has a distinct lifecycle from OAuth. It can safely avoid token persistence, registration, browser callbacks, and refresh, but it cannot honestly be labeled OAuth login/logout. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) and [Codex repository](https://github.com/openai/codex) (accessed 2026-08-10).

## 5. Pitfalls and evidence gaps

- **Fact:** MCP authorization requires clients to use metadata discovery and resource indicators; a generic OAuth endpoint/client-id form without that discovery does not prove protocol conformance. [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization) (accessed 2026-08-10).
- **Evidence gap:** Direct retrieval of the official Codex developer MCP page returned HTTP 403 on 2026-08-10, and direct Claude Code documentation retrieval reset during the same research. Installed CLI help establishes public command topology, but not their private credential storage or callback implementation.
- **Evidence gap:** Product documentation establishes OpenCode's automatic flow and command surface, but does not prove which OS credential store, browser callback transport, or token encryption mechanism it uses on every platform.
- **Evidence gap:** RFC 9700 and MCP rules specify security outcomes, not a portable secure-storage API. An implementation must still define failure behavior on hosts without a usable keychain and must not silently downgrade to world-readable storage.

## References

- Model Context Protocol, [Authorization specification, 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), accessed 2026-08-10.
- IETF, [RFC 9700: Best Current Practice for OAuth 2.0 Security](https://www.rfc-editor.org/rfc/rfc9700.html), published 2025-01, accessed 2026-08-10.
- OpenAI Codex CLI 0.146.0, locally observed `codex mcp login --help`, 2026-08-10; [Codex repository](https://github.com/openai/codex).
- Claude Code CLI 2.1.220, locally observed `claude mcp --help`, 2026-08-10; [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).
- OpenCode, [MCP servers](https://opencode.ai/docs/mcp-servers/), accessed 2026-08-10.
