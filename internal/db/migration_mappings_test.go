package db

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

func TestMigrationMappingsIdempotent(t *testing.T) {
	f := newRunFixture(t)
	ctx := context.Background()
	first := uuid.New()
	p := dbgen.CreateMigrationMappingParams{Source: "semaphore", Kind: "secret", SourceID: "7", AcpID: first, WorkspaceID: f.ws.ID}
	a, err := f.q.CreateMigrationMapping(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	// A second insert with another acp_id must return the original mapping.
	p.AcpID = uuid.New()
	b, err := f.q.CreateMigrationMapping(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || b.AcpID != first {
		t.Fatalf("mapping not idempotent: %v vs %v", a, b)
	}
	list, err := f.q.ListMigrationMappings(ctx, dbgen.ListMigrationMappingsParams{Source: "semaphore", WorkspaceID: f.ws.ID})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	if _, err := f.q.ListWorkspaceInventoryHostIdentifiers(ctx, f.ws.ID); err != nil {
		t.Fatal(err)
	}
}
