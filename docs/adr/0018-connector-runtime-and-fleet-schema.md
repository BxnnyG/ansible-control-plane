# ADR-0018: Connector runtime, ownership and fleet schema

- Status: accepted
- Date: 2026-11-06

## Context
M5 implements CON-1..5, CON-8/9, FLEET-2/3 and INV-5 on top of ADR-0009 (connector interface) and ADR-0010 (host merge). Open points were: who owns connectors (the plan called connector data "global"), where connector credentials live, how syncs are scheduled, how observations are stored, how unified hosts keep a stable identity across re-merges, and how `network` scopes (ADR-0007) resolve to hosts.

## Decision
- **Ownership: connectors belong to a workspace.** All objects, including connectors and their derived networks/hosts/conflicts, carry `workspace_id`. This keeps authorization on the existing per-workspace policy (no cross-workspace leakage, no new "instance admin" role). The same firewall can be configured in two workspaces; for v1.0 (single-workspace installations) this is acceptable. PLAN ORG-1's exception for "global connector data" is dropped.
- **Permissions:** `connector.read` / `connector.write` (admin+, workspace-wide) manage connector configuration, test and manual sync. New `fleet.read` (viewer+) reads networks, hosts and conflicts, filtered by scope; `fleet.manage` (editor+) acknowledges conflicts and edits group rules.
- **Credentials:** connector secrets are stored as `secrets` rows of the workspace (new types `api_key_pair`, `api_token`) and referenced by `connectors.secret_id`. They are decrypted only inside the sync job and never returned or logged; the HTTP wrapper masks `Authorization` headers in errors.
- **HTTP client:** one shared wrapper (`internal/connectors/httpx`) that refuses every method except GET/HEAD at the transport level (CON-8), with per-instance base URL, TLS verify on by default, optional custom CA PEM, timeout and response size limit. OPNsense `search*` endpoints are called with GET and query parameters.
- **Runtime:** connectors are compiled in and registered in a registry (`Type`, config schema, `Test`, `Sync`). The app runs a sync loop (30 s tick) that claims due connectors with `FOR UPDATE SKIP LOCKED` (concurrency 2, per-sync timeout 2 min). Failures back off exponentially (interval × 2^n, capped at 1 h). Every sync writes a `connector_syncs` row (status, duration, counts, sanitized error); the connector row holds last success/error for the UI (CON-2). Manual sync sets `next_sync_at = now()`.
- **Observations:** a sync replaces the connector's observation set in one transaction. One row per observed entity per connector (`host_observations`: `connector_id`, `external_key`, `kind` lease/arp/ndp/static/vm/lxc/node, JSONB `fields` with mac/hostname/fqdn/ips/tags/notes/status, `first_seen`, `last_seen`); entities not reported again keep their `last_seen` and age out (pruned after 30 days). Networks are stored the same way (`networks`: CIDR, VLAN, interface, description, source). Per-field provenance (ADR-0010) is derived during the merge from the observation's connector type.
- **Merge:** after each sync the workspace is re-merged by the pure function `fleet.Merge(observations, priority) → hosts, conflicts`. Results are written as `hosts` (merged fields with source per field, JSONB) plus `host_keys` (mac/name/ip). **Stable identity:** a merged host reuses the existing host id that shares its strongest key (MAC, then name, then IP); ties go to the oldest host. Conflicts are keyed by a deterministic fingerprint (`type` + sorted keys) so acknowledgements survive re-merges; a resolved condition closes the conflict. "Host disappeared" = no observation newer than 24 h (default).
- **Network scopes:** a `network` scope ref names a network id; the scope resolver grants the hosts whose merged IPs lie in that network's CIDR. Inventory hosts are linked to fleet hosts by name/FQDN/IP (`ansible_host`), so network-scoped operators can run templates limited to those hosts.
- **Group rules (INV-5):** rules (`subnet` | `proxmox_tag` | later NetBox role/site/tag) map matching fleet hosts into one DB inventory group. A rule has a preview (add/remove list) and is applied explicitly; Git-synced inventories are changed only through the existing write-back flow, never silently.

## Consequences
- **Hosts without observations are deleted**, not marked gone: the merge result is derived data, so a host exists exactly as long as at least one observation does. The 24 h "host disappeared" conflict covers the window until the observation is pruned (30 days); afterwards the host row is removed and its conflict resolves. Host ids are therefore not retained for hosts that are gone for good.
- A MAC-less observation joins a MAC-bearing host by name or IP only when exactly one such host matches; with several candidates it stays a separate host and the duplicate shows up as a conflict. Loopback, link-local, unspecified and multicast addresses never act as match keys. Source priority is a number (higher wins; NetBox 400 > Proxmox 300 > OPNsense 200 > everything else 100) and can be overridden per connector with the integer config key `priority`.
- Conflict fingerprints use the conflicting key (IP or name) plus the strongest key of each involved host, so a host gaining a second IP or tag does not reset an acknowledgement. A conflict that reappears after being resolved reopens the same row with the acknowledgement cleared.
- Re-merging is cheap at homelab scale (hundreds of hosts) and always reproducible after priority changes.
- Observation history beyond `first_seen`/`last_seen` is not kept; `connector_syncs` keeps counts only.
- A connector configured in two workspaces is synced twice.

## Alternatives
- Global connectors with an instance-level admin role: closer to the original plan, but needs a second authorization layer and cross-workspace filtering for every fleet query.
- One row per observed field (EAV): exact ADR-0010 wording, but many more rows and writes with no extra information over JSONB fields plus the source connector.
