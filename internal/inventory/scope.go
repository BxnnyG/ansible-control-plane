package inventory

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// InventoryGraph returns the groups, hosts, group links and memberships of an
// inventory for scope resolution (policy.GraphSource). An inventory that does
// not exist in the workspace yields policy.ErrNoInventory.
func (s *Service) InventoryGraph(ctx context.Context, ws, inv uuid.UUID) (policy.Graph, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		if errors.Is(err, ErrNotFound) {
			return policy.Graph{}, policy.ErrNoInventory
		}
		return policy.Graph{}, err
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return policy.Graph{}, err
	}
	g := policy.Graph{Groups: map[uuid.UUID]string{}, Hosts: map[uuid.UUID]string{}, HostAddrs: map[uuid.UUID]string{}}
	for _, x := range snap.groups {
		g.Groups[x.ID] = x.Name
	}
	for _, x := range snap.hosts {
		g.Hosts[x.ID] = x.Name
		if addr := ansibleHost(x.Vars); addr != "" {
			g.HostAddrs[x.ID] = addr
		}
	}
	for _, l := range snap.links {
		g.Links = append(g.Links, policy.GraphLink{Parent: l.ParentID, Child: l.ChildID})
	}
	for _, m := range snap.members {
		g.Members = append(g.Members, policy.GraphMember{Group: m.GroupID, Host: m.HostID})
	}
	return g, nil
}

// Visible limits an inventory to the groups and hosts a scoped membership may
// see (see policy.Access).
type Visible struct {
	Groups map[uuid.UUID]bool
	Hosts  map[uuid.UUID]bool
}

// restrict returns a copy of the snapshot reduced to the visible groups and
// hosts. The implicit "all" group is not part of any group scope, so it and
// its variables disappear; vars of groups outside the scope never reach the
// result.
func (s snapshot) restrict(v Visible) snapshot {
	var out snapshot
	for _, g := range s.groups {
		if v.Groups[g.ID] {
			out.groups = append(out.groups, g)
		}
	}
	for _, h := range s.hosts {
		if v.Hosts[h.ID] {
			out.hosts = append(out.hosts, h)
		}
	}
	for _, l := range s.links {
		if v.Groups[l.ParentID] && v.Groups[l.ChildID] {
			out.links = append(out.links, l)
		}
	}
	for _, m := range s.members {
		if v.Groups[m.GroupID] && v.Hosts[m.HostID] {
			out.members = append(out.members, m)
		}
	}
	return out
}

// RenderVisible renders the `ansible-inventory --list` preview of the visible
// part of an inventory. Host variables are merged from the visible groups
// only, so the preview of a scoped member can differ from what Ansible
// computes on the full inventory: variables of groups outside the scope
// (including "all") are never disclosed. Vault values are masked.
func (s *Service) RenderVisible(ctx context.Context, ws, id uuid.UUID, export bool, v Visible) (map[string]any, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, id)
	if err != nil {
		return nil, err
	}
	d, err := snap.restrict(v).doc()
	if err != nil {
		return nil, err
	}
	return Render(d, RenderOptions{Export: export, MaskVault: true}), nil
}

// ansibleHost returns the string value of the ansible_host variable, if any.
func ansibleHost(vars []byte) string {
	if len(vars) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(vars, &m) != nil {
		return ""
	}
	s, _ := m["ansible_host"].(string)
	return s
}
