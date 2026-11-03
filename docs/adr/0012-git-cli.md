# ADR-0012: Use the git CLI instead of a Git library

- Status: accepted
- Date: 2026-11-03

## Decision
Git operations (clone, fetch, checkout, commit/push for Git-synced inventories and vault changes) shell out to the `git` binary with a controlled environment (`GIT_SSH_COMMAND` with a temporary key file and pinned `known_hosts`, no credential helpers, timeouts).

## Consequences
Full compatibility with SSH, submodules, LFS and server quirks; `git` must be present in app and worker images.

## Alternatives
go-git (pure Go, but incomplete SSH/LFS/protocol support).
