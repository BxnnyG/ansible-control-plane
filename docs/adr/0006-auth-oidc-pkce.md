# ADR-0006: OIDC with PKCE (public client), server-side sessions, break-glass admin

- Status: accepted
- Date: 2026-11-03

## Context
The identity provider is Zitadel; the client is configured as a PKCE client without a secret. A local login is needed only for emergencies.

## Decision
- Authorization Code flow with PKCE (S256) performed by the backend (BFF pattern); tokens never reach the browser.
- Server-side sessions in Postgres, HttpOnly + Secure + SameSite=Lax cookie, CSRF token for state-changing requests.
- Roles from a configurable claim (Zitadel default `urn:zitadel:iam:org:project:roles`), mapped to workspace roles/scopes by rules.
- Redirect URI `/api/auth/oidc/callback`, post-logout redirect `/login`, RP-initiated logout via `end_session_endpoint`.
- Break-glass admin: argon2id password + mandatory TOTP, can be disabled via `ACP_BREAKGLASS_ENABLED=false`, all uses audited.
  - Created only through the CLI: `acp admin create-breakglass --username X [--generate]` (password from a no-echo prompt, a pipe, or generated and printed once) and `acp admin reset-breakglass-totp --username X`. Both print the TOTP secret and otpauth URI once.
  - Bootstrap: the user becomes `owner` of the workspace with slug `default`, created when missing.
  - Login: `POST /api/auth/breakglass/login {username,password,totp}` (JSON only, same-origin) issues the same session and CSRF cookies as OIDC. It works when OIDC is not configured; the OIDC routes are then not mounted. Failures return one generic 401; the reason (unknown user, bad password, bad TOTP, replay, locked, disabled) is only written to the audit log (`auth.breakglass_failed`, success: `auth.breakglass_login`). Unknown users cost the same Argon2 work as known ones.
  - Argon2id: m=64 MiB, t=3, p=2, 16-byte salt, 32-byte key, PHC string; at most 2 hashes run concurrently (memory bound on a 4 GiB host); stored parameters are range-checked on verify. Minimum password length 12.
  - TOTP: RFC 6238, SHA1, 6 digits, 30 s, accepted steps -1..+1. Replay protection: `users.totp_last_step`, updated atomically (only a newer step wins). The secret is sealed with `internal/secrets`; AAD = {SecretID: user ID, WorkspaceID: nil UUID}, which binds it to the user and separates it from workspace secrets.
  - Lockout: 5 consecutive failures lock the account for 15 min (`users.failed_logins`/`locked_until`, durable); additionally 10 failures per source IP in 15 min yield 429 (in memory, uses the socket address, never forwarded headers). Trade-off: an attacker can lock the break-glass account for 15 min; a successful login or `reset-breakglass-totp` clears the lock.

## Consequences
Works with any standards-compliant IdP; client secret optional (confidential clients supported via config).

## Alternatives
SPA-side OIDC (tokens in the browser); proxy-based auth (oauth2-proxy) – loses in-app role mapping.
