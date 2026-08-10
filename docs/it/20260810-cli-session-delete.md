# CLI permanent session deletion

Date: 2026-08-10

## Goal

Provide an explicit headless command for permanently deleting an inactive saved session. The store already had a deletion primitive, but the root CLI had no discoverable lifecycle command and the primitive did not verify that a durable session was inactive after acquiring its writer lock.

## Research basis

See [session deletion research](../research/session-deletion-research.md). The locally installed Codex CLI `0.146.0` exposes `codex delete <SESSION>` with `--force`. This implementation adopts the explicit-ID and explicit-confirmation boundary, while deliberately keeping the repository's existing ID-only selector model rather than inventing a picker, name lookup, or archive state.

## Implementation

- Add `eino delete <SESSION_ID> --yes`; `--force` is an equivalent explicit confirmation spelling compatible with Codex's visible UX.
- The command opens only the configured durable store. It never starts the TUI or a model and prints the deleted ID only after the journal was removed.
- `ThreadStore.DeleteThread` now replays state and lifecycle events after acquiring the writer lock. It rejects active turns and pending compactions, preserving their journals for normal recovery or terminalization.
- The command intentionally has no implicit `--last` path and deletion is permanent. Users can inspect IDs with `eino sessions` and preserve a transcript with `eino export` first.

## Verification

- Store tests cover refusal for active turns and pending compactions, and confirm the journal remains readable.
- CLI tests cover help discovery, missing-confirmation refusal, `--yes`, `--force`, durable removal, and lifecycle error propagation.
- Delivery validation runs `git diff --check`, `go test ./...`, `go build ./...`, and `go tool golangci-lint run ./...`.
