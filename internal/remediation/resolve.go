package remediation

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// Net is a network of the workspace.
type Net struct {
	ID     uuid.UUID
	Prefix netip.Prefix
}

// linkedNames returns the names of the inventory hosts of inv that are linked
// to the fleet host.
func linkedNames(h fleet.HostView, inv uuid.UUID) []string {
	var out []string
	for _, l := range h.Ansible {
		if l.InventoryID == inv && !slices.Contains(out, l.Host) {
			out = append(out, l.Host)
		}
	}
	sort.Strings(out)
	return out
}

// NetworkOf picks the most specific network that contains the host's primary
// address, falling back to any of its other addresses.
func NetworkOf(nets []Net, ips []string) (Net, bool) {
	ordered := []string{}
	if p := fleet.PrimaryAddress(ips); p != "" {
		ordered = append(ordered, p)
	}
	ordered = append(ordered, ips...)
	for _, s := range ordered {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		a = a.Unmap()
		best, found := Net{}, false
		for _, n := range nets {
			if n.Prefix.Contains(a) && (!found || n.Prefix.Bits() > best.Prefix.Bits()) {
				best, found = n, true
			}
		}
		if found {
			return best, true
		}
	}
	return Net{}, false
}

func inNet(ips []string, p netip.Prefix) bool {
	for _, s := range ips {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil && p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// JoinLimit builds an Ansible limit from explicit host names.
func JoinLimit(hosts []string) string { return strings.Join(hosts, ",") }

// Resolve computes the limit of a rule for the alert's host: the inventory
// hosts of the template's inventory inv. all are the fleet hosts of the
// workspace (with their Ansible links); graph is only consulted for group
// targets. It returns the sorted host names; the limit is JoinLimit of them,
// or the group name for group targets (limit).
func Resolve(target, group string, inv uuid.UUID, host fleet.HostView, all []fleet.HostView, nets []Net, graph func() (policy.Graph, error)) (limit string, hosts []string, err error) {
	own := linkedNames(host, inv)
	if len(own) == 0 {
		return "", nil, fmt.Errorf("%w: host %q is not linked to a host of the template's inventory", ErrInvalid, host.Name)
	}
	switch target {
	case TargetHost:
		return JoinLimit(own), own, nil
	case TargetGroup:
		g, err := graph()
		if err != nil {
			return "", nil, err
		}
		members, err := runs.ResolveLimit(g, group)
		if err != nil {
			return "", nil, err
		}
		in := []string{}
		for _, n := range own {
			if slices.Contains(members, n) {
				in = append(in, n)
			}
		}
		if len(in) == 0 {
			return "", nil, fmt.Errorf("%w: group %q of the template's inventory does not contain host %q", ErrInvalid, group, host.Name)
		}
		return group, members, nil
	case TargetNetwork:
		n, ok := NetworkOf(nets, host.IPs)
		if !ok {
			return "", nil, fmt.Errorf("%w: host %q is in no known network", ErrInvalid, host.Name)
		}
		set := map[string]bool{}
		for _, h := range all {
			if inNet(h.IPs, n.Prefix) {
				for _, name := range linkedNames(h, inv) {
					set[name] = true
				}
			}
		}
		hosts := make([]string, 0, len(set))
		for name := range set {
			hosts = append(hosts, name)
		}
		sort.Strings(hosts)
		return JoinLimit(hosts), hosts, nil
	}
	return "", nil, fmt.Errorf("%w: unknown target %q", ErrInvalid, target)
}
