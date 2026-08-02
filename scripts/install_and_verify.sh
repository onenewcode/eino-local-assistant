#!/usr/bin/env bash
# Install cobra via go get (never hand-edit go.mod) and verify the CLI.
set -euo pipefail
cd "$(dirname "$0")/.."

log() { printf '%s\n' "$*"; }

log "==> go get cobra@latest"
go get github.com/spf13/cobra@latest
log "==> go mod tidy"
go mod tidy

log "==> go.mod head"
head -20 go.mod
log "==> cobra entries"
grep -n cobra go.mod || {
  log "ERROR: cobra missing from go.mod"
  exit 1
}

log "==> go test ./..."
go test ./... -count=1
log "==> go build ./..."
go build ./...
log "==> golangci-lint"
go tool golangci-lint run ./...
log "==> help"
go run ./cmd/eino-assistant -h
log "==> version"
go run ./cmd/eino-assistant version
log "==> help resume"
go run ./cmd/eino-assistant help resume
log "ALL_OK"
