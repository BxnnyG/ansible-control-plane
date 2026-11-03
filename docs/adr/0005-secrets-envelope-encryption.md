# ADR-0005: Envelope encryption for secrets with a pluggable provider

- Status: accepted
- Date: 2026-11-03

## Context
SSH keys, vault passwords and API tokens must be stored encrypted; a later OpenBao integration is planned.

## Decision
- Each secret has its own random data key (AES-256-GCM); the data key is wrapped with a key-encryption key derived (HKDF-SHA256) from a 256-bit master key.
- The master key is read from a file (Docker secret, default) or an env variable; it is never stored in the DB.
- `SecretProvider` interface (`Wrap/Unwrap/Rotate`); `local` provider in v1.0, OpenBao/Vault transit later.
- Secrets are write-only via API/UI; associated data (secret ID, workspace ID) bound into the GCM tag.
- CLI `acp admin rotate-master-key` re-wraps all data keys.

## Consequences
Losing the master key means losing all secrets – backup docs must cover it explicitly.

## Alternatives
pgcrypto in the DB (key would reach the DB server); age/sops files (no fine-grained access).
