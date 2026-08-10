# CLI session display-name selectors

Date: 2026-08-10

## Goal

Make saved sessions selectable by their existing durable display name without weakening stable ID workflows. This restores a documented session affordance and aligns shell lifecycle commands with the observable Codex and Claude Code session model.

## Research basis

See [session display-name selection research](../research/session-name-selection-research.md). Codex `0.146.0` directly accepts a UUID or session name and gives UUIDs precedence; Claude Code `2.1.220` exposes `--name` as a durable display label in resume-oriented UI. The implementation takes the safe non-picker subset: full exact names, no fuzzy lookup, and explicit ambiguity failure.

## Implementation

- Add `-n` / `--name` to bare interactive startup, `chat`, and `fork`. It aliases the existing durable `ThreadMeta.Title`; `--title` and `--name` cannot be combined. Fork names are written only to the newly published child and never mutate the source.
- Add one resolver for ID-or-name selectors. Exact IDs always win, including when an unrelated session has the same title. Names are case-sensitive complete matches; duplicate names report every matching ID and do not run an action.
- Resolve active names before TUI and headless resume/fork startup, so source model inheritance, ephemeral snapshot protection, and the actual open/fork operation all use the canonical ID.
- Resolve name selectors for archive (active scope), unarchive (archived scope), and export/delete (all scopes). Explicit IDs still reach the lifecycle operation even when they refer to an archived session, preserving existing recognizable archive errors.
- Update usage/help text to say `SESSION_ID_OR_NAME`; `--last` remains a separate explicit newest-session selector and never routes through name matching.

## Verification

- Resolver tests cover ID precedence, active/archived/all scopes, exact names, archived-name exclusion from active lookup, missing names, and duplicate-name errors with candidate IDs.
- CLI tests cover `--name` propagation for bare chat, `chat`, and `fork`, conflict rejection with `--title`, archive/unarchive by name, help text, and all existing exec input contracts.
- Runtime fork regression verifies a supplied child display name is persisted without changing source state or durable model inheritance.
- Delivery validation runs `git diff --check`, `go test ./...`, `go build ./...`, and `go tool golangci-lint run ./...`.
