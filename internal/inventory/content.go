package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// GroupView is the API view of an inventory group. ParentIDs are the groups it is
// a child of (Ansible allows several). Vars are YAML; vault values keep their
// encrypted `!vault` form.
type GroupView struct {
	ID        uuid.UUID   `json:"id"`
	Name      string      `json:"name"`
	ParentIDs []uuid.UUID `json:"parent_ids"`
	VarsYAML  string      `json:"vars_yaml"`
}

// HostView is the API view of an inventory host with its direct group memberships.
type HostView struct {
	ID       uuid.UUID   `json:"id"`
	Name     string      `json:"name"`
	GroupIDs []uuid.UUID `json:"group_ids"`
	VarsYAML string      `json:"vars_yaml"`
}

// GroupInput creates a group.
type GroupInput struct {
	Name      string
	ParentIDs []uuid.UUID
	VarsYAML  string
}

// GroupUpdate edits a group; nil fields stay unchanged.
type GroupUpdate struct {
	Name      *string
	ParentIDs *[]uuid.UUID
	VarsYAML  *string
}

// HostInput creates a host.
type HostInput struct {
	Name     string
	GroupIDs []uuid.UUID
	VarsYAML string
}

// HostUpdate edits a host; nil fields stay unchanged.
type HostUpdate struct {
	Name     *string
	GroupIDs *[]uuid.UUID
	VarsYAML *string
}

// snapshot is the complete content of one inventory.
type snapshot struct {
	groups  []dbgen.InventoryGroup
	hosts   []dbgen.InventoryHost
	links   []dbgen.InventoryGroupLink
	members []dbgen.InventoryGroupHost
}

func loadSnapshot(ctx context.Context, q *dbgen.Queries, inv uuid.UUID) (snapshot, error) {
	var s snapshot
	var err error
	if s.groups, err = q.ListInventoryGroups(ctx, inv); err != nil {
		return s, fmt.Errorf("list groups: %w", err)
	}
	if s.hosts, err = q.ListInventoryHosts(ctx, inv); err != nil {
		return s, fmt.Errorf("list hosts: %w", err)
	}
	if s.links, err = q.ListInventoryGroupLinks(ctx, inv); err != nil {
		return s, fmt.Errorf("list group links: %w", err)
	}
	if s.members, err = q.ListInventoryMemberships(ctx, inv); err != nil {
		return s, fmt.Errorf("list memberships: %w", err)
	}
	return s, nil
}

func decodeVars(raw []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode stored vars: %w", err)
	}
	return m, nil
}

// doc converts the snapshot into the in-memory model.
func (s snapshot) doc() (*Doc, error) {
	d := &Doc{}
	gname := map[uuid.UUID]string{}
	byName := map[string]*Group{}
	for _, g := range s.groups {
		vars, err := decodeVars(g.Vars)
		if err != nil {
			return nil, err
		}
		ng := &Group{Name: g.Name, Vars: vars}
		d.Groups = append(d.Groups, ng)
		gname[g.ID] = g.Name
		byName[g.Name] = ng
	}
	hname := map[uuid.UUID]string{}
	for _, h := range s.hosts {
		vars, err := decodeVars(h.Vars)
		if err != nil {
			return nil, err
		}
		d.Hosts = append(d.Hosts, &Host{Name: h.Name, Vars: vars})
		hname[h.ID] = h.Name
	}
	for _, l := range s.links {
		if p := byName[gname[l.ParentID]]; p != nil {
			p.Children = append(p.Children, gname[l.ChildID])
		}
	}
	for _, m := range s.members {
		if g := byName[gname[m.GroupID]]; g != nil {
			g.Hosts = append(g.Hosts, hname[m.HostID])
		}
	}
	for _, g := range d.Groups {
		sort.Strings(g.Children)
		sort.Strings(g.Hosts)
	}
	sort.Slice(d.Groups, func(i, j int) bool { return d.Groups[i].Name < d.Groups[j].Name })
	sort.Slice(d.Hosts, func(i, j int) bool { return d.Hosts[i].Name < d.Hosts[j].Name })
	return d, nil
}

// LoadDoc returns the stored content of an inventory as a Doc (vault values
// stay opaque and unmasked; callers must not expose it unmasked).
func (s *Service) LoadDoc(ctx context.Context, ws, id uuid.UUID) (*Doc, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, id)
	if err != nil {
		return nil, err
	}
	return snap.doc()
}

// Render returns the `ansible-inventory --list` preview of an inventory.
// Vault values are always masked.
func (s *Service) Render(ctx context.Context, ws, id uuid.UUID, export bool) (map[string]any, error) {
	d, err := s.LoadDoc(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	return Render(d, RenderOptions{Export: export, MaskVault: true}), nil
}

// parseVarsInput validates a YAML vars field and returns its stored JSON.
func parseVarsInput(text string) ([]byte, error) {
	if len(text) > maxVarsSize {
		return nil, invalid("variables are larger than %d KiB", maxVarsSize>>10)
	}
	m, err := ParseVarsYAML(text)
	if err != nil {
		return nil, err
	}
	return varsJSON(m)
}

func varsToYAML(raw []byte) (string, error) {
	m, err := decodeVars(raw)
	if err != nil {
		return "", err
	}
	return MarshalVarsYAML(m)
}

func (s snapshot) groupView(g dbgen.InventoryGroup) (GroupView, error) {
	y, err := varsToYAML(g.Vars)
	if err != nil {
		return GroupView{}, err
	}
	out := GroupView{ID: g.ID, Name: g.Name, VarsYAML: y, ParentIDs: []uuid.UUID{}}
	for _, l := range s.links {
		if l.ChildID == g.ID {
			out.ParentIDs = append(out.ParentIDs, l.ParentID)
		}
	}
	sortIDs(out.ParentIDs)
	return out, nil
}

func (s snapshot) hostView(h dbgen.InventoryHost) (HostView, error) {
	y, err := varsToYAML(h.Vars)
	if err != nil {
		return HostView{}, err
	}
	out := HostView{ID: h.ID, Name: h.Name, VarsYAML: y, GroupIDs: []uuid.UUID{}}
	for _, m := range s.members {
		if m.HostID == h.ID {
			out.GroupIDs = append(out.GroupIDs, m.GroupID)
		}
	}
	sortIDs(out.GroupIDs)
	return out, nil
}

func sortIDs(ids []uuid.UUID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
}

// ListGroups returns all groups of an inventory, ordered by name.
func (s *Service) ListGroups(ctx context.Context, ws, inv uuid.UUID) ([]GroupView, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return nil, err
	}
	out := make([]GroupView, 0, len(snap.groups))
	for _, g := range snap.groups {
		v, err := snap.groupView(g)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetGroup returns one group.
func (s *Service) GetGroup(ctx context.Context, ws, inv, id uuid.UUID) (GroupView, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return GroupView{}, err
	}
	row, err := s.q.GetInventoryGroup(ctx, dbgen.GetInventoryGroupParams{ID: id, InventoryID: inv})
	if err != nil {
		return GroupView{}, mapErr(err)
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return GroupView{}, err
	}
	return snap.groupView(row)
}

func cleanGroupName(name string) error {
	if name == GroupUngrouped {
		return invalid("%q is a reserved group name", GroupUngrouped)
	}
	if !ValidGroupName(name) {
		return invalid("group name must match %s", groupNameRe)
	}
	return nil
}

// checkParents validates a parent set for group id (uuid.Nil for a new group)
// against the current links: parents must exist, must not be "all" and must
// not create a cycle.
func (s snapshot) checkParents(id uuid.UUID, name string, parents []uuid.UUID) error {
	if name == GroupAll && len(parents) > 0 {
		return invalid("group %q cannot have parents", GroupAll)
	}
	byID := map[uuid.UUID]dbgen.InventoryGroup{}
	for _, g := range s.groups {
		byID[g.ID] = g
	}
	children := map[uuid.UUID][]uuid.UUID{}
	for _, l := range s.links {
		children[l.ParentID] = append(children[l.ParentID], l.ChildID)
	}
	// descendants of id: a parent among them would close a cycle.
	desc := map[uuid.UUID]bool{id: true}
	var walk func(uuid.UUID)
	walk = func(g uuid.UUID) {
		for _, c := range children[g] {
			if !desc[c] {
				desc[c] = true
				walk(c)
			}
		}
	}
	if id != uuid.Nil {
		walk(id)
	}
	for _, p := range parents {
		pg, ok := byID[p]
		switch {
		case !ok:
			return invalid("parent group %s does not exist in this inventory", p)
		case pg.Name == GroupAll:
			return invalid("%q is implicit; leave the group without a parent instead", GroupAll)
		case desc[p]:
			return invalid("parent %q would create a cycle", pg.Name)
		}
	}
	return nil
}

func setParents(ctx context.Context, q *dbgen.Queries, id uuid.UUID, parents []uuid.UUID) error {
	if err := q.DeleteInventoryGroupParents(ctx, id); err != nil {
		return fmt.Errorf("clear parents: %w", err)
	}
	for _, p := range parents {
		if err := q.AddInventoryGroupLink(ctx, dbgen.AddInventoryGroupLinkParams{ParentID: p, ChildID: id}); err != nil {
			return fmt.Errorf("link parent: %w", mapErr(err))
		}
	}
	return nil
}

// CreateGroup adds a group. It returns the group and the new inventory version.
func (s *Service) CreateGroup(ctx context.Context, ws, inv uuid.UUID, in GroupInput, ifMatch *int) (GroupView, int, error) {
	if err := cleanGroupName(in.Name); err != nil {
		return GroupView{}, 0, err
	}
	vars, err := parseVarsInput(in.VarsYAML)
	if err != nil {
		return GroupView{}, 0, err
	}
	var out GroupView
	ver, err := s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		snap, err := loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		if len(snap.groups) >= MaxGroups {
			return invalid("more than %d groups", MaxGroups)
		}
		if err := snap.checkParents(uuid.Nil, in.Name, in.ParentIDs); err != nil {
			return err
		}
		row, err := q.CreateInventoryGroup(ctx, dbgen.CreateInventoryGroupParams{InventoryID: inv, Name: in.Name, Vars: vars})
		if err != nil {
			return fmt.Errorf("create group: %w", mapErr(err))
		}
		if err := setParents(ctx, q, row.ID, in.ParentIDs); err != nil {
			return err
		}
		out = GroupView{ID: row.ID, Name: row.Name, ParentIDs: uniqueIDs(in.ParentIDs)}
		out.VarsYAML, err = varsToYAML(row.Vars)
		return err
	})
	return out, ver, err
}

func uniqueIDs(in []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, id := range in {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sortIDs(out)
	return out
}

// UpdateGroup edits a group.
func (s *Service) UpdateGroup(ctx context.Context, ws, inv, id uuid.UUID, in GroupUpdate, ifMatch *int) (GroupView, int, error) {
	if in.Name != nil {
		if err := cleanGroupName(*in.Name); err != nil {
			return GroupView{}, 0, err
		}
	}
	var vars []byte
	if in.VarsYAML != nil {
		var err error
		if vars, err = parseVarsInput(*in.VarsYAML); err != nil {
			return GroupView{}, 0, err
		}
	}
	var out GroupView
	ver, err := s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		cur, err := q.GetInventoryGroup(ctx, dbgen.GetInventoryGroupParams{ID: id, InventoryID: inv})
		if err != nil {
			return mapErr(err)
		}
		name, newVars := cur.Name, cur.Vars
		if in.Name != nil {
			name = *in.Name
		}
		if cur.Name == GroupAll && name != GroupAll {
			return invalid("group %q cannot be renamed", GroupAll)
		}
		if vars != nil {
			newVars = vars
		}
		snap, err := loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		if in.ParentIDs != nil || name == GroupAll {
			var parents []uuid.UUID
			if in.ParentIDs != nil {
				parents = *in.ParentIDs
			}
			if err := snap.checkParents(id, name, parents); err != nil {
				return err
			}
			if in.ParentIDs != nil {
				if err := setParents(ctx, q, id, parents); err != nil {
					return err
				}
			}
		}
		if name == GroupAll {
			for _, m := range snap.members {
				if m.GroupID == id {
					return invalid("group %q cannot have hosts", GroupAll)
				}
			}
		}
		row, err := q.UpdateInventoryGroup(ctx, dbgen.UpdateInventoryGroupParams{ID: id, InventoryID: inv, Name: name, Vars: newVars})
		if err != nil {
			return fmt.Errorf("update group: %w", mapErr(err))
		}
		snap, err = loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		out, err = snap.groupView(row)
		return err
	})
	return out, ver, err
}

// DeleteGroup removes a group; its child groups become independent and hosts
// merely lose the membership.
func (s *Service) DeleteGroup(ctx context.Context, ws, inv, id uuid.UUID, ifMatch *int) (int, error) {
	return s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		n, err := q.DeleteInventoryGroup(ctx, dbgen.DeleteInventoryGroupParams{ID: id, InventoryID: inv})
		if err != nil {
			return fmt.Errorf("delete group: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ListHosts returns all hosts of an inventory, ordered by name.
func (s *Service) ListHosts(ctx context.Context, ws, inv uuid.UUID) ([]HostView, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return nil, err
	}
	out := make([]HostView, 0, len(snap.hosts))
	for _, h := range snap.hosts {
		v, err := snap.hostView(h)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// GetHost returns one host.
func (s *Service) GetHost(ctx context.Context, ws, inv, id uuid.UUID) (HostView, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return HostView{}, err
	}
	row, err := s.q.GetInventoryHost(ctx, dbgen.GetInventoryHostParams{ID: id, InventoryID: inv})
	if err != nil {
		return HostView{}, mapErr(err)
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return HostView{}, err
	}
	return snap.hostView(row)
}

func cleanHostName(name string) error {
	if !ValidHostName(name) {
		return invalid("host name must be 1-255 characters without whitespace")
	}
	return nil
}

// checkGroups validates that every id is an existing group that can hold hosts.
func (s snapshot) checkGroups(ids []uuid.UUID) error {
	byID := map[uuid.UUID]string{}
	for _, g := range s.groups {
		byID[g.ID] = g.Name
	}
	for _, id := range ids {
		n, ok := byID[id]
		if !ok {
			return invalid("group %s does not exist in this inventory", id)
		}
		if n == GroupAll {
			return invalid("hosts are in %q implicitly", GroupAll)
		}
	}
	return nil
}

func setMemberships(ctx context.Context, q *dbgen.Queries, snap snapshot, host uuid.UUID, groups []uuid.UUID) error {
	want := map[uuid.UUID]bool{}
	for _, g := range groups {
		want[g] = true
	}
	have := map[uuid.UUID]bool{}
	for _, m := range snap.members {
		if m.HostID == host {
			have[m.GroupID] = true
			if !want[m.GroupID] {
				if _, err := q.DeleteInventoryMembership(ctx, dbgen.DeleteInventoryMembershipParams{GroupID: m.GroupID, HostID: host}); err != nil {
					return fmt.Errorf("remove membership: %w", err)
				}
			}
		}
	}
	for g := range want {
		if !have[g] {
			if err := q.AddInventoryMembership(ctx, dbgen.AddInventoryMembershipParams{GroupID: g, HostID: host}); err != nil {
				return fmt.Errorf("add membership: %w", mapErr(err))
			}
		}
	}
	return nil
}

// CreateHost adds a host.
func (s *Service) CreateHost(ctx context.Context, ws, inv uuid.UUID, in HostInput, ifMatch *int) (HostView, int, error) {
	if err := cleanHostName(in.Name); err != nil {
		return HostView{}, 0, err
	}
	vars, err := parseVarsInput(in.VarsYAML)
	if err != nil {
		return HostView{}, 0, err
	}
	var out HostView
	ver, err := s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		snap, err := loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		if len(snap.hosts) >= MaxHosts {
			return invalid("more than %d hosts", MaxHosts)
		}
		if err := snap.checkGroups(in.GroupIDs); err != nil {
			return err
		}
		row, err := q.CreateInventoryHost(ctx, dbgen.CreateInventoryHostParams{InventoryID: inv, Name: in.Name, Vars: vars})
		if err != nil {
			return fmt.Errorf("create host: %w", mapErr(err))
		}
		if err := setMemberships(ctx, q, snap, row.ID, in.GroupIDs); err != nil {
			return err
		}
		out = HostView{ID: row.ID, Name: row.Name, GroupIDs: uniqueIDs(in.GroupIDs)}
		out.VarsYAML, err = varsToYAML(row.Vars)
		return err
	})
	return out, ver, err
}

// UpdateHost edits a host.
func (s *Service) UpdateHost(ctx context.Context, ws, inv, id uuid.UUID, in HostUpdate, ifMatch *int) (HostView, int, error) {
	if in.Name != nil {
		if err := cleanHostName(*in.Name); err != nil {
			return HostView{}, 0, err
		}
	}
	var vars []byte
	if in.VarsYAML != nil {
		var err error
		if vars, err = parseVarsInput(*in.VarsYAML); err != nil {
			return HostView{}, 0, err
		}
	}
	var out HostView
	ver, err := s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		cur, err := q.GetInventoryHost(ctx, dbgen.GetInventoryHostParams{ID: id, InventoryID: inv})
		if err != nil {
			return mapErr(err)
		}
		name, newVars := cur.Name, cur.Vars
		if in.Name != nil {
			name = *in.Name
		}
		if vars != nil {
			newVars = vars
		}
		snap, err := loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		if in.GroupIDs != nil {
			if err := snap.checkGroups(*in.GroupIDs); err != nil {
				return err
			}
			if err := setMemberships(ctx, q, snap, id, *in.GroupIDs); err != nil {
				return err
			}
		}
		row, err := q.UpdateInventoryHost(ctx, dbgen.UpdateInventoryHostParams{ID: id, InventoryID: inv, Name: name, Vars: newVars})
		if err != nil {
			return fmt.Errorf("update host: %w", mapErr(err))
		}
		if snap, err = loadSnapshot(ctx, q, inv); err != nil {
			return err
		}
		out, err = snap.hostView(row)
		return err
	})
	return out, ver, err
}

// DeleteHost removes a host.
func (s *Service) DeleteHost(ctx context.Context, ws, inv, id uuid.UUID, ifMatch *int) (int, error) {
	return s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		n, err := q.DeleteInventoryHost(ctx, dbgen.DeleteInventoryHostParams{ID: id, InventoryID: inv})
		if err != nil {
			return fmt.Errorf("delete host: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddMember puts a host into a group (idempotent).
func (s *Service) AddMember(ctx context.Context, ws, inv, group, host uuid.UUID, ifMatch *int) (int, error) {
	return s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		snap, err := loadSnapshot(ctx, q, inv)
		if err != nil {
			return err
		}
		if err := snap.checkGroups([]uuid.UUID{group}); err != nil {
			return err
		}
		if _, err := q.GetInventoryHost(ctx, dbgen.GetInventoryHostParams{ID: host, InventoryID: inv}); err != nil {
			return mapErr(err)
		}
		if err := q.AddInventoryMembership(ctx, dbgen.AddInventoryMembershipParams{GroupID: group, HostID: host}); err != nil {
			return fmt.Errorf("add membership: %w", mapErr(err))
		}
		return nil
	})
}

// RemoveMember takes a host out of a group.
func (s *Service) RemoveMember(ctx context.Context, ws, inv, group, host uuid.UUID, ifMatch *int) (int, error) {
	return s.mutate(ctx, ws, inv, ifMatch, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		n, err := q.DeleteInventoryMembership(ctx, dbgen.DeleteInventoryMembershipParams{GroupID: group, HostID: host})
		if err != nil {
			return fmt.Errorf("remove membership: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// HostsInGroups returns the sorted names of all hosts that are members of any
// of the given groups, directly or through child groups. It backs the policy
// ScopeResolver for inventory_group scopes (ADR-0007). Group ids that do not
// belong to the inventory are ignored.
func (s *Service) HostsInGroups(ctx context.Context, ws, inv uuid.UUID, groupIDs []uuid.UUID) ([]string, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return nil, err
	}
	children := map[uuid.UUID][]uuid.UUID{}
	for _, l := range snap.links {
		children[l.ParentID] = append(children[l.ParentID], l.ChildID)
	}
	known := map[uuid.UUID]bool{}
	for _, g := range snap.groups {
		known[g.ID] = true
	}
	in := map[uuid.UUID]bool{}
	var walk func(uuid.UUID)
	walk = func(g uuid.UUID) {
		if in[g] {
			return
		}
		in[g] = true
		for _, c := range children[g] {
			walk(c)
		}
	}
	for _, g := range groupIDs {
		if known[g] {
			walk(g)
		}
	}
	hostName := map[uuid.UUID]string{}
	for _, h := range snap.hosts {
		hostName[h.ID] = h.Name
	}
	set := map[string]bool{}
	for _, m := range snap.members {
		if in[m.GroupID] {
			set[hostName[m.HostID]] = true
		}
	}
	return sortedSet(set), nil
}

// GroupsOfHost returns the ids of all groups the named host belongs to,
// directly or through parent groups.
func (s *Service) GroupsOfHost(ctx context.Context, ws, inv uuid.UUID, host string) ([]uuid.UUID, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return nil, err
	}
	snap, err := loadSnapshot(ctx, s.q, inv)
	if err != nil {
		return nil, err
	}
	var hid uuid.UUID
	for _, h := range snap.hosts {
		if h.Name == host {
			hid = h.ID
		}
	}
	parents := map[uuid.UUID][]uuid.UUID{}
	for _, l := range snap.links {
		parents[l.ChildID] = append(parents[l.ChildID], l.ParentID)
	}
	seen := map[uuid.UUID]bool{}
	var walk func(uuid.UUID)
	walk = func(g uuid.UUID) {
		if seen[g] {
			return
		}
		seen[g] = true
		for _, p := range parents[g] {
			walk(p)
		}
	}
	for _, m := range snap.members {
		if hid != uuid.Nil && m.HostID == hid {
			walk(m.GroupID)
		}
	}
	out := make([]uuid.UUID, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sortIDs(out)
	return out, nil
}

// HostNames returns the sorted names of all hosts of an inventory (scope
// kind inventory).
func (s *Service) HostNames(ctx context.Context, ws, inv uuid.UUID) ([]string, error) {
	if _, err := s.load(ctx, ws, inv); err != nil {
		return nil, err
	}
	hosts, err := s.q.ListInventoryHosts(ctx, inv)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = h.Name
	}
	return out, nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
