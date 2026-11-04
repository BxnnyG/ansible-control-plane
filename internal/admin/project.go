package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// ProjectService is the part of the projects service used by the CLI.
type ProjectService interface {
	List(ctx context.Context, ws uuid.UUID) ([]projects.Project, error)
	Sync(ctx context.Context, ws, id uuid.UUID) (projects.Revision, error)
}

// ProjectOps bundles the dependencies of the project CLI commands.
type ProjectOps struct {
	Q        *dbgen.Queries
	Projects ProjectService
}

// ScanSummary holds the counts of a project scan; it carries no file content.
type ScanSummary struct {
	Playbooks   int
	Roles       int
	Inventories int
	VarsFiles   int
	VaultFiles  int
	Warnings    int
}

// ProjectSyncResult reports a finished sync.
type ProjectSyncResult struct {
	Project    projects.Project
	Status     string
	SHA        string
	RevisionID uuid.UUID
	Scan       ScanSummary
}

func (o *ProjectOps) workspace(ctx context.Context, slug string) (uuid.UUID, error) {
	return (&InventoryOps{Q: o.Q}).Workspace(ctx, slug)
}

// ListProjects returns the projects of a workspace.
func (o *ProjectOps) ListProjects(ctx context.Context, workspace string) ([]projects.Project, error) {
	ws, err := o.workspace(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return o.Projects.List(ctx, ws)
}

// SummarizeScan counts the entries of a scan result and its warnings.
// Warnings include both the scanner's and the sync's own.
func SummarizeScan(scan, warnings json.RawMessage) ScanSummary {
	var sc struct {
		Playbooks   []json.RawMessage `json:"playbooks"`
		Roles       []json.RawMessage `json:"roles"`
		Inventories []json.RawMessage `json:"inventories"`
		VarsFiles   []json.RawMessage `json:"vars_files"`
		VaultFiles  []json.RawMessage `json:"vault_files"`
		Warnings    []json.RawMessage `json:"warnings"`
	}
	var w []json.RawMessage
	_ = json.Unmarshal(scan, &sc)
	_ = json.Unmarshal(warnings, &w)
	return ScanSummary{len(sc.Playbooks), len(sc.Roles), len(sc.Inventories), len(sc.VarsFiles), len(sc.VaultFiles), len(sc.Warnings) + len(w)}
}

// SyncProject syncs one project (slug or id) through the projects service,
// using the manual trigger (the trigger column does not allow "cli"), and
// audits the run with actor "cli".
func (o *ProjectOps) SyncProject(ctx context.Context, workspace, ref string) (ProjectSyncResult, error) {
	ws, err := o.workspace(ctx, workspace)
	if err != nil {
		return ProjectSyncResult{}, err
	}
	list, err := o.Projects.List(ctx, ws)
	if err != nil {
		return ProjectSyncResult{}, fmt.Errorf("list projects: %w", err)
	}
	var p *projects.Project
	for i := range list {
		if list[i].Slug == ref || list[i].ID.String() == ref {
			p = &list[i]
			break
		}
	}
	if p == nil {
		return ProjectSyncResult{}, fmt.Errorf("%w: %q", ErrNoSuchProject, ref)
	}
	audit := func(action string, details map[string]any) error {
		b, _ := json.Marshal(details)
		if _, err := o.Q.InsertAudit(ctx, dbgen.InsertAuditParams{
			ActorLabel: "cli", Action: action, TargetType: "project", TargetID: p.ID.String(),
			WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: b,
		}); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
		return nil
	}
	rev, err := o.Projects.Sync(ctx, ws, p.ID)
	if err != nil {
		if aerr := audit("project.sync_failed", map[string]any{"trigger": projects.TriggerManual, "source": "cli", "error": err.Error()}); aerr != nil {
			err = errors.Join(err, aerr)
		}
		return ProjectSyncResult{}, err
	}
	if err := audit("project.sync", map[string]any{"trigger": projects.TriggerManual, "source": "cli", "sha": rev.SHA}); err != nil {
		return ProjectSyncResult{}, err
	}
	return ProjectSyncResult{Project: *p, Status: "ok", SHA: rev.SHA, RevisionID: rev.ID, Scan: SummarizeScan(rev.Scan, rev.Warnings)}, nil
}
