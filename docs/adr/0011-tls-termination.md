# ADR-0011: TLS terminated by the app, reverse proxy optional

- Status: accepted
- Date: 2026-11-03

## Context
There is no existing reverse proxy; the operator provides a certificate from an internal CA.

## Decision
The app terminates TLS itself when `ACP_TLS_CERT_FILE` and `ACP_TLS_KEY_FILE` are set (with optional HTTP→HTTPS redirect). Behind a reverse proxy, plain HTTP with trusted proxy headers can be configured instead. Ports are configurable to avoid clashes with the old system during migration.

## Consequences
`docker compose up -d` gives a working HTTPS endpoint without extra components.

## Alternatives
Bundled Caddy/Traefik container (extra component; can still be added by operators).
