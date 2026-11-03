# Handoff

## Goal
- M1: plan, ADRs, license, repo skeleton, gitleaks, compose stack, CI.

## Done
- Repository initialized (`main`), orchestrator setup, `.gitignore`.
- Discovery done (branch `feat/m0-discovery`): `docs/discovery.md` (sanitized), `docs/local/discovery.md` (raw, git-ignored).
- Semaphore backup created outside the repo (path in `docs/local/discovery.md`).
- User answers recorded in `docs/local/discovery.md`; secrets stored in git-ignored `secrets/`.
- Commit identity switched to GitHub noreply; unpublished history rewritten.
- Remote `origin` (SSH, deploy key in `secrets/`) configured, nothing pushed yet.

- M0 merged, tagged `m0`, pushed (full-history gitleaks clean).
- Connectors reachable: 2x OPNsense 26.7 (ISC legacy / dnsmasq), Proxmox 9.2, NetBox 4.7 (HTTP 8080 over overlay); Checkmk creds missing.
- M1 branch `feat/m1-foundation`: LICENSE, PLAN.md, ADR-0000..0012, README, CLAUDE.md extended.

## Open / Next step
- WAITING: user approval of PLAN.md + ADRs (Phase 1 gate).
- After approval: repo skeleton (Go module, SvelteKit app), Makefile, gitleaks pre-commit, compose stack, .env.example, CI.
- Still missing: Checkmk read-only automation user + site name; Zitadel role names.
- After answers: finish M0, then M1 (PLAN.md, ADRs, license, skeleton).

## Decisions (with short rationale)
- Semaphore source DB is MySQL 8.0 → migration tool must read MySQL (or the API).
- Subagents: project agents (`scout`/`worker`) require a session started in the repo dir; fallback `Explore` (haiku).

- OIDC: Zitadel PKCE public client, no client secret; roles via Zitadel project roles claim.
- Product requirement from user: guided setup wizard (users, connectors) and scoped access (e.g. only server network), not all-or-nothing.

## Relevant files
- `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `docs/discovery.md`, `docs/local/discovery.md`
