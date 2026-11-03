# Handoff

## Goal
- M0: Git init, orchestrator setup, read-only discovery, open questions to the user.

## Done
- Repository initialized (`main`), orchestrator setup, `.gitignore`.
- Discovery done (branch `feat/m0-discovery`): `docs/discovery.md` (sanitized), `docs/local/discovery.md` (raw, git-ignored).
- Semaphore backup created outside the repo (path in `docs/local/discovery.md`).

## Open / Next step
- Waiting for user answers: project name, connector URLs/tokens + monitoring system, OIDC (Zitadel), domain/reverse proxy, GitHub remote.
- Additional questions: host resources (2 GiB RAM / ~3.9 GB free disk), port 80 conflict with Semaphore until cutover, management access from this host to OPNsense.
- After answers: finish M0, then M1 (PLAN.md, ADRs, license, skeleton).

## Decisions (with short rationale)
- Semaphore source DB is MySQL 8.0 → migration tool must read MySQL (or the API).
- Subagents: project agents (`scout`/`worker`) require a session started in the repo dir; fallback `Explore` (haiku).

## Relevant files
- `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `docs/discovery.md`, `docs/local/discovery.md`
