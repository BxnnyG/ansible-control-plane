package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// FleetHost is the identity of one merged fleet host as far as network scope
// resolution needs it: the names and addresses it is known by.
type FleetHost struct {
	ID    uuid.UUID
	Names []string
	IPs   []string
}

// NetworkSource provides the connector-synced network data used to resolve
// `network` scopes (ADR-0018). DBNetworkSource implements it.
type NetworkSource interface {
	// NetworkPrefixes returns the CIDRs of the given networks; unknown ids
	// are skipped.
	NetworkPrefixes(ctx context.Context, workspaceID uuid.UUID, ids []uuid.UUID) ([]netip.Prefix, error)
	// FleetHosts returns every merged host of the workspace.
	FleetHosts(ctx context.Context, workspaceID uuid.UUID) ([]FleetHost, error)
}

// FleetAccess is the part of the fleet a membership may see. All means the
// whole fleet (unscoped membership). Otherwise a host is visible when one of
// its addresses lies in a scoped network. The zero value grants nothing.
type FleetAccess struct {
	All      bool
	prefixes []netip.Prefix
}

// None reports whether no fleet host is accessible.
func (a FleetAccess) None() bool { return !a.All && len(a.prefixes) == 0 }

// Host reports whether a host with the given merged addresses is accessible.
func (a FleetAccess) Host(ips []string) bool {
	if a.All {
		return true
	}
	return inPrefixes(a.prefixes, ips)
}

func inPrefixes(prefixes []netip.Prefix, ips []string) bool {
	for _, s := range ips {
		ip, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		for _, p := range prefixes {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// FleetResolver is implemented by resolvers that can expand network scopes
// into fleet access.
type FleetResolver interface {
	FleetAccess(ctx context.Context, workspaceID string, scopes []Scope) (FleetAccess, error)
}

func networkScopeIDs(scopes []Scope) []uuid.UUID {
	var ids []uuid.UUID
	for _, s := range scopes {
		if s.Kind != KindNetwork {
			continue
		}
		if id, err := uuid.Parse(s.RefID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// FleetAccess implements FleetResolver. Without a NetworkSource, or without
// resolvable network scopes, nothing is accessible.
func (r InventoryResolver) FleetAccess(ctx context.Context, workspaceID string, scopes []Scope) (FleetAccess, error) {
	pfx, _, err := r.scopedNetworks(ctx, workspaceID, scopes)
	if err != nil {
		return FleetAccess{}, err
	}
	return FleetAccess{prefixes: pfx}, nil
}

// scopedNetworks returns the prefixes of the network scopes and the fleet
// hosts lying inside them.
func (r InventoryResolver) scopedNetworks(ctx context.Context, workspaceID string, scopes []Scope) ([]netip.Prefix, []FleetHost, error) {
	ids := networkScopeIDs(scopes)
	if r.Networks == nil || len(ids) == 0 {
		return nil, nil, nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("workspace id: %w", err)
	}
	pfx, err := r.Networks.NetworkPrefixes(ctx, ws, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("load network scopes: %w", err)
	}
	if len(pfx) == 0 {
		return nil, nil, nil
	}
	all, err := r.Networks.FleetHosts(ctx, ws)
	if err != nil {
		return nil, nil, fmt.Errorf("load fleet hosts: %w", err)
	}
	var in []FleetHost
	for _, h := range all {
		if inPrefixes(pfx, h.IPs) {
			in = append(in, h)
		}
	}
	return pfx, in, nil
}

// fleetKeys are the lower-case names and addresses of fleet hosts that lie in
// a scoped network. An inventory host is linked to them when its name or its
// ansible_host equals one of the keys.
type fleetKeys map[string]bool

func newFleetKeys(hosts []FleetHost) fleetKeys {
	k := fleetKeys{}
	for _, h := range hosts {
		for _, n := range h.Names {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				k[n] = true
			}
		}
		for _, ip := range h.IPs {
			if a, err := netip.ParseAddr(strings.TrimSpace(ip)); err == nil {
				k[a.Unmap().String()] = true
			}
		}
	}
	return k
}

func (k fleetKeys) matches(name, addr string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if a, err := netip.ParseAddr(s); err == nil {
			return a.Unmap().String()
		}
		return s
	}
	if name != "" && k[norm(name)] {
		return true
	}
	return addr != "" && k[norm(addr)]
}

// addNetworkHosts grants the inventory hosts linked to fleet hosts of the
// scoped networks.
func (a *Access) addNetworkHosts(g Graph, keys fleetKeys) {
	if len(keys) == 0 {
		return
	}
	if a.Hosts == nil {
		a.Hosts = map[uuid.UUID]bool{}
	}
	if a.hostNames == nil {
		a.hostNames = map[string]bool{}
	}
	for id, name := range g.Hosts {
		if keys.matches(name, g.HostAddrs[id]) {
			a.Hosts[id] = true
			a.hostNames[name] = true
		}
	}
}

// DBNetworkSource implements NetworkSource on the database queries.
type DBNetworkSource struct{ Q NetworkQueries }

// NetworkQueries is the subset of dbgen.Queries used by DBNetworkSource.
type NetworkQueries interface {
	ListNetworks(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.Network, error)
	ListHosts(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.Host, error)
	ListHostKeys(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.HostKey, error)
}

// NetworkPrefixes implements NetworkSource.
func (s DBNetworkSource) NetworkPrefixes(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) ([]netip.Prefix, error) {
	want := map[uuid.UUID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	nets, err := s.Q.ListNetworks(ctx, ws)
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, n := range nets {
		if want[n.ID] {
			out = append(out, n.Cidr)
		}
	}
	return out, nil
}

// FleetHosts implements NetworkSource. Addresses come from the merged host
// fields, names from the host keys plus the display name.
func (s DBNetworkSource) FleetHosts(ctx context.Context, ws uuid.UUID) ([]FleetHost, error) {
	hosts, err := s.Q.ListHosts(ctx, ws)
	if err != nil {
		return nil, err
	}
	keys, err := s.Q.ListHostKeys(ctx, ws)
	if err != nil {
		return nil, err
	}
	names := map[uuid.UUID][]string{}
	for _, k := range keys {
		if k.Kind == "name" {
			names[k.HostID] = append(names[k.HostID], k.Value)
		}
	}
	out := make([]FleetHost, 0, len(hosts))
	for _, h := range hosts {
		var f struct {
			IPs      []string `json:"ips"`
			Hostname string   `json:"hostname"`
			FQDN     string   `json:"fqdn"`
		}
		if len(h.Fields) > 0 {
			if err := json.Unmarshal(h.Fields, &f); err != nil {
				return nil, fmt.Errorf("decode host %s: %w", h.ID, err)
			}
		}
		n := append(names[h.ID], h.Name, f.Hostname, f.FQDN)
		out = append(out, FleetHost{ID: h.ID, Names: n, IPs: f.IPs})
	}
	return out, nil
}
