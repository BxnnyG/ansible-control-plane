package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Template is the API view of a run template. It never contains secret
// values, only references to the secret store.
type Template struct {
	ID                     uuid.UUID       `json:"id"`
	ProjectID              uuid.UUID       `json:"project_id"`
	InventoryID            uuid.UUID       `json:"inventory_id"`
	Name                   string          `json:"name"`
	Slug                   string          `json:"slug"`
	Description            string          `json:"description"`
	Playbook               string          `json:"playbook"`
	ExtraVars              map[string]any  `json:"extra_vars"`
	EnvVars                map[string]any  `json:"env_vars"`
	SSHSecretID            *uuid.UUID      `json:"ssh_secret_id"`
	BecomeSecretID         *uuid.UUID      `json:"become_secret_id"`
	VaultIdentityIDs       []uuid.UUID     `json:"vault_identity_ids"`
	DefaultLimit           string          `json:"default_limit"`
	DefaultTags            string          `json:"default_tags"`
	DefaultSkipTags        string          `json:"default_skip_tags"`
	Verbosity              int             `json:"verbosity"`
	CheckMode              bool            `json:"check_mode"`
	DiffMode               bool            `json:"diff_mode"`
	Survey                 json.RawMessage `json:"survey"`
	MaxConcurrent          int             `json:"max_concurrent"`
	AllowLimitOverride     bool            `json:"allow_limit_override"`
	AllowTagsOverride      bool            `json:"allow_tags_override"`
	AllowVarsOverride      bool            `json:"allow_vars_override"`
	AllowCheckOverride     bool            `json:"allow_check_override"`
	AllowDiffOverride      bool            `json:"allow_diff_override"`
	AllowVerbosityOverride bool            `json:"allow_verbosity_override"`
	WebhookEnabled         bool            `json:"webhook_enabled"`
	CreatedBy              *uuid.UUID      `json:"created_by"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

// TemplateInput creates or updates a template. On update, nil fields stay as
// they are; nullable references use projects.Field so that JSON null clears
// them. On create, ProjectID, InventoryID, Playbook and Name are required.
type TemplateInput struct {
	Name, Slug, Description *string
	ProjectID, InventoryID  *uuid.UUID
	Playbook                *string
	ExtraVars, EnvVars      map[string]any
	SSHSecretID             projects.Field[uuid.UUID]
	BecomeSecretID          projects.Field[uuid.UUID]
	VaultIdentityIDs        *[]uuid.UUID
	DefaultLimit            *string
	DefaultTags             *string
	DefaultSkipTags         *string
	Verbosity               *int
	CheckMode, DiffMode     *bool
	Survey                  projects.Field[json.RawMessage]
	MaxConcurrent           *int

	AllowLimitOverride, AllowTagsOverride, AllowVarsOverride *bool
	AllowCheckOverride, AllowDiffOverride                    *bool
	AllowVerbosityOverride                                   *bool

	CreatedBy uuid.UUID
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// forbiddenEnv lists variables that would let a template change how the
// worker process itself loads code.
var forbiddenEnv = map[string]bool{
	"PATH": true, "HOME": true, "PYTHONPATH": true, "PYTHONHOME": true, "PYTHONSTARTUP": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
}

// envDenied reports whether a template may not set the variable: the exact
// names above, the worker's own ACP_* variables and everything that selects
// or configures Ansible callbacks (the acp event callback must stay active).
func envDenied(k string) bool {
	return forbiddenEnv[k] || strings.HasPrefix(k, "ACP_") ||
		strings.HasPrefix(k, "ANSIBLE_CALLBACK") || strings.HasPrefix(k, "ANSIBLE_STDOUT_CALLBACK")
}

func (s *Service) toTemplate(ctx context.Context, q *dbgen.Queries, t dbgen.Template) (Template, error) {
	ex, err := decodeObject(t.ExtraVars)
	if err != nil {
		return Template{}, err
	}
	env, err := decodeObject(t.EnvVars)
	if err != nil {
		return Template{}, err
	}
	vis, err := q.ListTemplateVaultIdentities(ctx, t.ID)
	if err != nil {
		return Template{}, fmt.Errorf("list template vault identities: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(vis))
	for _, v := range vis {
		ids = append(ids, v.ID)
	}
	var survey json.RawMessage
	if len(t.Survey) > 0 {
		survey = t.Survey
	}
	return Template{
		ID: t.ID, ProjectID: t.ProjectID, InventoryID: t.InventoryID, Name: t.Name, Slug: t.Slug,
		Description: t.Description, Playbook: t.Playbook, ExtraVars: ex, EnvVars: env,
		SSHSecretID: fromPg(t.SshSecretID), BecomeSecretID: fromPg(t.BecomeSecretID), VaultIdentityIDs: ids,
		DefaultLimit: t.DefaultLimit, DefaultTags: t.DefaultTags, DefaultSkipTags: t.DefaultSkipTags,
		Verbosity: int(t.Verbosity), CheckMode: t.CheckMode, DiffMode: t.DiffMode, Survey: survey,
		MaxConcurrent: int(t.MaxConcurrent), AllowLimitOverride: t.AllowLimitOverride,
		AllowTagsOverride: t.AllowTagsOverride, AllowVarsOverride: t.AllowVarsOverride,
		AllowCheckOverride: t.AllowCheckOverride, AllowDiffOverride: t.AllowDiffOverride,
		AllowVerbosityOverride: t.AllowVerbosityOverride, WebhookEnabled: t.WebhookEnabled,
		CreatedBy: fromPg(t.CreatedBy), CreatedAt: t.CreatedAt.Time, UpdatedAt: t.UpdatedAt.Time,
	}, nil
}

// ListTemplates returns all templates of the workspace.
func (s *Service) ListTemplates(ctx context.Context, ws uuid.UUID) ([]Template, error) {
	rows, err := s.q.ListTemplates(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	out := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := s.toTemplate(ctx, s.q, r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// GetTemplate returns one template.
func (s *Service) GetTemplate(ctx context.Context, ws, id uuid.UUID) (Template, error) {
	r, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Template{}, mapErr(err)
	}
	return s.toTemplate(ctx, s.q, r)
}

func (s *Service) belongs(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, in resolved) error {
	if _, err := q.GetProject(ctx, dbgen.GetProjectParams{ID: in.project, WorkspaceID: ws}); err != nil {
		return refErr(err, "project")
	}
	if _, err := q.GetInventory(ctx, dbgen.GetInventoryParams{ID: in.inventory, WorkspaceID: ws}); err != nil {
		return refErr(err, "inventory")
	}
	checkSecret := func(id *uuid.UUID, what string, types ...string) error {
		if id == nil {
			return nil
		}
		sec, err := q.GetSecret(ctx, dbgen.GetSecretParams{ID: *id, WorkspaceID: ws})
		if err != nil {
			return refErr(err, what)
		}
		for _, t := range types {
			if sec.Type == t {
				return nil
			}
		}
		return fmt.Errorf("%w: %s has the wrong secret type", ErrInvalid, what)
	}
	if err := checkSecret(in.ssh, "ssh_secret_id", secrets.TypeSSHKey); err != nil {
		return err
	}
	if err := checkSecret(in.become, "become_secret_id", secrets.TypePassword); err != nil {
		return err
	}
	for _, v := range in.vaultIDs {
		if _, err := q.GetVaultIdentity(ctx, dbgen.GetVaultIdentityParams{ID: v, WorkspaceID: ws}); err != nil {
			return refErr(err, "vault identity")
		}
	}
	return nil
}

func refErr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s does not exist in this workspace", ErrInvalid, what)
	}
	return fmt.Errorf("check %s: %w", what, err)
}

// resolved holds the template values after applying defaults or a patch.
type resolved struct {
	project, inventory uuid.UUID
	ssh, become        *uuid.UUID
	vaultIDs           []uuid.UUID
	p                  dbgen.CreateTemplateParams
}

func resolveCreate(in TemplateInput) (resolved, error) {
	if in.ProjectID == nil || in.InventoryID == nil || in.Playbook == nil || in.Name == nil {
		return resolved{}, fmt.Errorf("%w: name, project_id, inventory_id and playbook are required", ErrInvalid)
	}
	r := resolved{project: *in.ProjectID, inventory: *in.InventoryID, ssh: in.SSHSecretID.Value, become: in.BecomeSecretID.Value}
	if in.VaultIdentityIDs != nil {
		r.vaultIDs = *in.VaultIdentityIDs
	}
	r.p = dbgen.CreateTemplateParams{
		ProjectID: r.project, InventoryID: r.inventory, Name: *in.Name, Playbook: *in.Playbook,
		MaxConcurrent: 1, AllowLimitOverride: true, AllowTagsOverride: true, AllowCheckOverride: true,
		AllowDiffOverride: true, AllowVerbosityOverride: true,
	}
	if in.Slug != nil {
		r.p.Slug = *in.Slug
	}
	return r, nil
}

func resolvePatch(cur dbgen.Template, vaultIDs []uuid.UUID, in TemplateInput) (resolved, error) {
	r := resolved{project: cur.ProjectID, inventory: cur.InventoryID, ssh: fromPg(cur.SshSecretID), become: fromPg(cur.BecomeSecretID), vaultIDs: vaultIDs}
	r.p = dbgen.CreateTemplateParams{
		ProjectID: cur.ProjectID, InventoryID: cur.InventoryID, Name: cur.Name, Slug: cur.Slug, Playbook: cur.Playbook,
		Description: cur.Description, ExtraVars: cur.ExtraVars, EnvVars: cur.EnvVars,
		DefaultLimit: cur.DefaultLimit, DefaultTags: cur.DefaultTags, DefaultSkipTags: cur.DefaultSkipTags,
		Verbosity: cur.Verbosity, CheckMode: cur.CheckMode, DiffMode: cur.DiffMode, Survey: cur.Survey,
		MaxConcurrent: cur.MaxConcurrent, AllowLimitOverride: cur.AllowLimitOverride, AllowTagsOverride: cur.AllowTagsOverride,
		AllowVarsOverride: cur.AllowVarsOverride, AllowCheckOverride: cur.AllowCheckOverride,
		AllowDiffOverride: cur.AllowDiffOverride, AllowVerbosityOverride: cur.AllowVerbosityOverride,
	}
	if in.ProjectID != nil {
		r.project, r.p.ProjectID = *in.ProjectID, *in.ProjectID
	}
	if in.InventoryID != nil {
		r.inventory, r.p.InventoryID = *in.InventoryID, *in.InventoryID
	}
	if in.Name != nil {
		r.p.Name = *in.Name
	}
	if in.Slug != nil {
		r.p.Slug = *in.Slug
	}
	if in.Playbook != nil {
		r.p.Playbook = *in.Playbook
	}
	if in.SSHSecretID.Set {
		r.ssh = in.SSHSecretID.Value
	}
	if in.BecomeSecretID.Set {
		r.become = in.BecomeSecretID.Value
	}
	if in.VaultIdentityIDs != nil {
		r.vaultIDs = *in.VaultIdentityIDs
	}
	return r, nil
}

// apply copies the optional scalar fields of in onto r.p and validates the result.
func (s *Service) apply(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, r *resolved, in TemplateInput) error {
	p := &r.p
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.DefaultLimit != nil {
		p.DefaultLimit = strings.TrimSpace(*in.DefaultLimit)
	}
	if in.DefaultTags != nil {
		p.DefaultTags = strings.TrimSpace(*in.DefaultTags)
	}
	if in.DefaultSkipTags != nil {
		p.DefaultSkipTags = strings.TrimSpace(*in.DefaultSkipTags)
	}
	if in.Verbosity != nil {
		if *in.Verbosity < 0 || *in.Verbosity > 4 {
			return fmt.Errorf("%w: verbosity must be between 0 and 4", ErrInvalid)
		}
		p.Verbosity = int16(*in.Verbosity) //nolint:gosec // range checked
	}
	if in.CheckMode != nil {
		p.CheckMode = *in.CheckMode
	}
	if in.DiffMode != nil {
		p.DiffMode = *in.DiffMode
	}
	if in.MaxConcurrent != nil {
		if *in.MaxConcurrent < 1 || *in.MaxConcurrent > 100 {
			return fmt.Errorf("%w: max_concurrent must be between 1 and 100", ErrInvalid)
		}
		p.MaxConcurrent = int32(*in.MaxConcurrent) //nolint:gosec // range checked
	}
	for _, f := range []struct {
		in  *bool
		dst *bool
	}{
		{in.AllowLimitOverride, &p.AllowLimitOverride}, {in.AllowTagsOverride, &p.AllowTagsOverride},
		{in.AllowVarsOverride, &p.AllowVarsOverride}, {in.AllowCheckOverride, &p.AllowCheckOverride},
		{in.AllowDiffOverride, &p.AllowDiffOverride}, {in.AllowVerbosityOverride, &p.AllowVerbosityOverride},
	} {
		if f.in != nil {
			*f.dst = *f.in
		}
	}
	if in.ExtraVars != nil {
		b, err := json.Marshal(in.ExtraVars)
		if err != nil {
			return fmt.Errorf("%w: extra_vars is not valid JSON", ErrInvalid)
		}
		p.ExtraVars = b
	}
	if in.EnvVars != nil {
		for k, v := range in.EnvVars {
			if !envNameRe.MatchString(k) || envDenied(k) {
				return fmt.Errorf("%w: environment variable name %q is not allowed", ErrInvalid, k)
			}
			switch v.(type) {
			case string, float64, bool:
			default:
				return fmt.Errorf("%w: environment variable %s must be a string, number or boolean", ErrInvalid, k)
			}
		}
		b, err := json.Marshal(in.EnvVars)
		if err != nil {
			return fmt.Errorf("%w: env_vars is not valid JSON", ErrInvalid)
		}
		p.EnvVars = b
	}
	if in.Survey.Set {
		if in.Survey.Value == nil {
			p.Survey = nil
		} else {
			if _, err := ParseSurvey(*in.Survey.Value); err != nil {
				return err
			}
			p.Survey = *in.Survey.Value
		}
	}
	if len(p.ExtraVars) == 0 {
		p.ExtraVars = []byte("{}")
	}
	if len(p.EnvVars) == 0 {
		p.EnvVars = []byte("{}")
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Playbook = strings.TrimSpace(p.Playbook)
	if p.Name == "" || len(p.Name) > 200 {
		return fmt.Errorf("%w: name must be 1 to 200 characters", ErrInvalid)
	}
	if p.Slug == "" {
		p.Slug = projects.Slugify(p.Name)
	}
	if !slugRe.MatchString(p.Slug) {
		return fmt.Errorf("%w: slug must be lowercase letters, digits and dashes", ErrInvalid)
	}
	if err := ValidatePattern(p.DefaultLimit); err != nil {
		return err
	}
	if err := s.belongs(ctx, q, ws, *r); err != nil {
		return err
	}
	_, res, err := s.revisionScan(ctx, r.project)
	if err != nil {
		return err
	}
	if !hasPlaybook(res, p.Playbook) {
		return fmt.Errorf("%w: playbook %q does not exist in the last scan of the project", ErrInvalid, p.Playbook)
	}
	return nil
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Service) setVaultIDs(ctx context.Context, q *dbgen.Queries, tid uuid.UUID, ids []uuid.UUID) error {
	if err := q.ClearTemplateVaultIdentities(ctx, tid); err != nil {
		return fmt.Errorf("clear template vault identities: %w", err)
	}
	seen := map[uuid.UUID]bool{}
	pos := 0
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if err := q.AddTemplateVaultIdentity(ctx, dbgen.AddTemplateVaultIdentityParams{TemplateID: tid, VaultIdentityID: id, Position: int32(pos)}); err != nil { //nolint:gosec // small index
			return fmt.Errorf("link vault identity: %w", mapErr(err))
		}
		pos++
	}
	return nil
}

// CreateTemplate validates and stores a template. The playbook must exist in
// the last scan of the project; project, inventory, secrets and vault
// identities must belong to the workspace.
func (s *Service) CreateTemplate(ctx context.Context, ws uuid.UUID, in TemplateInput) (Template, error) {
	r, err := resolveCreate(in)
	if err != nil {
		return Template{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Template{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	r.p.ExtraVars, r.p.EnvVars = []byte("{}"), []byte("{}")
	if err := s.apply(ctx, q, ws, &r, in); err != nil {
		return Template{}, err
	}
	r.p.WorkspaceID = ws
	r.p.SshSecretID, r.p.BecomeSecretID = toPg(r.ssh), toPg(r.become)
	if in.CreatedBy != uuid.Nil {
		r.p.CreatedBy = toPg(&in.CreatedBy)
	}
	row, err := q.CreateTemplate(ctx, r.p)
	if err != nil {
		return Template{}, fmt.Errorf("create template: %w", mapErr(err))
	}
	if err := s.setVaultIDs(ctx, q, row.ID, r.vaultIDs); err != nil {
		return Template{}, err
	}
	out, err := s.toTemplate(ctx, q, row)
	if err != nil {
		return Template{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Template{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// UpdateTemplate changes a template; nil fields of in stay as they are.
// Queued runs keep the parameter snapshot they were created with.
func (s *Service) UpdateTemplate(ctx context.Context, ws, id uuid.UUID, in TemplateInput) (Template, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Template{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	cur, err := q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Template{}, mapErr(err)
	}
	vis, err := q.ListTemplateVaultIdentities(ctx, id)
	if err != nil {
		return Template{}, fmt.Errorf("list template vault identities: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(vis))
	for _, v := range vis {
		ids = append(ids, v.ID)
	}
	r, err := resolvePatch(cur, ids, in)
	if err != nil {
		return Template{}, err
	}
	if err := s.apply(ctx, q, ws, &r, in); err != nil {
		return Template{}, err
	}
	p := r.p
	row, err := q.UpdateTemplate(ctx, dbgen.UpdateTemplateParams{
		ID: id, WorkspaceID: ws, ProjectID: p.ProjectID, InventoryID: p.InventoryID, Name: p.Name, Slug: p.Slug,
		Description: p.Description, Playbook: p.Playbook, ExtraVars: p.ExtraVars, EnvVars: p.EnvVars,
		SshSecretID: toPg(r.ssh), BecomeSecretID: toPg(r.become), DefaultLimit: p.DefaultLimit,
		DefaultTags: p.DefaultTags, DefaultSkipTags: p.DefaultSkipTags, Verbosity: p.Verbosity,
		CheckMode: p.CheckMode, DiffMode: p.DiffMode, Survey: p.Survey, MaxConcurrent: p.MaxConcurrent,
		AllowLimitOverride: p.AllowLimitOverride, AllowTagsOverride: p.AllowTagsOverride,
		AllowVarsOverride: p.AllowVarsOverride, AllowCheckOverride: p.AllowCheckOverride,
		AllowDiffOverride: p.AllowDiffOverride, AllowVerbosityOverride: p.AllowVerbosityOverride,
	})
	if err != nil {
		return Template{}, fmt.Errorf("update template: %w", mapErr(err))
	}
	if in.VaultIdentityIDs != nil {
		if err := s.setVaultIDs(ctx, q, id, r.vaultIDs); err != nil {
			return Template{}, err
		}
	}
	out, err := s.toTemplate(ctx, q, row)
	if err != nil {
		return Template{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Template{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// DeleteTemplate cancels the template's queued runs and deletes it in one
// transaction and returns the number of cancelled runs. Finished and running
// runs keep their history (template_id is set to NULL).
func (s *Service) DeleteTemplate(ctx context.Context, ws, id uuid.UUID) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if _, err := q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws}); err != nil {
		return 0, mapErr(err)
	}
	cancelled, err := q.CancelQueuedRunsForTemplate(ctx, dbgen.CancelQueuedRunsForTemplateParams{TemplateID: toPg(&id), WorkspaceID: ws})
	if err != nil {
		return 0, fmt.Errorf("cancel queued runs: %w", err)
	}
	n, err := q.DeleteTemplate(ctx, dbgen.DeleteTemplateParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return 0, fmt.Errorf("delete template: %w", mapErr(err))
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(cancelled), nil
}
