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
