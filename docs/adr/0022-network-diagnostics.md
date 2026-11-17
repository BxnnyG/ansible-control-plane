# ADR-0022: Network diagnostics scope

- Status: accepted (scope only, not implemented)
- Date: 2026-11-17

## Context
The operator runs two home sites (each behind its own OPNsense) plus an offsite backup host. Sites are linked by a routed WireGuard site-to-site tunnel and by a NetBird overlay for remote users. Debugging "host A cannot reach host B" today means visiting each firewall GUI, the overlay management UI and DNS one after another.

A broader "network control plane" (central rule editing, group-to-IP aliases, cluster-style UI on every node, overlay connector) was evaluated and rejected by the user on 2026-11-17. Firewall logic and rule management stay on the routers. ACP adds visibility and diagnosis only.

## Decision
Three features are in scope, in this order:

1. **Reachability matrix (NET-1).** Operators define probe sources (inventory hosts, typically one per site) and targets (host + check: ICMP, TCP port, DNS lookup). A scheduled run of a bundled probe playbook executes the checks from each source; results are stored per (source, target) with status, latency and timestamp. The UI shows a source × target matrix. A source that cannot be reached by the worker is shown as "probe unreachable", distinct from a failed check, so a dead tunnel is not mistaken for many dead targets. Result transport from the run to the database is decided during implementation (preferred: structured per-host result data captured by the worker callback, no log parsing).
2. **Path diagnosis (NET-2).** Input: source host and target (host or IP). ACP runs a traceroute and a DNS resolution on the source host (normal run, audited, permission-checked), reads routes, gateway status and WireGuard tunnel status from the OPNsense connector, and annotates each hop with what it knows (router, tunnel, site). Firewall rules are **not** evaluated; there is no automatic verdict beyond "path ends at hop N".
3. **Desired/actual drift (NET-3).** Desired network state (aliases, static routes, WireGuard tunnels) lives as YAML in the Ansible Git repository in a schema documented by ACP. Actual state comes from the OPNsense connector. ACP shows the difference per site and offers to start the matching template (check mode first). ACP never writes to the routers itself.

Connector consequences: the OPNsense connector gains read-only collection of routes, gateway status, WireGuard peers/status and alias contents (GET only via `internal/connectors/httpx`, verified against the installed versions, sanitized fixtures).

## Rejected
- Editing firewall rules or aliases in ACP: duplicates the router GUIs, breaks the read-only connector rule; writes go through Ansible from Git.
- Dynamic group aliases (identity → IP): belongs in a plugin on each router so firewalls do not depend on a single ACP host.
- NetBird connector: not needed for the three features; overlay hops appear as tunnel addresses in traceroute and router interface data.
- Clustering / UI reachable from every node: one instance, reachable over site-to-site and overlay, is enough for two sites.

## Consequences
- Hard rule 4 holds: no new write path to external systems.
- NET-1 and NET-2 depend on the worker reaching probe hosts at every site; when the tunnel is down, the matrix shows it rather than hiding it.
- Firewall-caused failures appear as "path ends at hop N" in NET-2; finding the exact rule stays a router task.
