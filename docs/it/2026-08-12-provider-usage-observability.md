# Iteration: provider usage observability

## Scope

Add one structured process-log event for each provider usage snapshot emitted
during a chat turn.

## Alignment evidence

- `docs/research/context-window-status-display-research.md` distinguishes the
  latest API usage snapshot from the local next-request estimate. The event
  records provider counts and the configured full context window, so operators
  can correlate the same snapshot shown by `/context` or a status bar without
  treating planner budgets as the model window.
- Existing `docs/logging.md` requires stable message vocabulary and forbids
  prompt/tool bodies in process logs. `model usage recorded` follows that
  contract and emits only numeric counts, IDs, operation, and availability.
- Evidence gap: no network lookup was available in this iteration; the local
  research notes are the authoritative references used here.

## Change

`internal/chat` now emits `model usage recorded` after normalizing each usage
event and before durable recording. The JSONL event includes
`prompt_tokens`, `completion_tokens`, `total_tokens`, `cached_tokens`,
`reasoning_tokens`, and `context_window_tokens`, plus `call_id`, operation, and
whether provider usage was available. Message and tool payloads remain absent.

## Verification

- `go test ./internal/chat ./internal/logging`
- Full repository gates remain for the integrating agent.
