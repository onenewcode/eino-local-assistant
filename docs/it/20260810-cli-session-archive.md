# CLI reversible session archive

Date: 2026-08-10

## Goal

Provide the non-destructive session lifecycle commonly exposed by coding-agent CLIs. This keeps completed work out of the normal resume/last-session path without treating organization as permanent deletion.

## Research basis

See [session archive research](../research/session-archive-research.md). Locally observable Codex CLI `0.146.0` exposes symmetric `archive <SESSION>` and `unarchive <SESSION>` commands distinct from `delete`. This iteration implements the explicit-ID subset, retaining the repository's current selector model rather than claiming a picker or title selector that is not available in the active command tree.

## Implementation

- Add `eino archive <SESSION_ID>` and `eino unarchive <SESSION_ID>` as headless lifecycle commands. They do not start the TUI or a model.
- Persist archive state as hash-chained `thread.archived` / `thread.unarchived` journal events; no journal, transcript, checkpoint, artifact, or usage data is moved or deleted.
- Normal `ListThreads`, its read-only variant, TUI session listing, and `--last` selectors see only active sessions. `eino sessions --archived` provides the explicit archived view and preserves `archived_at` in JSON.
- Explicit resume and start-turn paths reject archived sessions with a recognizable store error. Forking an archived source is also rejected. Export and confirmed delete remain able to access the retained journal.
- Archive and unarchive acquire the same thread writer lock as turn mutations, check revision CAS, and refuse active turns or pending compactions. This prevents an archival transition from racing a durable writer or unresolved provider operation.

## Verification

- Store tests cover journal replay, active/archived list separation, read access, start-turn and fork rejection, duplicate transitions, unarchive recovery, and active/pending lifecycle refusal.
- CLI tests cover root/help discovery, archive/unarchive output, `sessions --archived` JSON, normal-list exclusion, and command-level active-turn error propagation.
- Chat tests verify explicit resume rejects an archived session before recovery or model work starts.
- Delivery validation runs `git diff --check`, `go test ./...`, `go build ./...`, and `go tool golangci-lint run ./...`.
