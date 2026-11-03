# ADR-0006: OIDC with PKCE (public client), server-side sessions, break-glass admin

- Status: proposed
- Date: 2026-11-03

## Context
The identity provider is Zitadel; the client is configured as a PKCE client without a secret. A local login is needed only for emergencies.

## Decision
- Authorization Code flow with PKCE (S256) performed by the backend (BFF pattern); tokens never reach the browser.
- Server-side sessions in Postgres, HttpOnly + Secure + SameSite=Lax cookie, CSRF token for state-changing requests.
- Roles from a configurable claim (Zitadel default `urn:zitadel:iam:org:project:roles`), mapped to workspace roles/scopes by rules.
- Redirect URI `/api/auth/oidc/callback`, post-logout redirect `/login`, RP-initiated logout via `end_session_endpoint`.
- Break-glass admin: argon2id password + TOTP, can be disabled via env, all uses audited.

## Consequences
Works with any standards-compliant IdP; client secret optional (confidential clients supported via config).

## Alternatives
SPA-side OIDC (tokens in the browser); proxy-based auth (oauth2-proxy) – loses in-app role mapping.
