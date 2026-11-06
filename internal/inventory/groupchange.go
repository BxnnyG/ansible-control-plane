package inventory

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// HostRef is an inventory host as far as group rules need it.
type HostRef struct {
	Name        string
	AnsibleHost string
}

// GroupState is the current state of one group of an inventory.
type GroupState struct {
	GroupExists bool
	Hosts       []HostRef // every host of the inventory
	Members     []string  // names of the direct members of the group
}

// HostAdd puts a host into the group. The host is created when no host with
// this name exists; AnsibleHost is then stored as its ansible_host variable.
type HostAdd struct {
	Name        string
	AnsibleHost string
}

// GroupChange is a membership change of one group. The group is created when
// it does not exist.
type GroupChange struct {
	Add    []HostAdd
	Remove []string
}

// GroupChangeResult lists what a change actually did.
type GroupChangeResult struct {
	GroupCreated bool
	HostsCreated []string
	Added        []string // hosts that became members
	Removed      []string // hosts that stopped being members
	Version      int
	Commit       *CommitResult // set for git inventories when a commit was attempted
}

// Changed reports whether the change modified the inventory.
func (r GroupChangeResult) Changed() bool {
	return r.GroupCreated || len(r.HostsCreated) > 0 || len(r.Added) > 0 || len(r.Removed) > 0
}

// GroupEditor drives ApplyGroupChange inside the transaction that holds the
// inventory lock.
type GroupEditor interface {
	// Plan computes the change from the fresh state of the locked inventory.
	Plan(ctx context.Context, q *dbgen.Queries, st GroupState) (GroupChange, error)
	// Done runs after the change was written to the database and before the
	// transaction commits (and, for git inventories, before the push result
	// is final): an error rolls the whole change back.
	Done(ctx context.Context, q *dbgen.Queries, res GroupChangeResult) error
}

// GroupApply is the input of ApplyGroupChange.
type GroupApply struct {
	Group   string
	Editor  GroupEditor
	IfMatch *int
	// Commit is used for git inventories: the changed model is written back to
	// the write branch in the same transaction. A conflict (moved branch, lost
	// push race) aborts and rolls back the database change.
	Commit CommitInput
}

func (st *GroupState) fill(snap snapshot, group string) {
	byID := map[uuid.UUID]string{}
	for _, h := range snap.hosts {
		byID[h.ID] = h.Name
		st.Hosts = append(st.Hosts, HostRef{Name: h.Name, AnsibleHost: ansibleHost(h.Vars)})
	}
	for _, g := range snap.groups {
		if g.Name != group {
			continue
		}
		st.GroupExists = true
		for _, m := range snap.members {
			if m.GroupID == g.ID {
				st.Members = append(st.Members, byID[m.HostID])
			}
		}
	}
	sort.Strings(st.Members)
}

// ApplyGroupChange changes the membership of one group atomically: with the
// inventory row locked it asks the editor for the change, creates the group
// and missing hosts, adds and removes memberships, bumps the version and, for
// git inventories, writes the result back to the write branch (never forced).
// The editor's Done hook runs in the same transaction. An empty change is a
// no-op without a version bump or commit.
func (s *Service) ApplyGroupChange(ctx context.Context, ws, id uuid.UUID, in GroupApply) (GroupChangeResult, error) {
	if in.Group == GroupAll {
		return GroupChangeResult{}, invalid("hosts are in %q implicitly", GroupAll)
	}
	if err := cleanGroupName(in.Group); err != nil {
		return GroupChangeResult{}, err
	}
	var author projects.Author
	var git bool
	if cur, err := s.load(ctx, ws, id); err != nil {
		return GroupChangeResult{}, err
	} else if cur.Mode == ModeGit {
		git = true
		if s.w == nil {
			return GroupChangeResult{}, fmt.Errorf("git write-back is not configured")
		}
		if author, err = cleanIdentity(in.Commit.Author); err != nil {
			return GroupChangeResult{}, err
		}
	}
	var res GroupChangeResult
	_, err := s.mutateRaw(ctx, ws, id, in.IfMatch, false, func(q *dbgen.Queries, cur dbgen.Inventory) error {
		if git {
			if _, _, err := gitTarget(cur); err != nil {
				return err
			}
		}
		snap, err := loadSnapshot(ctx, q, id)
		if err != nil {
			return err
		}
		var st GroupState
		st.fill(snap, in.Group)
		ch, err := in.Editor.Plan(ctx, q, st)
		if err != nil {
			return err
		}
		res = GroupChangeResult{Version: int(cur.Version)}
		if err := applyGroupChange(ctx, q, id, in.Group, snap, ch, &res); err != nil {
			return err
		}
		if err := in.Editor.Done(ctx, q, res); err != nil {
			return err
		}
		if !res.Changed() {
			return nil
		}
		v, err := q.BumpInventoryVersion(ctx, id)
		if err != nil {
			return fmt.Errorf("bump version: %w", err)
		}
		res.Version = int(v)
		if !git {
			return nil
		}
		cur.Version = v
		cr, err := s.writeBack(ctx, q, ws, cur, in.Commit.Message, author)
		if err != nil {
			return err
		}
		res.Commit = &cr
		return nil
	})
	return res, err
}

func applyGroupChange(ctx context.Context, q *dbgen.Queries, inv uuid.UUID, group string, snap snapshot, ch GroupChange, res *GroupChangeResult) error {
	hostID := map[string]uuid.UUID{}
	for _, h := range snap.hosts {
		hostID[h.Name] = h.ID
	}
	var gid uuid.UUID
	for _, g := range snap.groups {
		if g.Name == group {
			gid = g.ID
		}
	}
	member := map[uuid.UUID]bool{}
	if gid != uuid.Nil {
		for _, m := range snap.members {
			if m.GroupID == gid {
				member[m.HostID] = true
			}
		}
	}
	for _, r := range ch.Remove {
		hid, ok := hostID[r]
		if !ok || !member[hid] {
			continue
		}
		if _, err := q.DeleteInventoryMembership(ctx, dbgen.DeleteInventoryMembershipParams{GroupID: gid, HostID: hid}); err != nil {
			return fmt.Errorf("remove membership: %w", err)
		}
		delete(member, hid)
		res.Removed = append(res.Removed, r)
	}
	if len(ch.Add) == 0 {
		return nil
	}
	if gid == uuid.Nil {
		if len(snap.groups) >= MaxGroups {
			return invalid("more than %d groups", MaxGroups)
		}
		row, err := q.CreateInventoryGroup(ctx, dbgen.CreateInventoryGroupParams{InventoryID: inv, Name: group, Vars: []byte("{}")})
		if err != nil {
			return fmt.Errorf("create group: %w", mapErr(err))
		}
		gid = row.ID
		res.GroupCreated = true
	}
	count := len(snap.hosts)
	for _, a := range ch.Add {
		if err := cleanHostName(a.Name); err != nil {
			return err
		}
		hid, ok := hostID[a.Name]
		if !ok {
			if count >= MaxHosts {
				return invalid("more than %d hosts", MaxHosts)
			}
			vars := []byte("{}")
			if a.AnsibleHost != "" {
				var err error
				if vars, err = varsJSON(map[string]any{"ansible_host": a.AnsibleHost}); err != nil {
					return err
				}
			}
			row, err := q.CreateInventoryHost(ctx, dbgen.CreateInventoryHostParams{InventoryID: inv, Name: a.Name, Vars: vars})
			if err != nil {
				return fmt.Errorf("create host: %w", mapErr(err))
			}
			hid = row.ID
			hostID[a.Name] = hid
			count++
			res.HostsCreated = append(res.HostsCreated, a.Name)
		}
		if member[hid] {
			continue
		}
		if err := q.AddInventoryMembership(ctx, dbgen.AddInventoryMembershipParams{GroupID: gid, HostID: hid}); err != nil {
			return fmt.Errorf("add membership: %w", mapErr(err))
		}
		member[hid] = true
		res.Added = append(res.Added, a.Name)
	}
	return nil
}

// GroupState reads the current state of one group (without locking): used by
// previews. A missing group yields GroupExists false.
func (s *Service) GroupState(ctx context.Context, ws, id uuid.UUID, group string) (GroupState, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return GroupState{}, err
	}
	snap, err := loadSnapshot(ctx, s.q, id)
	if err != nil {
		return GroupState{}, err
	}
	var st GroupState
	st.fill(snap, group)
	return st, nil
}
