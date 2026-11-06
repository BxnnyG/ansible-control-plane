package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// Errors of AddToInventory.
var (
	// ErrManaged means the fleet host is already linked to an inventory host.
	ErrManaged = errors.New("host is already linked to an inventory host")
	// ErrNameTaken means the inventory already has a host with that name.
	ErrNameTaken = errors.New("the inventory already has a host with this name")
	// ErrInvalid wraps rejected input.
	ErrInvalid = errors.New("invalid input")
)

// InventoryEditor changes group memberships of an inventory;
// *inventory.Service implements it. DB inventories are written directly, Git
// inventories through the write-back flow (a commit on the write branch).
type InventoryEditor interface {
	ApplyGroupChange(ctx context.Context, ws, id uuid.UUID, in inventory.GroupApply) (inventory.GroupChangeResult, error)
}

// Adder adds unmanaged fleet hosts to inventories (FLEET-5).
type Adder struct {
	Fleet       *Store
	Inventories InventoryEditor
}

// AddInput describes one "add to inventory" action. Name defaults to the
// merged host name; the host's ansible_host is its primary address.
type AddInput struct {
	InventoryID uuid.UUID
	Group       string
	Name        string
	Author      projects.Author // used for the commit of Git inventories
	IfMatch     *int
}

// AddResult reports what was written.
type AddResult struct {
	Host        string                      `json:"host"`
	AnsibleHost string                      `json:"ansible_host"`
	Result      inventory.GroupChangeResult `json:"result"`
}

// PrimaryAddress picks the address written as ansible_host: the first IPv4
// address, else the first address.
func PrimaryAddress(ips []string) string {
	first := ""
	for _, s := range ips {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		a = a.Unmap()
		if a.Is4() {
			return a.String()
		}
		if first == "" {
			first = a.String()
		}
	}
	return first
}

type addEditor struct{ add inventory.HostAdd }

func (e addEditor) Plan(_ context.Context, _ *dbgen.Queries, st inventory.GroupState) (inventory.GroupChange, error) {
	for _, h := range st.Hosts {
		if h.Name == e.add.Name {
			return inventory.GroupChange{}, ErrNameTaken
		}
	}
	return inventory.GroupChange{Add: []inventory.HostAdd{e.add}}, nil
}

func (addEditor) Done(context.Context, *dbgen.Queries, inventory.GroupChangeResult) error {
	return nil
}

// AddToInventory puts an unmanaged fleet host into a group of the inventory,
// creating the inventory host (name, ansible_host) and, when missing, the
// group. visible limits which fleet hosts the caller may act on; hosts outside
// are reported as ErrNotFound.
func (a Adder) AddToInventory(ctx context.Context, ws, hostID uuid.UUID, in AddInput, visible Visible) (AddResult, error) {
	entries, err := a.Fleet.hostEntries(ctx, ws, true)
	if err != nil {
		return AddResult{}, err
	}
	var host *HostView
	for i := range entries {
		if entries[i].view.ID == hostID && visible.ok(entries[i].view.IPs) {
			host = &entries[i].view
		}
	}
	if host == nil {
		return AddResult{}, ErrNotFound
	}
	if host.Managed {
		return AddResult{}, ErrManaged
	}
	group := strings.TrimSpace(in.Group)
	if group == "" {
		return AddResult{}, fmt.Errorf("%w: group is required", ErrInvalid)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = host.Name
	}
	if name == "" {
		return AddResult{}, fmt.Errorf("%w: the host has no name, give one", ErrInvalid)
	}
	add := inventory.HostAdd{Name: name, AnsibleHost: PrimaryAddress(host.IPs)}
	res, err := a.Inventories.ApplyGroupChange(ctx, ws, in.InventoryID, inventory.GroupApply{
		Group: group, Editor: addEditor{add: add}, IfMatch: in.IfMatch,
		Commit: inventory.CommitInput{Message: "Add " + name + " from the fleet via ansible-control-plane", Author: in.Author},
	})
	if err != nil {
		return AddResult{}, err
	}
	for _, l := range []*[]string{&res.HostsCreated, &res.Added, &res.Removed} {
		if *l == nil {
			*l = []string{}
		}
	}
	return AddResult{Host: name, AnsibleHost: add.AnsibleHost, Result: res}, nil
}
