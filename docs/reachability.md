# Reachability matrix (NET-1)

The reachability matrix answers "can host A reach target B?" for a set of source hosts and targets, on demand or on a schedule. Design: [ADR-0022](adr/0022-network-diagnostics.md). UI: **Reachability** in the sidebar (`/network/reachability`).

## How it works

ACP runs a bundled probe playbook (shipped in the worker image, not in a Git project) on the source hosts via SSH. Each source runs the checks against all enabled targets and reports the results through the callback plugin. ACP stores the latest result per (source, target). A probe run appears in the run list as "Reachability probe" and has no template.

## Setup

1. **Sources:** pick an inventory and a host in it. Sources are inventory hosts, typically one per site. All sources must belong to the same inventory.
2. **Targets:** name, address and a check type:
   - `icmp`: ping the address.
   - `tcp`: connect to address and port (1-65535).
   - `dns`: resolve a name on the source host. The address is informational only; the name to resolve is a separate field.
3. **Interval:** 0 turns the scheduler off (default), otherwise 60-86400 seconds. "Run now" starts a probe run independent of the interval.
4. **Credentials:** optionally select an SSH secret that the probe run uses to log in to the sources. Without one, the inventory variables of the hosts apply.

Permissions: `reachability.read` (view), `reachability.write` (config, sources, targets), `reachability.run` (Run now).

## Statuses

- **Reachable** (green, check mark): the check succeeded; ICMP shows the latency.
- **Failed** (red, cross): the source ran the check and it failed (detail shows the reason).
- **Probe host unreachable** (grey, question mark): the worker could not reach the source host over SSH, so nothing is known about its targets. This is deliberately different from "failed" so a dead tunnel is not read as many dead targets.
- **Empty cell:** never checked.
- **Stale:** a result older than twice the interval is shown dashed with a "stale" marker. Never shown when the scheduler is off.

Click a cell for latency, detail, check time and a link to the run.

## Limitations

- TCP latency is not reported (`wait_for` only returns whole seconds); the cell shows no latency.
- The DNS target address is informational; only the name to resolve is checked.
- Only the latest result per pair is kept; there is no history.
- Vaulted inventories: probe runs have no vault identities. Inventories that need vault passwords cannot be used as probe inventory until vault ids can be assigned to the probe configuration.
- One probe run per workspace at a time; a run that is still queued or running is not doubled by the scheduler.
- Checks need only a normal SSH user; no privileges are required on the sources.
