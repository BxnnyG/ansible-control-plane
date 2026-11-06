package policy

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// ErrNoInventory is returned by a GraphSource when the inventory does not exist
// in the workspace. The resolver treats it as "nothing in scope".
var ErrNoInventory = errors.New("policy: inventory not found")

// Graph is the structure of one inventory as far as scope resolution needs it:
// groups and hosts (id to name), the group parent/child links and the
// direct host memberships.
type Graph struct {
	Groups map[uuid.UUID]string
	Hosts  map[uuid.UUID]string
	// HostAddrs holds the ansible_host variable of hosts that set one; it
	// links inventory hosts to fleet hosts for network scopes.
	HostAddrs map[uuid.UUID]string
	Links     []GraphLink
	Members   []GraphMember
}

// GraphLink is a parent to child edge between two groups. A group may have
// several parents.
type GraphLink struct{ Parent, Child uuid.UUID }

// GraphMember is a direct host membership of a group.
type GraphMember struct{ Group, Host uuid.UUID }

// GraphSource loads the Graph of an inventory; *inventory.Service implements it.
type GraphSource interface {
	InventoryGraph(ctx context.Context, workspaceID, inventoryID uuid.UUID) (Graph, error)
}

// Access is the part of one inventory that a membership may use. All means the
// whole inventory (inventory scope). Otherwise Groups holds the scoped groups
// and all their descendants, Hosts the hosts that are members of any of them.
// The zero value grants nothing.
type Access struct {
	All       bool
	Groups    map[uuid.UUID]bool
	Hosts     map[uuid.UUID]bool
	hostNames map[string]bool
}

// None reports whether nothing of the inventory is accessible.
func (a Access) None() bool { return !a.All && len(a.Groups) == 0 && len(a.Hosts) == 0 }

// Group reports whether the group is accessible.
func (a Access) Group(id uuid.UUID) bool { return a.All || a.Groups[id] }

// Host reports whether the host is accessible.
func (a Access) Host(id uuid.UUID) bool { return a.All || a.Hosts[id] }

// HostName reports whether the named host is accessible. With All every name
// passes (no graph was loaded).
func (a Access) HostName(name string) bool { return a.All || a.hostNames[name] }

// AllHostsIn reports whether every explicit host name is accessible. An empty
// list fails closed.
func (a Access) AllHostsIn(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if !a.HostName(n) {
			return false
		}
	}
	return true
}

// AllGroupsIn reports whether every group id is accessible.
func (a Access) AllGroupsIn(ids []uuid.UUID) bool {
	for _, id := range ids {
		if !a.Group(id) {
			return false
		}
	}
	return true
}

// hasGroupScopes reports whether any inventory_group scope exists.
func hasGroupScopes(scopes []Scope) bool {
	for _, s := range scopes {
		if s.Kind == KindInventoryGroup {
			return true
		}
	}
	return false
}

func hasInventoryScope(scopes []Scope, inv uuid.UUID) bool {
	for _, s := range scopes {
		if s.Kind == KindInventory && s.RefID == inv.String() {
			return true
		}
	}
	return false
}

// ComputeAccess derives the Access of the scopes within one inventory. Group
// scopes include every descendant group (links form a multi-parent graph and
// may contain cycles) and their hosts. Network scopes are not resolved here
// (see ComputeAccessWithNetworks). Scopes naming groups of other inventories
// are ignored.
func ComputeAccess(scopes []Scope, inv uuid.UUID, g Graph) Access {
	return ComputeAccessWithNetworks(scopes, inv, g, nil)
}

// ComputeAccessWithNetworks is ComputeAccess plus the inventory hosts linked
// to the fleet hosts of the scoped networks (ADR-0018): hosts whose name or
// ansible_host equals a name or address of such a fleet host. They are
// granted as single hosts, without any group.
func ComputeAccessWithNetworks(scopes []Scope, inv uuid.UUID, g Graph, networkHosts []FleetHost) Access {
	a := computeGroupAccess(scopes, inv, g)
	if !a.All {
		a.addNetworkHosts(g, newFleetKeys(networkHosts))
	}
	return a
}

func computeGroupAccess(scopes []Scope, inv uuid.UUID, g Graph) Access {
	if hasInventoryScope(scopes, inv) {
		return Access{All: true}
	}
	children := map[uuid.UUID][]uuid.UUID{}
	for _, l := range g.Links {
		children[l.Parent] = append(children[l.Parent], l.Child)
	}
	a := Access{Groups: map[uuid.UUID]bool{}, Hosts: map[uuid.UUID]bool{}, hostNames: map[string]bool{}}
	var stack []uuid.UUID
	for _, s := range scopes {
		if s.Kind != KindInventoryGroup {
			continue
		}
		id, err := uuid.Parse(s.RefID)
		if err != nil {
			continue
		}
		if _, ok := g.Groups[id]; ok {
			stack = append(stack, id)
		}
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if a.Groups[cur] {
			continue
		}
		a.Groups[cur] = true
		stack = append(stack, children[cur]...)
	}
	for _, m := range g.Members {
		if !a.Groups[m.Group] {
			continue
		}
		if name, ok := g.Hosts[m.Host]; ok {
			a.Hosts[m.Host] = true
			a.hostNames[name] = true
		}
	}
	return a
}

// InventoryResolver is the inventory-backed ScopeResolver (ADR-0007). Targets
// that name groups or hosts must carry Target.InventoryID (or exactly one
// inventory ref): group ids and host names are only meaningful inside one
// inventory.
//
// Networks is optional; without it network scopes grant nothing.
type InventoryResolver struct {
	Source   GraphSource
	Networks NetworkSource
}

// AccessResolver is implemented by resolvers that can expand scopes into the
// accessible part of an inventory.
type AccessResolver interface {
	Access(ctx context.Context, workspaceID string, scopes []Scope, inv uuid.UUID) (Access, error)
}

// Access implements AccessResolver. The graph is only loaded when group or
// network scopes exist and no inventory scope already grants everything.
func (r InventoryResolver) Access(ctx context.Context, workspaceID string, scopes []Scope, inv uuid.UUID) (Access, error) {
	if hasInventoryScope(scopes, inv) {
		return Access{All: true}, nil
	}
	_, netHosts, err := r.scopedNetworks(ctx, workspaceID, scopes)
	if err != nil {
		return Access{}, err
	}
	if !hasGroupScopes(scopes) && len(netHosts) == 0 {
		return Access{}, nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return Access{}, fmt.Errorf("workspace id: %w", err)
	}
	g, err := r.Source.InventoryGraph(ctx, ws, inv)
	if errors.Is(err, ErrNoInventory) {
		return Access{}, nil
	}
	if err != nil {
		return Access{}, fmt.Errorf("load inventory graph: %w", err)
	}
	return ComputeAccessWithNetworks(scopes, inv, g, netHosts), nil
}

// InScope implements ScopeResolver: the whole target must be accessible.
// Hosts are explicit names (no patterns); names unknown to the inventory are
// outside every group scope. An empty target is denied.
func (r InventoryResolver) InScope(ctx context.Context, workspaceID string, scopes []Scope, t Target) (bool, error) {
	if len(t.Refs) == 0 && len(t.Hosts) == 0 {
		return false, nil
	}
	cache := map[uuid.UUID]Access{}
	access := func(inv uuid.UUID) (Access, error) {
		if a, ok := cache[inv]; ok {
			return a, nil
		}
		a, err := r.Access(ctx, workspaceID, scopes, inv)
		cache[inv] = a
		return a, err
	}
	invID := uuid.Nil
	if t.InventoryID != "" {
		id, err := uuid.Parse(t.InventoryID)
		if err != nil {
			return false, nil
		}
		invID = id
	} else {
		for _, ref := range t.Refs {
			if ref.Kind != KindInventory {
				continue
			}
			id, err := uuid.Parse(ref.ID)
			if err != nil || (invID != uuid.Nil && invID != id) {
				invID = uuid.Nil
				break
			}
			invID = id
		}
	}
	for _, ref := range t.Refs {
		id, err := uuid.Parse(ref.ID)
		if err != nil {
			return false, nil
		}
		switch ref.Kind {
		case KindInventory:
			a, err := access(id)
			if err != nil {
				return false, err
			}
			if !a.All {
				return false, nil
			}
		case KindInventoryGroup:
			if invID == uuid.Nil {
				return false, nil
			}
			a, err := access(invID)
			if err != nil {
				return false, err
			}
			if !a.Group(id) {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	if len(t.Hosts) > 0 {
		if invID == uuid.Nil {
			return false, nil
		}
		a, err := access(invID)
		if err != nil {
			return false, err
		}
		if !a.AllHostsIn(t.Hosts) {
			return false, nil
		}
	}
	return true, nil
}

// SortedIDs returns the ids of a set in a stable order.
func SortedIDs(set map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
