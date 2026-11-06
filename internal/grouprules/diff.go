package grouprules

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
)

// Modes of a rule.
const (
	ModeAddOnly = "add-only"
	ModeSync    = "sync"
)

// AddItem is a host that joins the group.
type AddItem struct {
	Name        string    `json:"name"`
	AnsibleHost string    `json:"ansible_host,omitempty"` // only for hosts that are created
	CreateHost  bool      `json:"create_host"`            // the inventory has no such host yet
	FleetHostID uuid.UUID `json:"fleet_host_id"`
}

// Skipped is a matching fleet host that cannot be added.
type Skipped struct {
	FleetHostID uuid.UUID `json:"fleet_host_id"`
	Name        string    `json:"name"`
	Reason      string    `json:"reason"`
}

// Plan is the pure diff of a rule against the current inventory state.
type Plan struct {
	GroupCreate bool      `json:"group_create"` // the group does not exist yet and gets created
	Matched     int       `json:"matched"`      // fleet hosts matching the rule
	Add         []AddItem `json:"add"`
	Remove      []string  `json:"remove"`  // sync mode: hosts the rule added earlier that no longer match
	Keep        int       `json:"keep"`    // matching hosts that already are members
	Untrack     []string  `json:"untrack"` // stale tracking entries (no longer members)
	Skipped     []Skipped `json:"skipped"`
}

// DiffInput is everything Diff needs.
type DiffInput struct {
	Matcher Matcher
	Mode    string
	Hosts   []Host               // all fleet hosts
	State   inventory.GroupState // current group and inventory hosts
	Tracked []string             // memberships the rule added earlier
}

// Diff computes what applying the rule would change. It is pure: no input is
// modified and the result is deterministic.
func Diff(in DiffInput) Plan {
	p := Plan{Add: []AddItem{}, Remove: []string{}, Untrack: []string{}, Skipped: []Skipped{}}
	p.GroupCreate = !in.State.GroupExists

	member := map[string]bool{}
	for _, m := range in.State.Members {
		member[m] = true
	}
	invHosts := append([]inventory.HostRef(nil), in.State.Hosts...)
	sort.Slice(invHosts, func(i, j int) bool { return invHosts[i].Name < invHosts[j].Name })
	byName := map[string]bool{}
	byLower := map[string]string{} // lower-case name or normalized ansible_host to host name
	for _, h := range invHosts {
		byName[h.Name] = true
		if _, ok := byLower[strings.ToLower(h.Name)]; !ok {
			byLower[strings.ToLower(h.Name)] = h.Name
		}
	}
	for _, h := range invHosts {
		if k := normKey(h.AnsibleHost); k != "" {
			if _, ok := byLower[k]; !ok {
				byLower[k] = h.Name
			}
		}
	}

	hosts := append([]Host(nil), in.Hosts...)
	sort.SliceStable(hosts, func(i, j int) bool {
		a, _, _ := InventoryName(hosts[i])
		b, _, _ := InventoryName(hosts[j])
		if a != b {
			return a < b
		}
		return hosts[i].ID.String() < hosts[j].ID.String()
	})

	desired := map[string]bool{}
	for _, h := range hosts {
		if in.Matcher == nil || !in.Matcher.Match(h) {
			continue
		}
		p.Matched++
		name, addr, ok := InventoryName(h)
		if !ok {
			p.Skipped = append(p.Skipped, Skipped{FleetHostID: h.ID, Name: h.Name, Reason: "no name or address to build an inventory host from"})
			continue
		}
		create := true
		if byName[name] {
			create = false
		} else if existing := linkExisting(h, byLower); existing != "" {
			name, create = existing, false
		}
		if desired[name] {
			p.Skipped = append(p.Skipped, Skipped{FleetHostID: h.ID, Name: name, Reason: "another matching host maps to the same inventory host"})
			continue
		}
		desired[name] = true
		if member[name] {
			p.Keep++
			continue
		}
		it := AddItem{Name: name, CreateHost: create, FleetHostID: h.ID}
		if create {
			it.AnsibleHost = addr
		}
		p.Add = append(p.Add, it)
	}

	tracked := append([]string(nil), in.Tracked...)
	sort.Strings(tracked)
	for _, t := range tracked {
		switch {
		case !member[t]:
			p.Untrack = append(p.Untrack, t)
		case in.Mode == ModeSync && !desired[t]:
			p.Remove = append(p.Remove, t)
		}
	}
	return p
}

func normKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String()
	}
	return s
}

// linkExisting finds an inventory host that already represents the fleet
// host: same FQDN, hostname or display name (case-insensitive), or an
// ansible_host equal to one of its addresses.
func linkExisting(h Host, byLower map[string]string) string {
	for _, c := range []string{h.FQDN, h.Hostname, h.Name} {
		if k := normKey(c); k != "" {
			if n, ok := byLower[k]; ok {
				return n
			}
		}
	}
	for _, ip := range h.IPs {
		if k := normKey(ip); k != "" {
			if n, ok := byLower[k]; ok {
				return n
			}
		}
	}
	return ""
}
