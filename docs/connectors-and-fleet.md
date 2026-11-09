# Connectors and fleet – operator guide

This guide covers the M5 features: read-only connectors (OPNsense, Proxmox VE, NetBox, Checkmk), the alert webhook,
the merged fleet view, conflicts, network scopes and group rules. Design
rationale: ADR-0018 (connectors, fleet, scopes, group rules), ADR-0009
(connector interface), ADR-0010 (host merge), ADR-0007 (scopes). Status: implemented,
live verification against real systems is still pending.

All objects belong to a workspace. Permissions: `connector.read` and
`connector.write` (admin and above, workspace-wide) manage connectors;
`fleet.read` (viewer and above, filtered by network scopes) reads networks, hosts,
alerts and conflicts; `fleet.manage` (editor and above) acknowledges conflicts and
edits group and remediation rules; `fleet.remediate` (operator and above) starts a
fix from an alert.

Connectors are **read-only** towards the external system: the HTTP client
refuses every method except GET and HEAD at the transport level. Create a
dedicated API user or token with read privileges only.

## Connector types

### OPNsense

Create an API key and secret for a dedicated user in OPNsense (System, Access,
Users) and grant only the following pages (ACL entries) to that user or its
group:

| Data | Privilege page (GUI, names as in OPNsense 26.7) |
|---|---|
| Firmware and system status | System: Status, System: Firmware |
| DHCP leases (ISC legacy) | Services: ISC DHCPv4: Leases |
| DHCP leases and reservations (dnsmasq) | Services: Dnsmasq DNS/DHCP: Settings |
| DHCP leases and reservations (Kea) | Services: Kea DHCP, Status: Kea leases |
| Interfaces overview | Status: Interfaces (older releases: Interfaces: Overview) |
| ARP and NDP tables | Diagnostics: ARP Table, Diagnostics: NDP Table |
| VLANs | Interfaces: Other Types: VLAN |

The connector calls the snake_case API paths (for example
`diagnostics/interface/get_arp`). Recent releases check ACLs only for those;
the legacy camelCase aliases answer 403 even with the right privilege. The ISC
DHCPv4 service status has no privilege page in recent releases; a 403 there is
ignored silently because the leases endpoint decides whether ISC is in use.

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
and give the built-in role **PVEAuditor** on path `/`. With privilege
separation enabled (the default) a token's rights are the intersection of the
token's and the user's permissions, so grant PVEAuditor to **both** the user
and the token. PVEAuditor is read-only.

Guest IPs come from the QEMU guest agent (VMs) and the container interface list
(LXC). Templates are skipped. Interfaces local to the guest (loopback, Docker
`docker*`/`br-*`, `veth*`, CNI/Kubernetes and similar bridges) are ignored, since
their addresses (for example `172.17.0.1`) repeat on every Docker host.

**Credential** (secret type `api_token`): the token in the form
`user@realm!name=secret`, for example `acp@pve!monitor=00000000-0000-0000-0000-000000000000`.

The connector reads nodes, VMs and containers (name, status, tags, MACs,
guest-agent addresses where the agent answers). Proxmox tags appear as
`proxmox:<tag>` in the fleet and can drive group rules.

### NetBox

**Base URL**: `http(s)://host[:port]` without `/api`; plain HTTP on a
non-standard port (for example `http://netbox.example.test:8080`) is supported.
**Credential** (secret type `api_token`): a NetBox API token, sent as
`Authorization: Token <token>`.

Use a **read-only token** (leave "Write enabled" off) of a user whose
permissions allow `view` on `ipam.prefix`, `ipam.ipaddress`, `dcim.device`,
`dcim.interface`, `virtualization.virtualmachine` and
`virtualization.vminterface`. ACP only issues GET requests. A missing
permission (HTTP 403/404 on one endpoint) produces a warning and a partial
sync; an invalid token (401) fails the sync.

The connector reads IPAM prefixes (as networks, including the VLAN id),
devices (observation kind `device`) and virtual machines (kind `vm`). Name,
status, primary IPs, IPs assigned to interfaces, interface MACs (also the
NetBox 4.2+ `primary_mac_address` / `mac_addresses`) and tags are merged into
the fleet; devices without a name are skipped. NetBox tags appear as
`netbox:<slug>`. Site, role, tenant, platform and cluster (slugs) are kept as
host attributes. Default merge priority is 400 (highest, see below).

### Checkmk

**Base URL**: the REST API root `https://host/<site>/check_mk/api/1.0`. The site
URL `https://host/<site>` is accepted too; `/check_mk/api/1.0` is appended when
the path does not already contain `/check_mk/api/`.
**Credential** (secret type `api_key_pair`): *key* = the name of an
**automation user**, *secret* = its automation secret. ACP sends
`Authorization: Bearer <user> <secret>`.

Create a dedicated automation user in Checkmk (Setup, Users) with a
**read-only role** (for example the built-in `guest` role, or a copy of it) that
may see the hosts and services to be reported. ACP only issues GET requests and
never acknowledges or changes anything in Checkmk.

Each sync reads the **host problems** (`GET /domain-types/host/collections/all`,
Livestatus filter `state != 0`) and the **service problems**
(`GET /domain-types/service/collections/all`, same filter) and replaces the
connector's open alert set: alerts that are no longer reported are resolved.
If either request fails, the whole sync fails and no alert is resolved.
Severity mapping: host DOWN and UNREACH are `critical`; service CRIT is
`critical`, WARN is `warning`, UNKNOWN is `unknown`. Acknowledged problems and
problems in a scheduled downtime are still reported; the labels `acknowledged`
and `in_downtime` (`true`/`false`) and `state` carry that information. The
alert key is `host:<name>` or `svc:<host>/<service>`.

### Alert webhook

The `alert_webhook` type receives alerts pushed by other systems (Alertmanager,
Zabbix media type, scripts). It needs no base URL and does not pull. On create,
ACP **generates a secret and shows it once** together with the endpoint
`POST /api/hooks/alerts/<connector id>` (the path is also listed on the
connectors page). Alternatively select an existing `token` secret. Requests
authenticate with `Authorization: Bearer <secret>`; the comparison is constant
time. Every rejection (unknown connector, disabled connector, wrong or missing
secret) answers `404 Not Found`. Bodies are limited to 1 MiB, each source
address may send 60 requests per minute (`429` above that), and a delivery may
contain up to 1000 alerts. A valid delivery answers `202` with the number of
applied alerts; a malformed payload answers `400` (only after authentication).
Accepted deliveries are audited with the alert count only, never the payload.

Alerts are upserted and resolved individually; the sync loop never resolves
webhook alerts. Resolved alerts are removed after 30 days.

**ACP JSON format**

```
curl -X POST https://acp.example.test/api/hooks/alerts/<connector-id> \
  -H "Authorization: Bearer <secret>" -H "Content-Type: application/json" \
  -d '{"alerts":[{
        "key": "alert-0001", "host": "web1.example.test", "ip": "192.0.2.10",
        "service": "disk /", "severity": "critical", "summary": "97% used",
        "labels": {"team": "ops"}, "status": "firing",
        "started_at": "2026-11-06T10:00:00Z"}]}'
```

`key` (required, stable per alert) identifies the alert within the connector.
`status` is `firing` (default) or `resolved` (only `key` is needed to resolve).
`severity` is `critical`, `warning`, `unknown` (default) or `info`. `host` and
`ip` are used to link the alert to a host. Label values must be strings.

**Alertmanager**: a payload with `"version": "4"` is read as an Alertmanager
webhook. `fingerprint` becomes the key, `status` the state, `startsAt` the start
time. `labels.host`, `labels.hostname` or `labels.instance` select the host (a
`:port` suffix is stripped; an IP address is used as address), `labels.alertname`
is the service, `labels.severity` maps (`critical`, `error`, `page`, `high` to
critical; `warning`, `warn` to warning; `info`, `none`, `low` to info; anything
else to unknown), `annotations.summary` (else `description`) is the summary.
Receiver configuration:

```
receivers:
  - name: acp
    webhook_configs:
      - url: https://acp.example.test/api/hooks/alerts/<connector-id>
        send_resolved: true
        http_config:
          authorization:
            type: Bearer
            credentials_file: /etc/alertmanager/acp-token
```

## Alerts

Alerts of all monitoring connectors are stored (table `alerts`) and shown under
Fleet, Alerts and in the host list and detail view (open alert count and worst
severity, API fields `open_alerts` and `worst_severity`). After every host merge
and after every webhook delivery the alerts are **linked** to merged hosts:
first by host name (exact name, hostname or FQDN, case-insensitive; then short
name against short name), then by IP address. A name that matches several hosts
is narrowed by the address; if it stays ambiguous, or an address belongs to
several hosts, the alert stays unlinked. Unlinked alerts are visible to
workspace-wide memberships only; network-scoped memberships see the alerts of
the hosts in their scope.

`GET /api/v1/workspaces/{ws}/alerts` (`fleet.read`) takes the filters
`state=open|resolved`, `host_id` and `connector` (connector id). Open alerts come
first, worst severity first. `GET .../hosts?alerts=true` lists hosts with open
alerts. Alerts are read-only in ACP; acknowledging in the monitoring system is
not supported in v1.0.

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

## Host status

Status is computed whenever hosts are read; nothing is stored apart from the merged
live-state inputs, so it always matches the latest syncs and runs.

| Field | Meaning |
|---|---|
| `online` | `online` when a lease, ARP or NDP observation was seen within 15 minutes, or a VM, LXC or device reports `running` / `active`. `offline` when such a source exists but neither holds. `unknown` when the host has no live-state source (for example static maps only). |
| `ansible` / `managed` | The inventory hosts linked to the fleet host. Linking is the same as for network scopes: the inventory host name or `ansible_host` equals a name, FQDN or address of the fleet host. |
| `last_run` | The newest finished run that touched a linked inventory host (run, template, time, result `ok`, `changed`, `failed` or `unreachable`, and whether it was a check run). Runs of deleted templates carry no inventory and are ignored. |
| `reachable` | `yes`, or `no` when the last run reported the host unreachable; `unknown` when it never ran. |
| `drift` | Set when the newest check-mode run for the host reported changes (with that run). A later clean check run clears it. |
| `open_alerts` | Open monitoring alerts linked to the host. |

The network overview adds counters per network (hosts whose merged addresses lie in the
CIDR, limited to what the caller may see): `online`, `ansible_ok` (last run did not fail),
`failed` (last run failed or hit an unreachable host), `never_managed` (no linked inventory
host, or none ever ran) and `open_alerts`.

## Unmanaged hosts

A fleet host that is linked to no inventory host of the workspace is *unmanaged*. The
**Unmanaged** tab (`GET /hosts?managed=false`) lists them, scope-filtered like the host list.

**Add to inventory** (`POST /hosts/{id}/add-to-inventory` with `inventory_id`, `group`
and an optional `name`) creates the inventory host (default name: the merged host name,
`ansible_host`: the primary address, IPv4 preferred) and puts it into the group, which is
created if missing. It needs `fleet.read` plus `inventory.write` on the target inventory.
DB inventories are written directly; Git inventories go through the write-back flow and
get a commit on their write branch (same path as applying a group rule). A name that
already exists in the inventory is refused (409), as is a host that is meanwhile linked.
Every add is written to the audit log.

## Tracked networks

Not every discovered network belongs in the fleet (Wi-Fi, phones, guests). Each network has a
`tracked` flag (default: true), and the workspace has a default for newly discovered networks
(`fleet_new_networks_tracked`, default true). Networks are identified by connector and external
key, so the flag survives every sync; a network that disappears from its connector is pruned and,
if it comes back, starts with the workspace default.

A host is **hidden by default** when it has an address in at least one known network and every
such network is untracked. A host in at least one tracked network stays visible; a host with no
address in any known network also stays visible, because nothing marks it as opted out.

Hidden by default (add `include_untracked=true` to see everything):

- `GET /hosts` (this includes `managed=false`, the Unmanaged tab) and the dashboard hosts tile
- `GET /alerts` (alerts linked to hidden hosts; unlinked alerts stay visible)

Not affected: `GET /networks` always lists every network (with `tracked`) and per-network host
counts, conflicts, group rules and remediation targeting (they keep using all hosts, so a
group rule with a CIDR still matches hosts of untracked networks; tracking only changes the
views). Merge and connector syncs also keep collecting untracked networks.

- `PATCH /networks/{id}` with `{"tracked": bool}`, and `PATCH /networks` with either
  `{"ids": [...], "tracked": bool}` or `{"track_only": [...]}` (tracks exactly those, untracks
  all others). `GET`/`PUT /fleet/settings` read and set `new_networks_tracked`.
  Changing needs `fleet.manage`; network-scoped memberships may not change it. Changes are audited.
- UI: the **Fleet** page has a Tracked toggle per network, "Track only selected..." and
  "Show hidden networks" (hidden networks are dimmed, hosts of hidden networks appear then);
  the setup wizard has a **Networks** step and **Settings** shows the same panel.
- CLI: `acp admin fleet network list|track|untrack --workspace SLUG [--network NAME|ID]`;
  `--network` takes the id, or a unique external key, description or CIDR.

## Network scopes

A membership can be limited to `network` scopes (ADR-0007). A scope names a
network discovered by a connector; the member sees the hosts with an address in
that network's CIDR, the network list reduced to their networks (including the
host count per network), and only conflicts whose hosts are all visible.
Inventory hosts are linked to fleet hosts by name, FQDN or `ansible_host`, so
network-scoped operators can run templates limited to those hosts.

## Group rules

A group rule maps matching fleet hosts into one inventory group. Matching by
subnet (`cidr`), Proxmox tag, or the NetBox kinds `netbox_role` (`role`), `netbox_site` (`site`) and `netbox_tag` (`tag`), each taking a NetBox slug; target inventory and group (created on apply
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

## Remediation

A **remediation rule** tells ACP which template can fix which alert. It is purely
a manual aid: a matching open alert gets a **Fix** button (alert list and host
detail); nothing is ever started automatically, by a sync, a webhook or a schedule.

A rule has:

- **Match:** connector type (empty = any), `service_regex`, `summary_regex` (RE2,
  unanchored, empty matches everything, `(?i)` for case-insensitive) and a minimum
  severity (`info` < `unknown` < `warning` < `critical`). Only open alerts match, and the
  alert must be linked to a fleet host.
- **Template and limit target:** the template (its inventory is where the hosts are
  looked up; it must allow limit overrides) and what the run is limited to:
  - `host` – the inventory host(s) linked to the alert's fleet host,
  - `group` – the named inventory group; it must contain the host,
  - `network` – the inventory hosts linked to fleet hosts in the network that contains the
    host (the most specific known network for its primary address).
- **Check first:** the Fix button first queues a **check-mode** run. When it has finished
  the UI shows "Apply now", which queues the real run. There is no automatic follow-up; the
  apply step needs the template to allow check overrides.

The rule editor (**Fleet > Remediation**) has a "Test against open alerts" button that
lists the open alerts the current (unsaved) criteria would match. Rules are managed with
`fleet.manage` on an unscoped membership (rules show templates of the whole workspace).

A fix needs `fleet.remediate` (operator and above). Before the run is queued, the effective
host set of the limit (group and network targets can be larger than the alert host) is
checked against the caller's scopes; if any host is outside, the request is refused with 403
and an `authz.denied` audit entry, and nothing is queued. A fix that is started writes the
audit entry `remediation.run`; the run records the rule, alert and phase (`check` or `apply`)
in its `remediation` field. Rule changes are audited as `remediation.rule.create|update|delete`.

API (all under `/api/v1/workspaces/{ws}`): `GET|POST /remediation-rules`,
`GET|PUT|DELETE /remediation-rules/{id}`, `POST /remediation-rules/preview`,
`GET /remediation-rules/applicable` (rules per open alert), `GET /alerts/{id}/remediations`,
`POST /alerts/{id}/remediate` with `{"rule_id": "...", "skip_check": false}`.

## Bulk actions

Select hosts in the fleet host list and choose **Run template on N host(s)**: pick a
template and whether to run in check mode. ACP ships no hidden playbooks, so
reachability or fact-gathering runs are simply templates you create for that.

- The selected fleet hosts are translated to their linked inventory hosts; one run is
  queued with `--limit` over them.
- Fleet hosts that are not linked to any inventory host are **skipped** and reported in
  the result.
- Hosts linked only to inventories other than the template's make the request fail with
  `422` and the list of those hosts.
- The template must allow limit overrides (and check overrides for check mode). The effective
  host set is scope-checked like any other run start (403 and `authz.denied` otherwise).
- Permission: `run.execute` (operator and above). The run records `source: bulk`; the
  audit entry is `fleet.bulk_run`. At most 500 hosts per request.

API: `POST /fleet/bulk-run` with `{"host_ids": [...], "template_id": "...", "check_mode": true}`.

## Admin CLI

Run inside the app container, for example
`docker compose exec app acp admin connector list --workspace default`.

- `acp admin connector types` – registered types and their settings.
- `acp admin connector list|create|test|sync` – `create` takes `--type`, `--name`,
  `--url`, `--secret NAME|ID` or `--secret-file PATH|-` with `--secret-type`, `--interval`,
  `--ca-file`, `--insecure-skip-verify`, `--timeout SECONDS`, `--set key=value` and
  `--disabled`. Secret values are never accepted as command-line arguments.
  `test` and `sync` take `--connector NAME|ID`. Creating an `alert_webhook` connector without
  a secret prints the generated secret once.
- `acp admin connector update --connector NAME|ID` – changes only the given settings:
  `--url`, `--secret NAME|ID`, `--interval`, `--ca-file` (a PEM bundle may hold several
  CA certificates), `--insecure-skip-verify=true|false`, `--timeout`, `--set key=value`,
  `--unset key`, `--enable` / `--disable`, `--secret-file PATH|-` (replaces the value of the
  connector's current secret in place; without a secret it creates `connector-<name>` and links
  it; `--secret-type` defaults to `api_token` and must match an existing secret's type).
  Config changes are merged into the stored config. Both actions are audited as `cli`.
- `acp admin connector delete --connector NAME|ID [--keep-secret]` – deletes the connector with
  its observations and alerts, and its secret unless `--keep-secret` or another connector or
  object still references it. Deleting an `alert_webhook` connector (also via the API) removes
  its generated secret.
- `acp admin fleet merge|hosts|unmanaged|conflicts` – re-run the merge, list hosts, list
  unmanaged hosts (`--include-untracked` also shows hosts of untracked networks), list conflicts (`--state open|resolved|all`). All take `--workspace SLUG`.
- `acp admin remediation list|create` – list rules, or create one with `--name`, `--template NAME|ID`,
  `--target host|group|network` (`--group NAME`), `--connector-type`, `--service-regex`,
  `--summary-regex`, `--min-severity`, `--check-first`, `--disabled`.
- `acp admin alert remediate --alert ID --rule NAME|ID [--skip-check]` – start the fix of an
  alert with workspace authority (no scope check, no audit entry, like `acp admin run start`).
- `acp admin fleet network list|track|untrack --network NAME|ID` – choose which networks the
  fleet tracks (audited as `cli`).
- `acp admin fleet add --host NAME|ID --inventory SLUG --group GROUP` – add an unmanaged
  host to an inventory group (`--name` overrides the host name; `--author-name` and
  `--author-email` are used for Git inventories).
- `acp admin group-rule list|preview|apply` – `--rule NAME|ID` selects a rule;
  `apply` accepts `--author-name` and `--author-email` for Git inventories.
