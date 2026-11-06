package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// OnlineWindow is how recent a lease, ARP or NDP sighting must be for a host to
// count as online.
const OnlineWindow = 15 * time.Minute

// Online states (HostView.Online).
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// Run results (RunRef.Result).
const (
	ResultOK          = "ok"
	ResultChanged     = "changed"
	ResultFailed      = "failed"
	ResultUnreachable = "unreachable"
)

// Reachability values (HostView.Reachable).
const (
	ReachYes     = "yes"
	ReachNo      = "no"
	ReachUnknown = "unknown"
)

// OnlineOf derives the online state of a host at now. A recent lease/ARP/NDP
// sighting, or a running/active VM, LXC or device status, means online; any
// other live-state source means offline; without a live-state source the state
// is unknown.
func OnlineOf(f Fields, now time.Time) string {
	if f.NetSeen == nil && f.Power == "" {
		return StatusUnknown
	}
	if f.NetSeen != nil && now.Sub(*f.NetSeen) <= OnlineWindow {
		return StatusOnline
	}
	if f.Power == "running" || f.Power == "active" {
		return StatusOnline
	}
	return StatusOffline
}

// ResultOf classifies the per-host counters of one run.
func ResultOf(changed, failed, unreachable int) string {
	switch {
	case unreachable > 0:
		return ResultUnreachable
	case failed > 0:
		return ResultFailed
	case changed > 0:
		return ResultChanged
	}
	return ResultOK
}

// AnsibleLink is an inventory host linked to a fleet host.
type AnsibleLink struct {
	InventoryID uuid.UUID `json:"inventory_id"`
	Inventory   string    `json:"inventory"`
	HostID      uuid.UUID `json:"host_id"`
	Host        string    `json:"host"`
}

// RunRef is the newest finished run that touched a linked inventory host.
type RunRef struct {
	RunID      uuid.UUID `json:"run_id"`
	Template   string    `json:"template"`
	FinishedAt time.Time `json:"finished_at"`
	Result     string    `json:"result"`
	Check      bool      `json:"check"`
}

// Drift reports that the newest check-mode run would change the host.
type Drift struct {
	RunID      uuid.UUID `json:"run_id"`
	Template   string    `json:"template"`
	FinishedAt time.Time `json:"finished_at"`
	Changed    int       `json:"changed"`
}

type invHostKey struct {
	inv  uuid.UUID
	host string
}

// statusData is everything needed to derive the Ansible side of host status
// for the whole workspace, loaded in a fixed number of queries.
type statusData struct {
	links map[uuid.UUID][]AnsibleLink // fleet host -> linked inventory hosts
	last  map[invHostKey]dbgen.LatestHostRunResultsRow
	check map[invHostKey]dbgen.LatestHostCheckResultsRow
}

// loadStatus links the workspace's inventory hosts to the given fleet hosts
// (policy.FleetIndex, the same linking network scopes use) and loads the
// newest run results of those inventory hosts.
func (s *Store) loadStatus(ctx context.Context, ws uuid.UUID, hosts []dbgen.Host, keys []dbgen.HostKey) (*statusData, error) {
	fh, err := policy.FleetHostsOf(hosts, keys)
	if err != nil {
		return nil, fmt.Errorf("fleet identities: %w", err)
	}
	idx := policy.NewFleetIndex(fh)
	invs, err := s.q.ListInventories(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list inventories: %w", err)
	}
	invName := map[uuid.UUID]string{}
	for _, i := range invs {
		invName[i.ID] = i.Name
	}
	rows, err := s.q.ListWorkspaceInventoryHosts(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list inventory hosts: %w", err)
	}
	d := &statusData{links: map[uuid.UUID][]AnsibleLink{}}
	for _, r := range rows {
		for _, id := range idx.Match(r.Name, r.AnsibleHost) {
			d.links[id] = append(d.links[id], AnsibleLink{InventoryID: r.InventoryID, Inventory: invName[r.InventoryID], HostID: r.ID, Host: r.Name})
		}
	}
	last, err := s.q.LatestHostRunResults(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("latest run results: %w", err)
	}
	d.last = make(map[invHostKey]dbgen.LatestHostRunResultsRow, len(last))
	for _, r := range last {
		d.last[invHostKey{r.InventoryID, r.Host}] = r
	}
	chk, err := s.q.LatestHostCheckResults(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("latest check results: %w", err)
	}
	d.check = make(map[invHostKey]dbgen.LatestHostCheckResultsRow, len(chk))
	for _, r := range chk {
		d.check[invHostKey{r.InventoryID, r.Host}] = r
	}
	return d, nil
}

// apply fills the status fields of a host view.
func (d *statusData) apply(v *HostView, f Fields, now time.Time) {
	v.Online = OnlineOf(f, now)
	v.Ansible = []AnsibleLink{}
	v.Reachable = ReachUnknown
	if d == nil {
		return
	}
	if l := d.links[v.ID]; len(l) > 0 {
		v.Ansible = l
	}
	v.Managed = len(v.Ansible) > 0
	for _, l := range v.Ansible {
		k := invHostKey{l.InventoryID, l.Host}
		if r, ok := d.last[k]; ok && r.FinishedAt.Valid && (v.LastRun == nil || r.FinishedAt.Time.After(v.LastRun.FinishedAt)) {
			v.LastRun = &RunRef{RunID: r.RunID, Template: r.TemplateName, FinishedAt: r.FinishedAt.Time.UTC(),
				Result: ResultOf(int(r.Changed), int(r.Failed), int(r.Unreachable)), Check: r.CheckMode}
		}
		if r, ok := d.check[k]; ok && r.FinishedAt.Valid && (v.checkAt.IsZero() || r.FinishedAt.Time.After(v.checkAt)) {
			v.checkAt = r.FinishedAt.Time
			v.Drift = nil
			if r.Changed > 0 {
				v.Drift = &Drift{RunID: r.RunID, Template: r.TemplateName, FinishedAt: r.FinishedAt.Time.UTC(), Changed: int(r.Changed)}
			}
		}
	}
	if v.LastRun != nil {
		v.Reachable = ReachYes
		if v.LastRun.Result == ResultUnreachable {
			v.Reachable = ReachNo
		}
	}
}

// NetworkCounts are the per-network status counters (FLEET-1).
type NetworkCounts struct {
	Online       int `json:"online"`
	AnsibleOK    int `json:"ansible_ok"`
	Failed       int `json:"failed"`
	NeverManaged int `json:"never_managed"`
	OpenAlerts   int `json:"open_alerts"`
}

// count adds one host to the counters. ansible_ok: the last run did not fail
// or hit an unreachable host; failed: it did; never_managed: no linked
// inventory host, or none of them ever ran.
func (c *NetworkCounts) count(v HostView) {
	if v.Online == StatusOnline {
		c.Online++
	}
	c.OpenAlerts += v.OpenAlerts
	switch {
	case v.LastRun == nil:
		c.NeverManaged++
	case v.LastRun.Result == ResultFailed || v.LastRun.Result == ResultUnreachable:
		c.Failed++
	default:
		c.AnsibleOK++
	}
}
