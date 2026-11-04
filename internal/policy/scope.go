package policy

import (
	"context"

	"github.com/google/uuid"
)

// Scope kinds, mirroring membership_scopes.kind.
const (
	KindNetwork        = "network"
	KindInventory      = "inventory"
	KindInventoryGroup = "inventory_group"
)

// Scope restricts a membership to one network, inventory or inventory group.
type Scope struct {
	Kind  string
	RefID string
}

// Ref names a resource that an action touches.
type Ref struct {
	Kind string
	ID   string
}

// Target describes what an action touches. Hosts is the effective host set
// (resolved by the caller); Refs are the resources named directly.
type Target struct {
	Refs  []Ref
	Hosts []string
	// InventoryID names the inventory that group refs and Hosts belong to.
	// It may be empty when the target has exactly one inventory ref.
	InventoryID string
}

// ScopeResolver decides whether the whole target lies inside the scopes of a
// membership. Implementations must deny if any part of the target is outside:
// there is no silent filtering.
type ScopeResolver interface {
	InScope(ctx context.Context, workspaceID string, scopes []Scope, t Target) (bool, error)
}

// RefResolver matches refs by kind and id. It cannot expand hosts, so any
// host in the target is denied for scoped memberships. Production uses the
// inventory-backed InventoryResolver.
type RefResolver struct{}

// InScope implements ScopeResolver. An empty target is denied (fail closed).
func (RefResolver) InScope(_ context.Context, _ string, scopes []Scope, t Target) (bool, error) {
	if len(t.Refs) == 0 && len(t.Hosts) == 0 {
		return false, nil
	}
	if len(t.Hosts) > 0 {
		return false, nil
	}
	allowed := make(map[Scope]bool, len(scopes))
	for _, s := range scopes {
		allowed[s] = true
	}
	for _, r := range t.Refs {
		if !allowed[Scope{Kind: r.Kind, RefID: r.ID}] {
			return false, nil
		}
	}
	return true, nil
}

// Access implements AccessResolver: only an inventory scope grants access.
func (RefResolver) Access(_ context.Context, _ string, scopes []Scope, inv uuid.UUID) (Access, error) {
	if hasInventoryScope(scopes, inv) {
		return Access{All: true}, nil
	}
	return Access{}, nil
}
