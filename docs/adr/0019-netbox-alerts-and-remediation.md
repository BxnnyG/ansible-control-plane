# ADR-0019: NetBox, monitoring alerts, host status and remediation

- Status: accepted
- Date: 2026-11-06

## Context
M6 covers CON-6 (NetBox), CON-7 (monitoring), FLEET-1, FLEET-4..7 and the NetBox kinds of INV-5 on top of the M5 connector runtime (ADR-0018). Open points: how NetBox intent data enters the merge, how alerts are stored and linked to hosts, which monitoring systems are supported, how per-host status is derived, and how remediation is triggered.

## Decision
- **NetBox** is a regular pull connector (`netbox`, API token secret `api_token`, `Authorization: Token`). Prefixes become networks; devices and virtual machines become observations (kind `device` / `vm`) with primary/interface IPs, interface MACs, name, status, tags. Site, role, tenant and platform go into new optional `Fields.Attrs` (string map) so group rules can match them without schema changes. Default priority 400 (ADR-0018). Pagination via `limit`/`offset`, per-instance URL incl. plain HTTP on non-standard ports.
- **Group rule kinds** `netbox_role`, `netbox_site`, `netbox_tag` register in `internal/grouprules` and match `Fields.Attrs` / tags of NetBox-sourced observations of the merged host.
- **Alerts are stored** in `alerts` (workspace, connector, `external_key`, host match keys hostname/IP, resolved `host_id`, service, severity `critical|warning|unknown|info`, summary, labels JSONB, `started_at`, `last_seen_at`, `resolved_at`). A pull sync replaces the connector's open set: alerts no longer reported get `resolved_at`. Host linking happens after every merge (name/FQDN first, then IP, ambiguous → unlinked). Resolved alerts are pruned after 30 days.
- **Monitoring adapters in v1.0:** Checkmk REST API (GET host and service problems, automation user bearer secret) and a **generic alert webhook** connector (`alert_webhook`): `POST /api/hooks/alerts/{connector_id}` with an HMAC/bearer secret shown once, accepting a documented ACP JSON format and the Alertmanager webhook payload. Webhook alerts carry their own resolve state. Zabbix (JSON-RPC needs POST, breaks the GET-only transport of CON-8) and a pulling Alertmanager adapter are deferred; Alertmanager is covered through the webhook.
- **No acknowledge in M6.** The opt-in monitoring write (CON-8 exception) is out of scope for v1.0 unless requested later.
- **Host status (FLEET-4)** is computed on read, not stored: online = observation with a live state (lease/ARP/NDP seen within 15 min, or VM/LXC/device status running/active); Ansible reachability, last run and drift come from `run_host_results` of the newest run touching the linked inventory host (reachability = last run not `unreachable`; drift = newest check-mode run reported changes). Network overview counts (FLEET-1) aggregate the same values per network.
- **Unmanaged (FLEET-5)** = fleet hosts not linked to any inventory host of the workspace. "Add to inventory" writes a DB inventory host (with `ansible_host`) into a chosen group; Git inventories go through the write-back flow.
- **Remediation (FLEET-6)** rules map (connector type, service/summary regex, minimum severity) to a template, a limit target (`host` | `group` | `network`) and `check_first`. Remediation is **manual only**: a fix button starts the run (and the check run first when configured); there is no automatic remediation. Permission `fleet.remediate` (operator+); the effective host set is checked against the caller's scope before queuing.
- **Bulk actions (FLEET-7)** start a template run with `--limit` over the selected hosts after the same scope check. Built-in reachability/fact runs use templates the operator selects; ACP ships no hidden playbooks.

## Consequences
- NetBox becomes the highest-priority source for names; stale NetBox data shows up as conflicts, never silently wins over live leases for online state.
- Status values are always consistent with the latest runs and observations; at homelab scale the read-time joins are cheap.
- Zabbix users need the webhook (a Zabbix media type) until a read-only JSON-RPC exception is decided.
