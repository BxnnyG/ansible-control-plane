# Run notifications

Operator guide for run notifications (v1.1). Design rationale: [ADR-0021](adr/0021-run-notifications.md).

## Concepts

- **Channel:** a destination (webhook, Matrix, SMTP e-mail, Gotify, ntfy) with non-secret config, an optional credential from the secrets store, and TLS options (extra CA bundle, skip-verify).
- **Rule:** connects a channel to one or more event kinds, optionally limited to one template. Rules can be disabled.
- **Delivery:** one row per (run, channel) with status `pending`, `sent` or `failed`, attempt count and `last_error`.

### Event kinds

| Kind | Fires when |
| --- | --- |
| `run.failed` | Run ended `failed` or `lost` |
| `run.recovered` | Run `succeeded` and the previous terminal run of the template (ignoring `cancelled`) was `failed` or `lost` |
| `run.drift` | Check-mode run `succeeded` with changed hosts (`hosts_changed > 0`) |
| `run.finished` | Every terminal status, including `cancelled` |

A run can match several kinds, but a channel gets at most one message per run. Precedence: `run.failed` > `run.recovered` > `run.drift` > `run.finished`.

### Delivery semantics

- Delivery is **at-least-once**: a crash between sending and recording the result can produce a duplicate. Receivers should deduplicate on `run.id` plus `X-ACP-Event`.
- Failed sends are retried with exponential backoff (30 s base, capped at 30 min), at most 5 attempts, then the delivery is `failed`. Permanent errors (for example a rejected configuration) fail immediately. `last_error` never contains secrets.
- Events and deliveries are pruned together with run logs after `ACP_RUN_LOG_RETENTION_DAYS` (default 90, `0` keeps everything).
- Messages contain workspace, template name, status, trigger, host counts, duration and a link built from `ACP_PUBLIC_URL`. They never contain log lines, params or variables.
- The app container sends the messages, so it needs outbound access to the targets. Private addresses are allowed; this is why channel management is admin-only.

## Channel configuration

| Type | Config fields | Secret (optional unless noted) |
| --- | --- | --- |
| `webhook` | `url`, `auth` (`hmac` default, or `bearer`) | HMAC key or bearer token |
| `matrix` | `homeserver`, `room_id` | Access token (required) |
| `smtp` | `host`, `port`, `security` (`starttls` default, `tls`, `none`), `insecure` (must be true for `none`), `username`, `from`, `to` (list) | Password |
| `gotify` | `url`, `priority` | App token (required) |
| `ntfy` | `url`, `topic`, `priority` | Access token |

Secrets live only in the secrets store and are resolved per send. Any secret type holding a single value works; the CLI creates `api_token` secrets by default (`--secret-type password` for SMTP is fine). Redirects and environment proxies are not followed; the timeout is 10 s; TLS 1.2+ is enforced.

## Webhook payload (schema version 1)

`POST` with `Content-Type: application/json` and header `X-ACP-Event: <kind>`.

```json
{
  "version": 1,
  "event": "run.failed",
  "workspace": "default",
  "run": {
    "id": "00000000-0000-0000-0000-000000000000",
    "template": "site-update",
    "status": "failed",
    "trigger": "schedule",
    "check": false,
    "hosts_ok": 3,
    "hosts_changed": 1,
    "hosts_failed": 1,
    "hosts_unreachable": 0,
    "duration_seconds": 42.5,
    "finished_at": "2026-01-01T12:00:00Z",
    "url": "https://acp.example.com/runs/00000000-0000-0000-0000-000000000000"
  }
}
```

Test messages from the test action carry `"test": true`.

With `auth: hmac` and a secret set, the header `X-ACP-Signature: sha256=<hex>` is the HMAC-SHA256 of the raw request body. With `auth: bearer`, the secret is sent as `Authorization: Bearer <token>` instead.

Verify a signature (shell; `body.json` is the raw body, `$KEY` the shared key, `$SIG` the header value):

```sh
expected="sha256=$(openssl dgst -sha256 -hmac "$KEY" -hex < body.json | awk '{print $NF}')"
[ "$expected" = "$SIG" ] && echo valid || echo invalid
```

Python receiver:

```python
import hmac, hashlib
def valid(key: bytes, body: bytes, header: str) -> bool:
    mac = "sha256=" + hmac.new(key, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(mac, header)
```

Always compare over the raw bytes, not re-serialized JSON.

## Permissions

- `notification.read` (editor and above): list channels, rules and deliveries.
- `notification.manage` (admin and above): create, change and delete channels and rules, send test messages.

Both are workspace-wide. Channel and rule changes and tests are audited.

## UI

Open **Notifications** in the workspace navigation. The page has three sections: channels (create, edit, enable/disable, test, delete), rules (pick channel, event kinds and an optional template) and recent deliveries with status filter. Secrets are never shown.

### Creating a channel

1. **New channel**, pick the type and a name. A collapsible **Setup guide** under the name explains the steps for the chosen type (see below). Each field has a help text; defaults are suggested (ntfy server `https://ntfy.sh`, SMTP port hint per security mode).
2. **Credential**: choose *None*, an *Existing secret*, or *Enter new credential*. A new credential is stored by the form as an encrypted secret named `notify-<channel name>` (type `password` for SMTP, `api_token` for Gotify, `token` otherwise) before the channel is saved; if saving the channel fails, that secret is removed again. For an existing secret you can optionally replace its value; this changes the shared secret for everything that uses it.
3. **Send test** works before saving (see draft test below) and on edits.
4. **Create**. Channels without any rule show a hint with a link to the rules tab: a channel only sends automatically once a rule references it.

### Draft test

`POST /api/v1/workspaces/{workspace}/notifications/channels/test` sends the sample message through an unsaved definition (`type`, `config`, optional `secret_id` or `secret_value`, TLS options and optional `channel_id`). The config is validated like on create (invalid input returns 422). Credential precedence: `secret_value`, then `secret_id`, then the stored secret of `channel_id`, so edits of an existing channel can be tested without retyping the credential. `secret_value` is used in memory only: it is never stored, logged, audited or returned, and error texts are sanitized. The response is `{ok, error}` like the saved-channel test; the audit entry `notification.channel.test_draft` records only type and result. Requires `notification.manage`; limited to 10 requests per minute and user (HTTP 429). Because the draft credential travels in the request body, use HTTPS (the default).

### Setup per type

The app container sends every message itself, so it must be allowed to reach the target (firewall and egress allowlist). The same guides are available in the UI.

- **ntfy:** use `https://ntfy.sh` or your own server; choose an unguessable topic (on public servers the topic is the only protection); an access token (`tk_...`) is optional and only needed for servers with access control; subscribe to the topic in the ntfy app; priority 1-5 is optional.
- **Matrix:** create a dedicated bot user; get its access token (Element: Settings, Help & About, Advanced, or the login API); invite the bot to the room and accept the invite; use the internal room ID (`!abc:server`, Room settings, Advanced), not the alias. **Encrypted rooms are not supported:** the sender posts plain `m.room.message` events without end-to-end encryption, so use an unencrypted room.
- **SMTP:** port 587 with STARTTLS, 465 with TLS, 25 without encryption (requires the explicit insecure confirmation). Providers with two-factor authentication need an app password. The From address must be allowed by the server. Empty port means the default of the chosen mode.
- **Gotify:** create an application in Gotify and use its application token (not a client token); URL is the base URL without `/message`.
- **Webhook:** HMAC (default): header `X-ACP-Signature: sha256=<hex>`, the HMAC-SHA256 of the raw body keyed with the credential (no header without a credential); bearer: `Authorization: Bearer <credential>`. Every request has `X-ACP-Event`. Verify on the raw bytes, for example `printf "sha256=%s" "$(openssl dgst -sha256 -hmac "$KEY" -hex < body.json | sed 's/^.* //')"`, and compare in constant time.

## CLI

Secret values are read from a file or stdin, never from arguments:

```sh
# Webhook with HMAC key
printf '%s' "$KEY" | docker compose exec -T app acp admin notification channel create \
  --workspace default --type webhook --name ops-hook \
  --set url=https://hooks.example.com/acp --secret-file /dev/stdin

# SMTP with password
printf '%s' "$PW" | docker compose exec -T app acp admin notification channel create \
  --workspace default --type smtp --name mail --set host=smtp.example.org --set port=587 \
  --set from=acp@example.org --set 'to=["ops@example.org"]' \
  --secret-file /dev/stdin --secret-type password

docker compose exec app acp admin notification channel test --workspace default --channel ops-hook
docker compose exec app acp admin notification rule create --workspace default \
  --channel ops-hook --event run.failed --event run.recovered [--template site-update]
docker compose exec app acp admin notification channel list --workspace default
docker compose exec app acp admin notification rule list --workspace default
docker compose exec app acp admin notification delivery list --workspace default \
  --channel ops-hook --status failed --limit 20
```

Other commands: `channel update` (`--set`, `--unset`, `--clear-secret`, `--enable`, `--disable`, `--ca-file`, `--insecure-skip-verify`), `channel delete [--delete-secret]` (also deletes the channel's secret once nothing else references it; otherwise the secret is kept and the command reports why), `rule delete --rule ID`. Stored secrets can be listed and removed with `acp admin secret list|delete`. Run without arguments for the full usage.

## Troubleshooting

- **Nothing arrives:** check that an enabled rule exists for the channel, the channel is enabled, and the event kind matches (a failed run only sends `run.failed`, not also `run.finished`, to the same channel). Then list deliveries.
- **Delivery `failed`:** read `last_error` (`delivery list`). Typical causes: DNS or firewall from the app container, TLS verification (use `--ca-file`), wrong credentials, wrong Matrix room or ntfy topic.
- **Delivery stays `pending`:** a retry is scheduled (`next_attempt_at`); the dispatcher runs inside `serve`, so the app must be running.
- **Duplicates:** expected rarely (at-least-once); deduplicate on `run.id`.
- **Test works, real runs do not:** the test bypasses rules; verify rules and the template filter.

## Limitations

One template filter per rule, no per-user subscriptions, digests or quiet hours, and runs imported already finished (Semaphore migration) emit no events.
