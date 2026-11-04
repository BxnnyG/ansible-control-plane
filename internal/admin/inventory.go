package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// ErrInventoryMismatch is returned when an inventory with the requested name
// exists but belongs to another project, path or mode.
var ErrInventoryMismatch = errors.New("an inventory with this name exists with different settings")

// ErrNoSuchWorkspace and ErrNoSuchProject are returned for unknown references.
var (
	ErrNoSuchWorkspace = errors.New("workspace not found")
	ErrNoSuchProject   = errors.New("project not found")
)

// InventoryService is the part of the inventory service used by the CLI.
type InventoryService interface {
	Create(ctx context.Context, ws uuid.UUID, in inventory.CreateInput) (inventory.Inventory, error)
	List(ctx context.Context, ws uuid.UUID) ([]inventory.Inventory, error)
	Import(ctx context.Context, ws, id uuid.UUID, in inventory.ImportInput, ifMatch *int) (inventory.ImportResult, error)
}

// ProjectLister lists the projects of a workspace.
type ProjectLister interface {
	List(ctx context.Context, ws uuid.UUID) ([]projects.Project, error)
}

// InventoryOps bundles the dependencies of the inventory CLI commands.
type InventoryOps struct {
	Q        *dbgen.Queries
	Inv      InventoryService
	Projects ProjectLister
}

// InventoryImportInput is the input of ImportInventory.
type InventoryImportInput struct {
	Workspace   string // workspace slug
	Project     string // project slug or id
	Path        string
	Name        string
	Mode        string // "git" (default) or "db"
	WriteBranch string
}

// InventoryImportResult reports what the command did. It never carries vars.
type InventoryImportResult struct {
	Inventory inventory.Inventory
	Created   bool
	Hosts     int
	Groups    int
	SHA       string
	Warnings  []string
}

// Workspace resolves a workspace slug.
func (o *InventoryOps) Workspace(ctx context.Context, slug string) (uuid.UUID, error) {
	w, err := o.Q.GetWorkspaceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: %q", ErrNoSuchWorkspace, slug)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get workspace: %w", err)
	}
	return w.ID, nil
}

// ListInventories returns the inventories of a workspace.
func (o *InventoryOps) ListInventories(ctx context.Context, workspace string) ([]inventory.Inventory, error) {
	ws, err := o.Workspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return o.Inv.List(ctx, ws)
}

func (o *InventoryOps) project(ctx context.Context, ws uuid.UUID, ref string) (uuid.UUID, error) {
	list, err := o.Projects.List(ctx, ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range list {
		if p.Slug == ref || p.ID.String() == ref {
			return p.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%w: %q", ErrNoSuchProject, ref)
}

// ImportInventory creates the inventory named in.Name if it does not exist
// (matched by name or slug) and imports the file from the latest project
// revision. An existing inventory is re-imported only if it matches the
// requested mode and, for git inventories, project and path.
func (o *InventoryOps) ImportInventory(ctx context.Context, in InventoryImportInput) (InventoryImportResult, error) {
	if in.Mode == "" {
		in.Mode = inventory.ModeGit
	}
	ws, err := o.Workspace(ctx, in.Workspace)
	if err != nil {
		return InventoryImportResult{}, err
	}
	pid, err := o.project(ctx, ws, in.Project)
	if err != nil {
		return InventoryImportResult{}, err
	}
	existing, err := o.Inv.List(ctx, ws)
	if err != nil {
		return InventoryImportResult{}, err
	}
	var inv *inventory.Inventory
	for i := range existing {
		if existing[i].Name == in.Name || existing[i].Slug == in.Name {
			inv = &existing[i]
			break
		}
	}
	created := false
	if inv == nil {
		ci := inventory.CreateInput{Name: in.Name, Mode: in.Mode}
		if in.Mode == inventory.ModeGit {
			ci.ProjectID, ci.Path, ci.WriteBranch = &pid, in.Path, in.WriteBranch
		}
		c, err := o.Inv.Create(ctx, ws, ci)
		if err != nil {
			return InventoryImportResult{}, err
		}
		inv, created = &c, true
	} else {
		if inv.Mode != in.Mode {
			return InventoryImportResult{}, fmt.Errorf("%w: mode is %q", ErrInventoryMismatch, inv.Mode)
		}
		if in.Mode == inventory.ModeGit && (inv.ProjectID == nil || *inv.ProjectID != pid || inv.Path != strings.TrimSpace(in.Path)) {
			return InventoryImportResult{}, fmt.Errorf("%w: project or path differ", ErrInventoryMismatch)
		}
	}
	res, err := o.Inv.Import(ctx, ws, inv.ID, inventory.ImportInput{ProjectID: &pid, Path: in.Path}, nil)
	if err != nil {
		return InventoryImportResult{}, err
	}
	details, _ := json.Marshal(map[string]any{"sha": res.SHA, "path": res.Inventory.Path, "hosts": res.Hosts, "groups": res.Groups, "created": created})
	if _, err := o.Q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: "inventory.import", TargetType: "inventory", TargetID: inv.ID.String(),
		WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: details,
	}); err != nil {
		return InventoryImportResult{}, fmt.Errorf("audit: %w", err)
	}
	return InventoryImportResult{Inventory: res.Inventory, Created: created, Hosts: res.Hosts, Groups: res.Groups, SHA: res.SHA, Warnings: res.Warnings}, nil
}
