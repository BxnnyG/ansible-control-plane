# Handoff

## Goal
- Next: M4 (worker, templates, live logs, run history, schedules, webhooks; GIT-4 requirements install).
- M3 (done, merged): Git projects (sync/scan/webhooks), inventories (DB + Git-synced, import, preview), vault (Go-native), surveys from argument_specs, inventory-backed ScopeResolver, secrets API.

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
- M4 on `feat/m4-runner`, design in ADR-0016. Steps: (1) worker image (full ansible pkg) + callback plugin, (2) schema 00011 templates/runs/log chunks/host results/schedules + sqlc, (3) templates API, (4) worker API + bundle + queue/claim + masking, (5) worker supervisor (exec, cancel, requirements cache GIT-4), (6) SSE logs + run history/filters + retention, (7) schedules + template webhooks, (8) UI (templates, run dialog w/ survey, live log, history, schedules), (9) docs + live deploy + first live run.
- M4 done: (1) worker image ansible 14.6.0 (core 2.21.6), `acp` JSON-event callback (ACP_EVENTS_FILE, no_log-safe), smoke test scripts/test-worker-image.sh, cache volume acpworker-cache at /var/cache/acp; (2) migration 00011 (templates w/ webhook fields, template_vault_identities, schedules, runs, run_log_chunks, run_host_results) + sqlc; use `db.ClaimRun` helper (advisory lock enforces max_concurrent), never raw sqlc ClaimRun. Open: template delete must cancel queued runs; scheduler computes next cron run in Go and AdvanceSchedule in same tx as ClaimDueSchedules.
- M3 plan: (1) secrets API, (2) gitops package, (3) projects schema+API+sync+scan, (4) argument_specs → survey schema, (5) vault package + vault identities, (6) inventory schema+CRUD+render+import, (7) git-synced write-back, (8) ScopeResolver, (9) webhooks + scheduled sync, (10) UI pages.
- M3 done: (1) secrets API (migration 00006 secret types+metadata; secrets.write admin/owner only; known gap: concurrent PUT not locked), (2) internal/gitops, (5a) internal/vault; Go tests install git+openssh via apk (Makefile, test-db.sh, CI). Commit 64aa7aa has trailers in subject – fix via squash-merge (no history rewrite).
- M3 done: (3) projects (migration 00007, internal/projects, API, hostkey TOFU scan/confirm, sync with revisions, ACP_DATA_DIR + acpdata volume – verify volume ownership on next `make up`), (4) internal/scan (playbooks/roles/requirements/vars/inventories/vault detection, surveys from argument_specs) wired via scan.JSONScanner. Permissions: project.read viewer, project.write editor, project.sync operator.
- M3 done: (6) inventories (00008, internal/inventory: INI/YAML parse+serialize, render --list masked, import from project revision incl. group_vars/host_vars, multi-parent groups via link table, vars as vars_yaml, optional If-Match/ETag, vault values stored as {__ansible_vault}); (9) scheduled sync (30s tick, SKIP LOCKED claim, concurrency 2) + webhooks (gitea/github/gitlab/generic, secret shown once, public POST /api/hooks/projects/{id}, all rejections 404, 60/min per peer IP – behind a proxy one bucket; rejected calls not audited).
- M3 done: (7) git write-back (projects.WriteBack/Preview reusable for vault; POST inventories/{id}/commit, GET /diff; conflicts 409 git_conflict with paths; INI+vault 422 vault_requires_yaml; formatting/comments of touched files are regenerated), (8) inventory ScopeResolver (policy.InventoryResolver, group scope incl. descendants, filtered list/render, out-of-scope mutations 403 + authz.denied, Authorizer.AuthorizeHosts for M4; commit/diff require scope covering the whole inventory; network scopes match nothing until M5).
- M3 done: (5b) vault identities (00010, internal/vaultids; vault.read editor+, vault.manage/decrypt admin+, workspace-wide), internal/vaultops (list/decrypt audited before plaintext, encrypt/rekey via WriteBack, default branch acp/vault); (10) UI: secrets, projects (hostkey, sync, scan + survey preview, webhook), inventories (tree, vars, import, render, diff/commit), vault identities page + project vault panel. Extensionless dotted vars files fixed in inventory + scan.
- M3 live: deployed (DB at migration 10; ACP DB backup before deploy in /opt/backups/acp-2026-11-04-2102/), the Ansible project synced (host key pinned, deploy key secret), its three inventories imported via `acp admin inventory import` (git mode, write branch acp/<slug>), counts verified against the source files.
- Ansible repo: group_vars/host_vars moved next to the infra inventory (README updated), merged to main (fast-forward) by Claude on user request; ACP project re-synced and infra re-imported (group vars all/docker_hosts and host_vars now present). matrix_webhook_url is an empty placeholder (no secret in Git).
- Admin CLI for autonomous ops: `acp admin project list|sync`, `acp admin inventory list|import` (run via `docker compose exec app acp admin ...`). User expects routine live ops (sync, import, checks) to be done by Claude without asking.
- Docs: README status/features, PLAN roadmap (M1–M3 done, GIT-4 → M4), operator guide docs/projects-and-inventories.md. Keep docs current with every change (user request).
- Full verification on branch green: make test, test-db, lint, web lint/check/vitest/build, docker compose build. Not yet: browser/live test, deploy to live stack (migrations 00006–00010).
- Open after M3: no vitest for vault components; identity CRUD SQL not DB-tested; secrets concurrent PUT unlocked; webhook rate limit per peer IP; rewriting files drops comments (inventory) / normalizes indentation (inline vault).
- M0, M1, M2 merged, tagged (`m0`, `m1`, `m2`), pushed. M2 was squash-merged (branch history contained an internal hostname). Live stack runs M2 (DB at migration 5); OIDC configured and verified live (SSO login -> IdP membership admin), break-glass admin created and verified. Next: M3 on `feat/m3-projects-inventory`.
- Live instance runs from this repo: the internal host (see docs/local/; 443, own TLS cert), HTTP 8080 redirects; Semaphore keeps port 80 until cutover.
- M2 done: `internal/secrets` (envelope encryption, local provider, masked Value) – d2ee31d. Master key wired (ACP_MASTER_KEY_FILE default /run/secrets/master_key, env fallback ACP_MASTER_KEY, `serve` fails fast); `acp admin rotate-master-key --new-key-file` re-wraps secrets + TOTP envelopes in one tx.
- M2 done: DB schema 00002_auth_core (users, workspaces, memberships+scopes, sessions, secrets, append-only audit_log) + sqlc (internal/db/queries, dbgen) – 8f5ff15..14d25d6. DB tests need ACP_TEST_DATABASE_URL (`make test-db` runs them against a throwaway Postgres). TOTP secret stored as envelope JSON on users row.
- M2 done: OIDC PKCE BFF (internal/auth; /api/auth/oidc/{login,callback}, POST /api/auth/logout, /api/v1/me; sealed state cookie, hashed session+CSRF tokens, JIT users, audit) – 5020f6a..7414a8a. Open: IdP-role→membership mapping (RoleSyncer hook), login rate limit, refresh tokens, /login UI (error codes login_failed/idp_unavailable/account_disabled).
- User TODO in Zitadel: app type native/SPA, PKCE, auth method none; redirect <PUBLIC_URL>/api/auth/oidc/callback; post-logout <PUBLIC_URL>/login; enable role assertion in ID token; add scope urn:zitadel:iam:org:project:roles.
- M2 done: break-glass admin (argon2id + TOTP, `acp admin create-breakglass|reset-breakglass-totp`, POST /api/auth/breakglass/login, lockout + IP limiter, migration 00003; details in ADR-0006). Auth service now mounts with break-glass only (no OIDC). Fixed .gitignore so internal/db/queries/*.sql are tracked. Note: DB tests of different packages must not share one database (use one throwaway DB per package).
- M2 done: `internal/policy` (permission matrix, scope resolver, `Authorizer.Handle/Require`, route-declaration test), migration 00004 (`memberships.source`), `auth.IdPRoleSyncer` (acp-* roles -> `default` workspace, login rejected with `/login?error=no_access`), GET /api/v1/workspaces[/{workspace}], extended /api/v1/me. Details in ADR-0007. Open: M3 inventory-backed ScopeResolver, membership-management API using `policy.CanAssignRole`, /login UI error code `no_access`.
- M2 done: /login (SSO + break-glass, ?error= codes, safe return_to), authenticated shell (workspace switcher, logout), first-run setup wizard (`/setup`, migration 00005, GET/PUT /api/v1/workspaces/{ws}/setup, POST .../setup/complete, public GET /api/auth/config). Open: OpenAPI-generated TS types (api.ts is hand-written), browser/e2e check against the live stack, remaining M2 sub-tasks (secrets store API, membership management UI).
- M2 small items closed: `make test-db`, session cleanup job (15m, serve), setup audit detached from request ctx, `acp admin set-breakglass-password`. After a key rotation: swap the master key file and restart.
- Fix merged (`fix/oidc-role-claims`): OIDC roles fall back to UserInfo and Zitadel project-specific claims; callback logs claim key names/role source; login_denied audit has roles_received/roles/role_source. Zitadel needs: project 'assert roles', app 'roles inside ID token', scope urn:zitadel:iam:org:project:roles (ACP_OIDC_SCOPES must still include openid).
- TLS chain: server cert served without intermediate; need intermediate + root CA cert (public) from user to serve full chain / trust in worker.

## Decisions (with short rationale)
- M4 (user, 2026-11-05): worker uses the full current `ansible` community package (compat with Semaphore's 93 collections); RUN-8 notifications deferred to M6/M8.
- M3 (user, 2026-11-04): ACP gets its own read-write deploy key for the Ansible repo (`secrets/acp_git_deploy_ed25519`, public key to be added on the Git server by the user); Git server is Gitea/Forgejo (webhook HMAC); write branch configurable per inventory, default `acp/<inventory-slug>`, never force-push.
- M3: ADR-0013 (project cache clone + scan in Go, TOFU-with-confirmation known_hosts), ADR-0014 (single relational inventory model for both modes), ADR-0015 (vault format in Go). GIT-4 (requirements install) moves to M4 (needs ansible-galaxy in worker).
- Semaphore source DB is MySQL 8.0 → migration tool must read MySQL (or the API).
- Subagents: project agents (`scout`/`worker`) require a session started in the repo dir; fallback `Explore` (haiku).

- OIDC: Zitadel PKCE public client, no client secret; roles via Zitadel project roles claim.
- Product requirement from user: guided setup wizard (users, connectors) and scoped access (e.g. only server network), not all-or-nothing.

- User delegates merge/tag/push decisions to Claude (stated 2026-11-03); still: gitleaks + infra scan before every push, no force-push to main.
- Zitadel roles created: acp-admin, acp-editor, acp-operator, acp-viewer.

## Relevant files
- `internal/secrets/` (ADR-0005); `.gitignore` anchors `/secrets/` so the Go package is tracked.
- `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `docs/discovery.md`, `docs/local/discovery.md`
