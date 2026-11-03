@ORCHESTRATOR.md

# ansible-control-plane (working title)

Self-hosted web platform to run, organize and monitor Ansible; replacement for the local Semaphore UI installation. License: AGPL-3.0 (public repo).

## Hard rules (non-negotiable)

1. **Never delete or modify Semaphore.** No uninstall, no DB changes, no config changes. Reading is allowed. Exception: at the end of the migration (M7) the service may be **stopped and disabled**, only after explicit user confirmation. Data, containers, volumes and binaries stay in place.
2. **Before any access to Semaphore data**, create a full backup (DB dump + `config.json` + data dir) in `/opt/backups/semaphore-<date>/` (outside the repo) and report the path.
3. **Destructive actions only after asking:** `rm -rf`, package removal, firewall/network changes, systemd changes to other services, any write access to OPNsense/Proxmox/NetBox/monitoring.
4. **Connectors are read-only** towards external systems in v1.0 (exception: monitoring acknowledge, if the user agrees).
5. **Secrets** (SSH keys, vault passwords, API tokens) never in plaintext in logs, Git, chat or test data. Output only masked.
6. Work exclusively inside `/opt/ansible-control-plane/` (renaming after the name is chosen is allowed).
7. **Stop at the end of every milestone**, summarize, wait for user OK.
8. No assumptions about the environment: check first, then build. When unclear, ask.

## Language

- Chat with the user in German.
- Every file in the repo is in English (code, comments, docs, ADRs, agent definitions, commit messages, branch names, issues). Translate any German text found in repo files.

## Git rules

- Everything belongs in the repo: code, frontend, compose files, Dockerfiles, `CLAUDE.md`, `ORCHESTRATOR.md`, `HANDOFF.md`, `PLAN.md`, `docs/`, ADRs, `.claude/agents/`, `scripts/`, migration tool, tests and fixtures, CI workflows, `.env.example`.
- Never in the repo: `.env`, keys, tokens, vault passwords, Semaphore backups and dumps, fixtures containing real data. `gitleaks` runs as pre-commit hook and in CI.
- Internal infrastructure details (real IPs, hostnames, MACs, domains, raw discovery data) go to `docs/local/` (git-ignored). Only sanitized versions go into the repo.
- Branches: one per milestone, `feat/m<N>-<short-name>`. Merge to `main` only after user OK, then tag `m<N>`. Release tag `v1.0.0`.
- Commits: small, Conventional Commits, one commit per completed sub-step. No bulk commits.
- Remote: push only after user OK. Before the first push, run a full-history `gitleaks` scan and show the result.
- No `git push --force` to `main`, no rewriting published history.

## Milestones

- M0 Git init, orchestrator setup, discovery + questions
- M1 Plan, license, repo skeleton, `.gitignore` + gitleaks, compose stack, CI
- M2 Auth (OIDC + break-glass admin), workspaces, roles, secrets store
- M3 Git projects, inventory (DB + Git-synced), vault integration
- M4 Runner/worker, templates, live logs, run history, schedules
- M5 Connector framework + OPNsense + Proxmox, host merge
- M6 NetBox + monitoring, fleet view, remediation
- M7 Semaphore migration (dry run → import → comparison → cutover)
- M8 Hardening, backup/restore, docs, release v1.0

## Architecture (details: `PLAN.md` §9, `docs/adr/`)

- `app`: Go binary (REST + SSE, OIDC, scheduler, connectors, Git ops), serves the embedded SvelteKit SPA, terminates TLS (ADR-0011).
- `worker`: separate hardened container, pulls jobs from the app API, runs `ansible-playbook` unprivileged; no DB access, no master key, no Docker socket (ADR-0004).
- `postgres`: only state store and job queue (ADR-0003).

## Stack

- Go (chi, pgx/v5, sqlc, goose, slog), single binary with subcommands (`serve`, `worker`, `migrate semaphore`, `admin`, `db migrate`).
- SvelteKit (Svelte 5, adapter-static, TypeScript) + Tailwind CSS.
- PostgreSQL; Docker Compose for deployment.

## Code conventions

- Go: `gofmt`/`goimports`, `golangci-lint`; errors wrapped with context; no global state except registries; table-driven tests.
- Every API handler declares its permission; authorization goes through the central policy package (ADR-0007).
- Secrets: never logged, never returned by the API; use the `secrets` package only (ADR-0005).
- Connectors: GET-only HTTP client wrapper, sanitized fixtures only (no real IPs/hostnames/MACs/tokens).
- Frontend: TypeScript strict, components small, API types generated from OpenAPI.

## Build / test commands

- No Go/Node on the host: everything runs in containers. `make` may be missing on the host – the Makefile targets are thin docker wrappers and can be run by hand.
- `make init` – create `.env` (random DB password), `secrets/master.key`, enable git hooks (`core.hooksPath scripts/hooks`).
- `make test` – `go vet` + `go test` for `./cmd/... ./internal/...` (never `./...`: `web/node_modules` contains stray Go files).
- `make lint` – golangci-lint; web: `docker run --rm -v $PWD/web:/app -w /app node:24-alpine sh -c 'npm ci && npm run lint && npm run check && npm test -- --run && npm run build'`.
- `make up` / `make down` – compose stack (`docker compose up -d --build`); ports from `.env` (`ACP_HTTP_PORT`, `ACP_HTTPS_PORT`).
- Full-history secret scan: `docker run --rm -v $PWD:/repo ghcr.io/gitleaks/gitleaks:v8.21.2 git /repo --redact --config /repo/.gitleaks.toml`.

## Known pitfalls

- Pinned versions: Go 1.27.2, Node 24 (Docker build) / 22 (lockfile created with), Python 3.14, ansible-core 2.21.5, PostgreSQL 18.6, gitleaks 8.21.2.
- A goose "relation goose_db_version does not exist" error in the Postgres log on first start is expected.

- Project subagents (`.claude/agents/`) are only loaded when Claude Code is started inside the repository directory.
- Infrastructure specifics (addresses, hostnames, tokens) belong in `docs/local/` and `secrets/` (both git-ignored), never in tracked files.
- The host is small (4 GiB RAM): use multi-stage builds and memory limits; keep worker concurrency low by default.
- OPNsense instances may use different DHCP backends (Kea, dnsmasq, ISC legacy) – detect per instance, never assume.
- Some upstream APIs are only reachable over plain HTTP on non-standard ports inside an encrypted overlay network – connectors must support per-instance URL, port and TLS settings.
