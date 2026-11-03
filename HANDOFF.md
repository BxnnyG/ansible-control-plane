# Handoff

## Goal
- M2: auth (OIDC PKCE + break-glass), setup wizard, workspaces, roles + scopes, secrets store, audit.

## Done
- Repository initialized (`main`), orchestrator setup, `.gitignore`.
- Discovery done (branch `feat/m0-discovery`): `docs/discovery.md` (sanitized), `docs/local/discovery.md` (raw, git-ignored).
- Semaphore backup created outside the repo (path in `docs/local/discovery.md`).
- User answers recorded in `docs/local/discovery.md`; secrets stored in git-ignored `secrets/`.
- Commit identity switched to GitHub noreply; unpublished history rewritten.
- Remote `origin` (SSH, deploy key in `secrets/`) configured, nothing pushed yet.

- M0 merged, tagged `m0`, pushed (full-history gitleaks clean).
- Connectors reachable: 2x OPNsense 26.7 (ISC legacy / dnsmasq), Proxmox 9.2, NetBox 4.7 (HTTP 8080 over overlay); Checkmk creds missing.
- M1 branch `feat/m1-foundation`: LICENSE, PLAN.md, ADR-0000..0012 (accepted), README, CLAUDE.md; web skeleton; Go skeleton; hardened Dockerfiles + compose (verified healthy); gitleaks hook (verified blocking); CI workflow.
- Attribution text is "Created by bxnny" (user decision), single source: internal/version, web/src/lib/attribution.ts.
- Checkmk 2.5 reachable (site/user in docs/local).

## Open / Next step
- M0, M1, M2 merged, tagged (`m0`, `m1`, `m2`), pushed. M2 was squash-merged (branch history contained an internal hostname). Live stack runs M2 (DB at migration 5); OIDC not yet configured in .env (needs client ID), break-glass admin not yet created. Next: M3 on `feat/m3-projects-inventory`.
- Live instance runs from this repo: the internal host (see docs/local/; 443, own TLS cert), HTTP 8080 redirects; Semaphore keeps port 80 until cutover.
- M2 done: `internal/secrets` (envelope encryption, local provider, masked Value) – d2ee31d. Master key wired (ACP_MASTER_KEY_FILE default /run/secrets/master_key, env fallback ACP_MASTER_KEY, `serve` fails fast); `acp admin rotate-master-key --new-key-file` re-wraps secrets + TOTP envelopes in one tx.
- M2 done: DB schema 00002_auth_core (users, workspaces, memberships+scopes, sessions, secrets, append-only audit_log) + sqlc (internal/db/queries, dbgen) – 8f5ff15..14d25d6. DB tests need ACP_TEST_DATABASE_URL (`make test-db` runs them against a throwaway Postgres). TOTP secret stored as envelope JSON on users row.
- M2 done: OIDC PKCE BFF (internal/auth; /api/auth/oidc/{login,callback}, POST /api/auth/logout, /api/v1/me; sealed state cookie, hashed session+CSRF tokens, JIT users, audit) – 5020f6a..7414a8a. Open: IdP-role→membership mapping (RoleSyncer hook), login rate limit, refresh tokens, /login UI (error codes login_failed/idp_unavailable/account_disabled).
- User TODO in Zitadel: app type native/SPA, PKCE, auth method none; redirect <PUBLIC_URL>/api/auth/oidc/callback; post-logout <PUBLIC_URL>/login; enable role assertion in ID token; add scope urn:zitadel:iam:org:project:roles.
- M2 done: break-glass admin (argon2id + TOTP, `acp admin create-breakglass|reset-breakglass-totp`, POST /api/auth/breakglass/login, lockout + IP limiter, migration 00003; details in ADR-0006). Auth service now mounts with break-glass only (no OIDC). Fixed .gitignore so internal/db/queries/*.sql are tracked. Note: DB tests of different packages must not share one database (use one throwaway DB per package).
- M2 done: `internal/policy` (permission matrix, scope resolver, `Authorizer.Handle/Require`, route-declaration test), migration 00004 (`memberships.source`), `auth.IdPRoleSyncer` (acp-* roles -> `default` workspace, login rejected with `/login?error=no_access`), GET /api/v1/workspaces[/{workspace}], extended /api/v1/me. Details in ADR-0007. Open: M3 inventory-backed ScopeResolver, membership-management API using `policy.CanAssignRole`, /login UI error code `no_access`.
- M2 done: /login (SSO + break-glass, ?error= codes, safe return_to), authenticated shell (workspace switcher, logout), first-run setup wizard (`/setup`, migration 00005, GET/PUT /api/v1/workspaces/{ws}/setup, POST .../setup/complete, public GET /api/auth/config). Open: OpenAPI-generated TS types (api.ts is hand-written), browser/e2e check against the live stack, remaining M2 sub-tasks (secrets store API, membership management UI).
- M2 small items closed: `make test-db`, session cleanup job (15m, serve), setup audit detached from request ctx, `acp admin set-breakglass-password`. After a key rotation: swap the master key file and restart.
- TLS chain: server cert served without intermediate; need intermediate + root CA cert (public) from user to serve full chain / trust in worker.

## Decisions (with short rationale)
- Semaphore source DB is MySQL 8.0 → migration tool must read MySQL (or the API).
- Subagents: project agents (`scout`/`worker`) require a session started in the repo dir; fallback `Explore` (haiku).

- OIDC: Zitadel PKCE public client, no client secret; roles via Zitadel project roles claim.
- Product requirement from user: guided setup wizard (users, connectors) and scoped access (e.g. only server network), not all-or-nothing.

- User delegates merge/tag/push decisions to Claude (stated 2026-11-03); still: gitleaks + infra scan before every push, no force-push to main.
- Zitadel roles created: acp-admin, acp-editor, acp-operator, acp-viewer.

## Relevant files
- `internal/secrets/` (ADR-0005); `.gitignore` anchors `/secrets/` so the Go package is tracked.
- `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `docs/discovery.md`, `docs/local/discovery.md`
