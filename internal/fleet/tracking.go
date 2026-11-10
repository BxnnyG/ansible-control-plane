package fleet

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// nonNilIDs avoids a SQL NULL array (ANY(NULL) would null the flag).
func nonNilIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

// Network tracking lets users pick which discovered networks the fleet
// follows. A host is hidden by default when it has an address in at least one
// known network and every such network is untracked. Hosts without an address
// in any known network stay visible, because nothing says they were opted out.

// untrackedFilter returns a predicate that reports whether a host with the
// given addresses is hidden because all of its networks are untracked. It
// returns nil when no network is untracked (nothing is hidden).
func (s *Store) untrackedFilter(ctx context.Context, ws uuid.UUID) (func(ips []string) bool, error) {
	nets, err := s.q.ListNetworks(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	var tracked, untracked []netip.Prefix
	for _, n := range nets {
		if n.Tracked {
			tracked = append(tracked, n.Cidr)
		} else {
			untracked = append(untracked, n.Cidr)
		}
	}
	if len(untracked) == 0 {
		return nil, nil
	}
	return func(ips []string) bool { return hiddenByTracking(ips, tracked, untracked) }, nil
}

func inAny(ips []string, ps []netip.Prefix) bool {
	for _, p := range ps {
		if hasIPIn(ips, p) {
			return true
		}
	}
	return false
}

// hiddenByTracking is true when ips lie in an untracked network and in no
// tracked one.
func hiddenByTracking(ips []string, tracked, untracked []netip.Prefix) bool {
	return inAny(ips, untracked) && !inAny(ips, tracked)
}

// SetTracked sets the tracked flag of the given networks and returns how many
// belong to the workspace.
func (s *Store) SetTracked(ctx context.Context, ws uuid.UUID, ids []uuid.UUID, tracked bool) (int, error) {
	n, err := s.q.SetNetworksTracked(ctx, dbgen.SetNetworksTrackedParams{WorkspaceID: ws, Column2: nonNilIDs(ids), Tracked: tracked})
	if err != nil {
		return 0, fmt.Errorf("set networks tracked: %w", err)
	}
	return int(n), nil
}

// TrackOnly tracks exactly the given networks and untracks all others of the
// workspace. It returns the number of networks in the workspace.
func (s *Store) TrackOnly(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) (int, error) {
	n, err := s.q.SetNetworksTrackedOnly(ctx, dbgen.SetNetworksTrackedOnlyParams{WorkspaceID: ws, Column2: nonNilIDs(ids)})
	if err != nil {
		return 0, fmt.Errorf("track only networks: %w", err)
	}
	return int(n), nil
}

// NewNetworksTracked reports the workspace default for newly discovered networks.
func (s *Store) NewNetworksTracked(ctx context.Context, ws uuid.UUID) (bool, error) {
	v, err := s.q.GetFleetNewNetworksTracked(ctx, ws)
	if err != nil {
		return false, fmt.Errorf("get new-network default: %w", err)
	}
	return v, nil
}

// SetNewNetworksTracked sets the workspace default for newly discovered networks.
func (s *Store) SetNewNetworksTracked(ctx context.Context, ws uuid.UUID, tracked bool) error {
	if err := s.q.SetFleetNewNetworksTracked(ctx, dbgen.SetFleetNewNetworksTrackedParams{ID: ws, FleetNewNetworksTracked: tracked}); err != nil {
		return fmt.Errorf("set new-network default: %w", err)
	}
	return nil
}
