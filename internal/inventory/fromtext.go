package inventory

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// FromTextInput creates a DB inventory from inventory text.
type FromTextInput struct {
	Name, Slug string
	Format     string // "ini" or "yaml"
	Content    string
	CreatedBy  uuid.UUID
}

// FromTextResult summarizes CreateFromText.
type FromTextResult struct {
	Inventory Inventory
	Hosts     int
	Groups    int
}

// CreateFromText parses INI or YAML inventory text and stores it as a new DB
// inventory (hosts, groups, variables) in one transaction. Nothing is created
// when the text does not parse or the name or slug is taken. Variables are
// stored as given; the text is never logged.
func (s *Service) CreateFromText(ctx context.Context, ws uuid.UUID, in FromTextInput) (FromTextResult, error) {
	var doc *Doc
	var err error
	switch in.Format {
	case "ini":
		doc, err = ParseINI([]byte(in.Content))
	case "yaml":
		doc, err = ParseYAML([]byte(in.Content))
	default:
		return FromTextResult{}, invalid("format must be ini or yaml")
	}
	if err != nil {
		return FromTextResult{}, err
	}
	v, err := s.validateSettings(ctx, ws, ModeDB, settings{name: in.Name, slug: in.Slug})
	if err != nil {
		return FromTextResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return FromTextResult{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	row, err := q.CreateInventory(ctx, dbgen.CreateInventoryParams{
		WorkspaceID: ws, Name: v.name, Slug: v.slug, Mode: ModeDB, SourceFormat: in.Format,
		CreatedBy: pgtype.UUID{Bytes: in.CreatedBy, Valid: in.CreatedBy != uuid.Nil},
	})
	if err != nil {
		return FromTextResult{}, fmt.Errorf("create inventory: %w", mapErr(err))
	}
	if err := writeDoc(ctx, q, row.ID, doc); err != nil {
		return FromTextResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FromTextResult{}, fmt.Errorf("commit: %w", err)
	}
	inv, err := s.Get(ctx, ws, row.ID)
	if err != nil {
		return FromTextResult{}, err
	}
	return FromTextResult{Inventory: inv, Hosts: len(doc.Hosts), Groups: len(doc.Groups)}, nil
}
