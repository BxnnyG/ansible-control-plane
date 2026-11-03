# ADR-0001: Backend in Go

- Status: accepted
- Date: 2026-11-03

## Context
The app needs a REST API, SSE streaming, a scheduler, connector syncs and Git operations, packaged as a small container on a 4 GiB host.

## Decision
- Go (current stable release, pinned in `go.mod` and CI).
- HTTP router: `chi`; DB: `pgx/v5` + `sqlc` for typed queries; migrations: `goose` (embedded SQL).
- Config via environment (`.env`), structured logging via `log/slog` (JSON).
- One binary with subcommands: `serve`, `worker`, `migrate semaphore`, `admin …`, `db migrate`.

## Consequences
Static binary, small image, strong concurrency primitives. `sqlc` requires regenerating code on schema changes (enforced in CI).

## Alternatives
Python/FastAPI (closer to Ansible, but heavier runtime and weaker typing); Node/TypeScript (one language for UI and API, but worse fit for long-running process supervision).
