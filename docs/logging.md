# Process logging and observability

> Operational diagnostics only. Durable conversation truth remains the session
> JSONL ledger under `<data_dir>/sessions/`.

## Goals

- Use the standard library `log/slog` for structured process logs.
- Persist logs while the CLI runs so failures after a TUI crash remain greppable.
- Keep high-signal Info events; never mirror user prompts or full tool
  argument bodies into process logs (privacy + noise).
- Leave stderr off by default so Bubble Tea is not corrupted.

## Where logs go

| Setting | Default |
| --- | --- |
| Directory | `<storage.data_dir>/logs` (usually `~/.eino-assistant/logs`) |
| File name | `eino-YYYY-MM-DD.log` (UTC day) |
| Encoding | JSON lines (`logging.format = "json"`) |
| Level | `info` (`EINO_LOG_LEVEL` overrides `logging.level`) |
| Retention | 7 daily files (`logging.retention_days`) |

Example:

```sh
# One-shot debug session with mirrored stderr (headless-friendly)
EINO_LOG_LEVEL=debug eino exec "summarize README"
tail -F ~/.eino-assistant/logs/eino-$(date -u +%F).log
```

## Configuration

See `[logging]` in `config.example.toml`:

```toml
[logging]
enabled = true
level = "info"
# dir = "/custom/path"
format = "json"
stderr = false
retention_days = 7
```

## Package map

| Concern | Location |
| --- | --- |
| slog setup, file sink, context helpers | `internal/logging` |
| TOML `[logging]` | `internal/config.LoggingConfig` |
| Open/close + process default | `cmd/eino-assistant` `openRuntimeLogger` |
| Turn lifecycle | `internal/chat` (`turn started` / `completed` / `failed`) |
| ReAct model steps + tool batches | `internal/agent` |
| Per-tool start/end (sizes only) | `toolEventCallback` in `internal/agent` |
| Compaction lifecycle | `internal/chat` compact paths |

## Event vocabulary (stable msg strings)

Prefer filtering on `msg` + `component`:

| msg | component | Notes |
| --- | --- | --- |
| `runtime started` / `runtime closing` | `runtime` | Session open/close |
| `turn started` / `turn completed` / `turn failed` | `chat` | No input text |
| `model step started` / `completed` / `failed` | `agent` | `kind=tool_enabled\|final` |
| `tool batch started` / `completed` / `failed` | `agent` | Batch boundary |
| `tool started` / `completed` / `error` | `agent` | `input_bytes` / `output_bytes` only |
| `compaction started` / `completed` / `failed` | `context` | Token release metrics on success |

Common attrs: `session_id`, `turn_id`, `model`, `duration_ms`, `step`,
`tool`, `tool_call_id`.

## Boundaries

- **Session ledger** (`internal/store`): product resume, usage, tool artifacts.
- **Process logs** (`internal/logging`): operator diagnostics; best-effort file I/O.
- **TUI status/usage**: user-visible live metrics; not a substitute for file logs.
- Future OpenTelemetry export is intentionally out of scope here; start with
  local JSON files first (same posture as many local coding agents).
