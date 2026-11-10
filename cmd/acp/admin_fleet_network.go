package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

// matchNetwork resolves a --network reference: an exact id, else a unique
// case-insensitive match on external key, description or CIDR.
func matchNetwork(nets []fleet.NetworkView, ref string) (fleet.NetworkView, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		for _, n := range nets {
			if n.ID == id {
				return n, nil
			}
		}
		return fleet.NetworkView{}, fmt.Errorf("network %s not found", ref)
	}
	var found []fleet.NetworkView
	for _, n := range nets {
		if strings.EqualFold(n.ExternalKey, ref) || strings.EqualFold(n.Description, ref) || n.CIDR == ref {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return fleet.NetworkView{}, fmt.Errorf("network %q not found", ref)
	case 1:
		return found[0], nil
	}
	return fleet.NetworkView{}, fmt.Errorf("network %q is ambiguous (%d matches), use the id", ref, len(found))
}

// formatNetwork renders one network line (read-only, no secrets).
func formatNetwork(n fleet.NetworkView) string {
	state := "tracked"
	if !n.Tracked {
		state = "untracked"
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\thosts=%d", n.ID, state, n.CIDR, dash(n.ExternalKey), dash(n.Description), dash(n.ConnectorName), n.HostCount)
}

// runAdminFleetNetwork implements `acp admin fleet network list|track|untrack`.
// Changes are audited with actor "cli".
func runAdminFleetNetwork(ctx context.Context, a fleetArgs, ws uuid.UUID, pool *pgxpool.Pool, stdout io.Writer) error {
	store := fleet.NewStore(pool, nil)
	nets, err := store.Networks(ctx, ws, nil, nil)
	if err != nil {
		return err
	}
	if a.Action == "list" {
		for _, n := range nets {
			_, _ = fmt.Fprintln(stdout, formatNetwork(n))
		}
		return nil
	}
	n, err := matchNetwork(nets, a.Network)
	if err != nil {
		return err
	}
	tracked := a.Action == "track"
	if _, err := store.SetTracked(ctx, ws, []uuid.UUID{n.ID}, tracked); err != nil {
		return err
	}
	details, _ := json.Marshal(map[string]any{"source": "cli", "tracked": tracked, "cidr": n.CIDR})
	if _, err := dbgen.New(pool).InsertAudit(ctx, dbgen.InsertAuditParams{ActorLabel: "cli", Action: "fleet.network.track",
		TargetType: "network", TargetID: n.ID.String(), WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: details}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	n.Tracked = tracked
	_, _ = fmt.Fprintln(stdout, formatNetwork(n))
	return nil
}
