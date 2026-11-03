# ADR-0008: Server-Sent Events for live logs and status updates

- Status: proposed
- Date: 2026-11-03

## Decision
Live run logs and UI status updates use SSE (`text/event-stream`) with `Last-Event-ID` resume. Commands (cancel, start) stay plain REST.

## Consequences
Works through standard reverse proxies, automatic reconnect, no WebSocket upgrade handling. Proxy buffering must be disabled for SSE paths (documented).

## Alternatives
WebSocket (bidirectional, not needed).
