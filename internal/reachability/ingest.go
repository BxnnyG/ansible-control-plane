package reachability

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Ingestor stores probe results of one built-in probe run. It is constructed
// from the run's parameters by the worker API handler, which only does so
// for runs whose params carry the built-in probe marker.
type Ingestor struct {
	Q           *dbgen.Queries
	RunID       uuid.UUID
	WorkspaceID uuid.UUID
	InventoryID uuid.UUID
	Hosts       map[string]bool // the run's source hosts
}

// StoreHost validates and stores the results of one source host. Events for
// hosts that are not sources of the run, and target ids unknown to the
// workspace, are ignored; schema violations return an ErrPayload error.
func (g Ingestor) StoreHost(ctx context.Context, host string, raw []byte) error {
	if !g.Hosts[host] {
		return nil
	}
	results, err := ParsePayload(raw)
	if err != nil {
		return err
	}
	src, err := g.Q.ProbeSourceByHost(ctx, dbgen.ProbeSourceByHostParams{WorkspaceID: g.WorkspaceID, InventoryID: g.InventoryID, Name: host})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve probe source: %w", err)
	}
	for tid, r := range results {
		var lat pgtype.Int4
		if r.LatencyMS != nil {
			lat = pgtype.Int4{Int32: int32(*r.LatencyMS), Valid: true} //nolint:gosec // bounded by ParsePayload
		}
		err := g.Q.UpsertReachabilityResult(ctx, dbgen.UpsertReachabilityResultParams{
			SourceID: src, ID: tid, Status: r.Status, LatencyMs: lat, Detail: r.Detail,
			RunID: pgtype.UUID{Bytes: g.RunID, Valid: true}, WorkspaceID: g.WorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("store probe result: %w", err)
		}
	}
	return nil
}

// MarkUnreachable marks every enabled target of a source host that the worker
// could not reach as probe_unreachable, distinct from a failed check.
func (g Ingestor) MarkUnreachable(ctx context.Context, host string) error {
	if !g.Hosts[host] {
		return nil
	}
	src, err := g.Q.ProbeSourceByHost(ctx, dbgen.ProbeSourceByHostParams{WorkspaceID: g.WorkspaceID, InventoryID: g.InventoryID, Name: host})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve probe source: %w", err)
	}
	err = g.Q.MarkSourceUnreachable(ctx, dbgen.MarkSourceUnreachableParams{
		SourceID: src, RunID: pgtype.UUID{Bytes: g.RunID, Valid: true}, WorkspaceID: g.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("mark source unreachable: %w", err)
	}
	return nil
}
