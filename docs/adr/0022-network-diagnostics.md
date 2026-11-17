# ADR-0022: Network diagnostics scope

- Status: accepted (scope only, not implemented; NET-2 site overview added 2026-11-17)
- Date: 2026-11-17

## Context
The operator runs two home sites (each behind its own OPNsense) plus an offsite backup host. Sites are linked by a routed WireGuard site-to-site tunnel and by a NetBird overlay for remote users. Debugging "host A cannot reach host B" today means visiting each firewall GUI, the overlay management UI and DNS one after another.

A broader "network control plane" (central rule editing, group-to-IP aliases, cluster-style UI on every node, overlay connector) was evaluated and rejected by the user on 2026-11-17. Firewall logic and rule management stay on the routers. ACP adds visibility and diagnosis only.

## Decision
Four features are in scope, in this order:

1. **Reachability matrix (NET-1).** Operators define probe sources (inventory hosts, typically one per site) and targets (host + check: ICMP, TCP port, DNS lookup). A scheduled run of a bundled probe playbook executes the checks from each source; results are stored per (source, target) with status, latency and timestamp. The UI shows a source × target matrix. A source that cannot be reached by the worker is shown as "probe unreachable", distinct from a failed check, so a dead tunnel is not mistaken for many dead targets. Result transport from the run to the database is decided during implementation (preferred: structured per-host result data captured by the worker callback, no log parsing).
2. **Site network overview (NET-2).** One page per site with interfaces/VLANs and subnets, gateways and their status, routing table, WireGuard site-to-site tunnels and the overlay interface as seen by the router (up/down, handshake age, transfer counters). Data comes from the OPNsense connector only; it is the same data NET-3 uses to annotate hops.
3. **Path diagnosis (NET-3).** Input: source host and target (host or IP). ACP runs a traceroute and a DNS resolution on the source host (normal run, audited, permission-checked), reads routes, gateway status and WireGuard tunnel status from the OPNsense connector, and annotates each hop with what it knows (router, tunnel, site). Firewall rules are **not** evaluated; there is no automatic verdict beyond "path ends at hop N".
4. **Desired/actual drift (NET-4).** Desired network state (aliases, static routes, WireGuard tunnels) lives as YAML in the Ansible Git repository in a schema documented by ACP. Actual state comes from the OPNsense connector. ACP shows the difference per site and offers to start the matching template (check mode first). ACP never writes to the routers itself.

Connector consequences: the OPNsense connector gains read-only collection of routes, gateway status, WireGuard peers/status, overlay interface status and alias contents (GET only via `internal/connectors/httpx`, verified against the installed versions, sanitized fixtures).

Access path (decided 2026-11-17): all router data comes from the OPNsense connector over the REST API with a dedicated API user per instance whose privileges cover only the needed status/diagnostics pages (no GUI-wide or shell rights). The connector also collects system status (uptime, firmware version, gateway status) for the overview. Reading via Ansible over SSH is rejected: OPNsense SSH access is effectively root, so a key in the worker would grant full control of the firewall for a read-only use case. NET-4 remediation templates use a separate API key with write privileges, stored as an Ansible vault/ACP secret and used only by those templates, never by the connector.

## Rejected
- Editing firewall rules or aliases in ACP: duplicates the router GUIs, breaks the read-only connector rule; writes go through Ansible from Git.
- Dynamic group aliases (identity → IP): belongs in a plugin on each router so firewalls do not depend on a single ACP host.
- NetBird connector: not needed for the three features; overlay hops appear as tunnel addresses in traceroute and router interface data.
- Clustering / UI reachable from every node: one instance, reachable over site-to-site and overlay, is enough for two sites.

## Consequences
- Hard rule 4 holds: no new write path to external systems.
- NET-1 and NET-3 depend on the worker reaching probe hosts at every site; when the tunnel is down, the matrix shows it rather than hiding it.
- Firewall-caused failures appear as "path ends at hop N" in NET-3; finding the exact rule stays a router task.
