# ADR-0004: Separate, hardened worker with a pull-based job API

- Status: proposed
- Date: 2026-11-03

## Context
Ansible runs execute untrusted-ish code (playbooks, collections from Git) and need credentials. The app holds the master key and the DB.

## Decision
- A separate `worker` container with ansible-core and common collections bundled; a Go supervisor (`acp worker`) runs `ansible-playbook` as an unprivileged subprocess.
- The worker **pulls** jobs from the app over an internal, token-authenticated API. It receives a run bundle (repo revision, rendered inventory, credentials decrypted just in time) and streams log chunks and per-host events back.
- The worker has **no DB credentials, no master key, no Docker socket**. Read-only rootfs, `cap_drop: [ALL]`, `no-new-privileges`, tmpfs for the run directory, resource limits.
- A run is cancelled via the job API (SIGTERM, then SIGKILL).

## Consequences
A compromised playbook can only see the secrets of its own run. The app must re-render inventories and bundle secrets per run. Scaling out = more worker containers.

## Alternatives
Running Ansible inside the app (no isolation); spawning containers per run via the Docker socket (root-equivalent access, explicitly forbidden).
