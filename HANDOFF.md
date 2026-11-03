# Handoff

## Goal
- M0: Git init, orchestrator setup, read-only discovery, open questions to the user.

## Done
- Repository initialized (`main`), orchestrator setup, `.gitignore`.
- Discovery done (branch `feat/m0-discovery`): `docs/discovery.md` (sanitized), `docs/local/discovery.md` (raw, git-ignored).
- Semaphore backup created outside the repo (path in `docs/local/discovery.md`).
- User answers recorded in `docs/local/discovery.md`; secrets stored in git-ignored `secrets/`.
- Commit identity switched to GitHub noreply; unpublished history rewritten.
- Remote `origin` (SSH, deploy key in `secrets/`) configured, nothing pushed yet.

## Open / Next step
- Waiting for: firewall rules (OPNsense x2, NetBox), tokens for Proxmox/NetBox/Checkmk, deploy key added on GitHub, project name, Zitadel redirect URIs set, M0 approval.
- After answers: finish M0, then M1 (PLAN.md, ADRs, license, skeleton).

## Decisions (with short rationale)
- Semaphore source DB is MySQL 8.0 → migration tool must read MySQL (or the API).
- Subagents: project agents (`scout`/`worker`) require a session started in the repo dir; fallback `Explore` (haiku).

- OIDC: Zitadel PKCE public client, no client secret; roles via Zitadel project roles claim.
- Product requirement from user: guided setup wizard (users, connectors) and scoped access (e.g. only server network), not all-or-nothing.

## Relevant files
- `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `docs/discovery.md`, `docs/local/discovery.md`
