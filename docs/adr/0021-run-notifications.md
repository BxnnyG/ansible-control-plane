# ADR-0021: Run notifications

- Status: accepted
- Date: 2026-11-07

## Context
PLAN.md open question 3: operators want to learn about failed runs, recoveries and drift without watching the UI. The user chose four channel types for v1.1: generic webhook, Matrix, e-mail (SMTP) and Gotify/ntfy, and four event kinds: run failed, run recovered, drift detected, every run.

A run reaches a terminal state on several code paths: worker `finish` (`internal/workerapi`), lost-run maintenance, `CancelRun` for queued runs and template deletion. There is no post-run hook today.

Notifications are the first feature that writes to external systems. Hard rule 4 (read-only connectors) covers connectors, not notifications; the connector HTTP client (`internal/connectors/httpx`) deliberately refuses non-GET methods and stays that way.

## Decision
- **Outbox fed by a database trigger.** An `AFTER UPDATE OF status` trigger on `runs` inserts one row into `notification_events` when a run moves into a terminal status (`succeeded`, `failed`, `cancelled`, `lost`). A trigger catches every current and future code path, including SQL run by the CLI, and commits atomically with the status change. Event derivation happens in Go, not in the trigger.
- **Dispatcher in `serve`** (`internal/notify`): polls the outbox (`FOR UPDATE SKIP LOCKED`, small batches), derives event kinds, matches rules, creates one `notification_deliveries` row per (event, channel) and sends. Failed deliveries are retried with exponential backoff (max 5 attempts, then `failed`); the last error is stored without secrets. Outbox and delivery rows are pruned with the run log retention.
- **Event kinds** derived from the run row and the previous terminal run of the same template:
  - `run.failed`: status `failed` or `lost`.
  - `run.recovered`: status `succeeded` and the previous terminal run (ignoring `cancelled`) of the same template was `failed` or `lost`.
  - `run.drift`: run in check mode (`params.check`) that finished `succeeded` with `hosts_changed > 0`.
  - `run.finished`: every terminal status, including `cancelled`.
  A run can match several kinds; a channel receives at most one message per run (the most specific kind wins: failed > recovered > drift > finished).
- **Model:** `notification_channels` (workspace, name, type `webhook|matrix|smtp|gotify|ntfy`, non-secret config JSON, optional `secret_id` into the secrets store, TLS options, enabled) and `notification_rules` (workspace, channel, event kinds, optional template filter, enabled). Credentials (webhook HMAC key or bearer token, Matrix access token, SMTP password, Gotify app token, ntfy token) live only in the secrets store and are resolved per send, then zeroed.
- **Message content:** workspace, template name, status, trigger, host counts, duration and a link built from `ACP_PUBLIC_URL`. No log lines, no params, no variables (they may contain secrets even when masked).
- **Transport:** a dedicated sender HTTP client in `internal/notify` (TLS 1.2+, optional CA bundle and skip-verify per channel, no proxy from the environment, no redirects, 10 s timeout, response body capped). SMTP uses STARTTLS or implicit TLS; plain SMTP only with an explicit `insecure` flag. Private and internal addresses are allowed on purpose (Matrix/Gotify are usually internal); channel management is therefore admin-only.
- **Permissions:** `notification.read` (editor+) and `notification.manage` (admin+), workspace-wide. A test endpoint sends a sample message and is audited. Channel and rule changes are audited.
- **Webhook format:** JSON body with a versioned schema (`"version": 1`), `X-ACP-Event` header and, when a secret is set, `X-ACP-Signature: sha256=<hex HMAC of the body>` (or `Authorization: Bearer` when the channel is configured for bearer auth).

## Consequences
- Notifications work for every terminal path without touching the run code paths; the trigger is the single integration point.
- Delivery is at-least-once: a crash between send and marking the delivery as sent can produce a duplicate.
- The app container needs outbound access to the notification targets (it is on the non-internal default network today). The worker egress allowlist is unaffected.
- No per-user subscriptions, digests or quiet hours in v1.1.
