# Connectors and fleet – operator guide

This guide covers the M5 features: read-only connectors (OPNsense, Proxmox VE),
the merged fleet view, conflicts, network scopes and group rules. Design
rationale: ADR-0018 (connectors, fleet, scopes, group rules), ADR-0009
(connector interface), ADR-0010 (host merge), ADR-0007 (scopes). Status: implemented,
live verification against real systems is still pending.

All objects belong to a workspace. Permissions: `connector.read` and
`connector.write` (admin and above, workspace-wide) manage connectors;
`fleet.read` (viewer and above, filtered by network scopes) reads networks, hosts
and conflicts; `fleet.manage` (editor and above) acknowledges conflicts and
edits group rules.

Connectors are **read-only** towards the external system: the HTTP client
refuses every method except GET and HEAD at the transport level. Create a
dedicated API user or token with read privileges only.

## Connector types

### OPNsense

Create an API key and secret for a dedicated user in OPNsense (System, Access,
Users) and grant only the following pages (ACL entries) to that user or its
group:

| Data | Privilege page (GUI) |
|---|---|
| Firmware and system status | Status: System, plus System: Firmware |
| DHCP leases (ISC) | Services: DHCP: Leases (also DHCPv4 service status) |
| DHCP leases and reservations (dnsmasq) | Services: Dnsmasq DNS/DHCP, Status: Dnsmasq leases |
| DHCP leases and reservations (Kea) | Services: Kea DHCP, Status: Kea leases |
| Interfaces overview | Interfaces: Overview |
| ARP and NDP tables | Diagnostics: ARP Table, Diagnostics: NDP Table |
| VLANs | Interfaces: Other Types: VLAN |
| DHCP service status | the status page of the respective DHCP service |

Page names differ slightly between OPNsense releases; if a page is missing the
sync reports it as a warning (see below) and you add the privilege that matches.

**Credential** (secret type `api_key_pair`): JSON with the key and secret, for example
`{"key": "KEY", "secret": "SECRET"}`. It is stored encrypted in the secrets store and
decrypted only inside the sync.

**DHCP backend detection.** Instances may use Kea, dnsmasq or the legacy ISC
server. The connector detects the running backend per instance: Kea first,
then dnsmasq, then ISC (when its leases endpoint answers). Nothing is assumed;
if none is found the sync succeeds with the warning "no DHCP backend detected".
Reservations (Kea, dnsmasq) become static observations, leases become lease
observations, and Kea subnets are added as networks.

### Proxmox VE

Create an API token on a dedicated user (Datacenter, Permissions, API Tokens)
and give the user or token the built-in role **PVEAuditor** on path `/`. With
privilege separation enabled the token needs the role itself. PVEAuditor is
read-only.

**Credential** (secret type `api_token`): the token in the form
`user@realm!name=secret`, for example `acp@pve!monitor=00000000-0000-0000-0000-000000000000`.

The connector reads nodes, VMs and containers (name, status, tags, MACs,
guest-agent addresses where the agent answers). Proxmox tags appear as
`proxmox:<tag>` in the fleet and can drive group rules.

## Configuring a connector

In the UI (Connectors) or with the CLI. Common settings:

| Setting | Meaning |
|---|---|
| Base URL | per instance, including scheme and port (plain HTTP on non-standard ports is allowed inside an encrypted overlay network) |
| Secret | existing secret, or one created from a file/stdin by the CLI |
| TLS verification | on by default; `tls_skip_verify` disables it (lab only) |
| CA certificate | `ca_pem`: extra trust anchor in PEM form for private CAs |
| Timeout | `timeout_seconds` per request; a whole sync is limited to 2 minutes |
| Interval | time between syncs, default 5 minutes, minimum 30 seconds |
| Priority | optional integer, see merge rules |
| Enabled | disabled connectors are not synced |

Failed syncs back off exponentially (interval times 2 per failure, capped at
one hour). Each sync is recorded with status, duration, counts and a sanitized
error; credentials never appear in errors or logs. "Test" checks the
connection without storing data; "Sync" requests an immediate sync.

**Partial sync warnings.** Optional endpoints that answer 403 or 404 do not fail
the sync. The data of that endpoint is skipped and a warning such as
"ARP endpoint: HTTP 403 (skipped; check API key privileges)" is stored with the
sync. Treat warnings as a hint to extend the read-only privileges. Authentication
errors (401) and other failures fail the sync.

## Fleet: merge rules

After each sync the workspace is re-merged from all observations into hosts.

- **Identity** is matched by the strongest shared key: MAC, then name, then IP.
  A re-merge keeps the existing host id (oldest wins on ties).
- **Field priority:** each field takes the value of the connector with the
  highest priority. Defaults: NetBox 400, Proxmox 300, OPNsense 200, other
  types 100. A per-connector `priority` value overrides the type default. List
  fields (addresses, tags) are unions; every field records its sources.
- Hosts exist as long as at least one observation does; observations that are
  not reported again age out after 30 days.

## Conflicts

The merge reports hints, it never blocks:

| Type | Meaning |
|---|---|
| `ip_conflict` | the same address is claimed by hosts with different MACs |
| `duplicate_hostname` | the same name belongs to different hosts |
| `host_disappeared` | no observation newer than 24 hours |

States: open, acknowledged, resolved. **Acknowledge** (`fleet.manage`) keeps a
known, accepted situation out of the open list; the acknowledgement survives
re-merges because conflicts have a stable fingerprint. A conflict resolves
itself when the condition ends. Conflict responses list the display names of the
hosts the caller may see.

## Network scopes

A membership can be limited to `network` scopes (ADR-0007). A scope names a
network discovered by a connector; the member sees the hosts with an address in
that network's CIDR, the network list reduced to their networks (including the
host count per network), and only conflicts whose hosts are all visible.
Inventory hosts are linked to fleet hosts by name, FQDN or `ansible_host`, so
network-scoped operators can run templates limited to those hosts.

## Group rules

A group rule maps matching fleet hosts into one inventory group. Matching by
subnet (`cidr`) or Proxmox tag; target inventory and group (created on apply
when missing). See [Group rules in the inventory guide](projects-and-inventories.md#5-group-rules)
for details.

- **Preview** shows the add/remove list without changing anything.
- **Apply** is always explicit and recomputes the diff under the inventory
  lock. For Git inventories it commits to the write branch.
- **Modes:** `add-only` (default) only adds; `sync` also removes memberships the
  rule added earlier that no longer match. Manually added hosts are never removed.
- **Naming rule:** the FQDN if there is one, otherwise the short hostname with
  `ansible_host` set to the primary IP, otherwise the primary IP.
- Rules need `fleet.manage` and `inventory.write`; they act on the whole fleet.

## Admin CLI

Run inside the app container, for example
`docker compose exec app acp admin connector list --workspace default`.

- `acp admin connector types` – registered types and their settings.
- `acp admin connector list|create|test|sync` – `create` takes `--type`, `--name`,
  `--url`, `--secret NAME|ID` or `--secret-file PATH|-` with `--secret-type`, `--interval`,
  `--ca-file`, `--insecure-skip-verify`, `--timeout SECONDS`, `--set key=value` and
  `--disabled`. Secret values are never accepted as command-line arguments.
  `test` and `sync` take `--connector NAME|ID`.
- `acp admin fleet merge|hosts|conflicts` – re-run the merge, list hosts, list
  conflicts (`--state open|resolved|all`). All take `--workspace SLUG`.
- `acp admin group-rule list|preview|apply` – `--rule NAME|ID` selects a rule;
  `apply` accepts `--author-name` and `--author-email` for Git inventories.
