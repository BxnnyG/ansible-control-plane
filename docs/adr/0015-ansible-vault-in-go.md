# ADR-0015: Ansible Vault handled natively in Go

- Status: accepted
- Date: 2026-11-04

## Context
VAULT-1..4: vault IDs bound to secrets, detection, encrypt/decrypt/rekey in the UI. The app image has no Python/Ansible; the worker is not allowed to hold long-lived secrets.

## Decision
- Implement the Ansible Vault format (`$ANSIBLE_VAULT;1.1;AES256` and `1.2;AES256;<vault-id>`: PBKDF2-HMAC-SHA256 10000 rounds, AES-256-CTR, HMAC-SHA256, hexlified envelope) in an internal Go package, tested against fixtures produced by the pinned `ansible-vault`.
- Vault identities (`label` → secret of type `vault_password` or external provider) are workspace objects. External providers (VAULT-4) use a provider interface; v1.0 ships `secret` and `script` (executed in the worker at run time only).
- Decrypt-for-view is a separate permission, audited per call, never cached, and returned only over the authenticated API with `Cache-Control: no-store`.
- Changes (encrypt, rekey, edit) are committed back through the same Git write path as ADR-0014.

## Consequences
No Python dependency in the app; full control over masking. The format implementation must track Ansible changes (low churn historically).

## Alternatives
Calling `ansible-vault` in the worker (round trip per UI action, secrets transported to the worker outside runs).

## Implementation notes (M3)
- Permissions: `vault.read` (editor and above, identity metadata and the project vault listing), `vault.manage` (admin and above, identity CRUD, encrypt, rekey) and `vault.decrypt` (admin and above only). All three are workspace-wide and unavailable to scoped memberships.
- Identities: `/api/v1/workspaces/{ws}/vault-identities`. The label `default` stands for the unlabeled vault (envelope 1.1); other labels produce 1.2 envelopes with that vault-id. A `script` identity only stores path and arguments; decrypt and encrypt in the app answer 422 `provider_not_supported_in_app`.
- Project operations: `GET .../projects/{id}/vault`, `POST .../vault/decrypt|encrypt|rekey`. Decrypt picks the identity by the vault-id in the envelope header and falls back to `default`; it audits every call (`vault.decrypt`, path and key path only), fails closed if the audit entry cannot be written, and responds with `Cache-Control: no-store`.
- Encrypt and rekey go through the write-back path of ADR-0014 (default branch `acp/vault`, 409 `git_conflict` on a moved branch). Inline edits re-encode the YAML document, which keeps comments but may normalize indentation; multi-document files are refused. Plaintext is limited to 1 MiB.
