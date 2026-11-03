# ADR-0010: Host identity merge with per-field provenance

- Status: proposed
- Date: 2026-11-03

## Decision
- Connectors store raw `HostObservation`s (source, field, value, seen_at); the unified `Host` is derived.
- Matching order: MAC address > FQDN/hostname (normalized) > IP address.
- Field values are chosen by configurable source priority (default NetBox > Proxmox > OPNsense > monitoring); the UI shows the source of each field.
- Contradictions (same IP different MAC, duplicate hostname, host disappeared) create `HostConflict` hints; nothing is silently overwritten. Hints can be acknowledged.
- The merge is a pure function over observations, covered by table-driven unit tests.

## Consequences
Re-merge is always possible after priority changes; observation storage grows and needs pruning.
