package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/vault"
)

// Errors returned by the Service. Validation errors wrap ErrInvalid.
var (
	ErrNotFound     = errors.New("inventory resource not found")
	ErrConflict     = errors.New("an inventory resource with this name already exists")
	ErrPrecondition = errors.New("inventory was modified by someone else")
)

// Modes of an inventory.
const (
	ModeDB  = "db"
	ModeGit = "git"
)

// maxSourceSize bounds the inventory file read during import.
const maxSourceSize = 8 << 20

// maxVarsSize bounds the YAML text of one group or host variables field.
const maxVarsSize = 64 << 10

// DB is the database access of the Service; *pgxpool.Pool implements it.
type DB interface {
	dbgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Checkouts gives read access to the synced Git checkout of a project;
// *projects.Service implements it.
type Checkouts interface {
	WithCheckout(ctx context.Context, ws, id uuid.UUID, fn func(root *os.Root, sha string) error) error
}

// Options configures a Service.
type Options struct {
	DB        DB
	Checkouts Checkouts
	// Writer enables Commit and Diff for git inventories (optional).
	Writer GitWriter
}

// Service manages inventories, their content and imports.
type Service struct {
	db DB
	q  *dbgen.Queries
	co Checkouts
	w  GitWriter
}

// New builds a Service.
func New(o Options) *Service {
	return &Service{db: o.DB, q: dbgen.New(o.DB), co: o.Checkouts, w: o.Writer}
}

// Inventory is the API view of an inventory.
type Inventory struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	Mode         string     `json:"mode"`
	ProjectID    *uuid.UUID `json:"project_id"`
	Path         string     `json:"path"`
	WriteBranch  string     `json:"write_branch"`
	SourceFormat string     `json:"source_format"`
	BaseSHA      string     `json:"base_sha"`
	Version      int        `json:"version"`
	LastImportAt *time.Time `json:"last_import_at"`
	Hosts        int        `json:"hosts"`
	Groups       int        `json:"groups"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// CreateInput creates an inventory. Slug defaults to a slug of the name and
// WriteBranch to acp/<slug> (git mode only).
type CreateInput struct {
	Name, Slug, Mode string
	ProjectID        *uuid.UUID
	Path             string
	WriteBranch      string
	CreatedBy        uuid.UUID
}

// UpdateInput changes the settings of an inventory; nil fields stay as they
// are. The mode never changes.
type UpdateInput struct {
	Name, Slug, Path, WriteBranch *string
	ProjectID                     *uuid.UUID
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		}
	}
	return err
}

func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

func cleanName(s, what string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 100 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
		return "", invalid("%s must be 1-100 characters", what)
	}
	return s, nil
}

func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.HasSuffix(b, ".")
}

// cleanPath validates a repository-relative file path.
func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || !fs.ValidPath(p) || strings.ContainsAny(p, "\x00\r\n\\") {
		return "", invalid("path must be a relative file path inside the repository")
	}
	return p, nil
}

func toPg(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}

func fromPg(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func toInventory(r dbgen.Inventory, hosts, groups int) Inventory {
	inv := Inventory{
		ID: r.ID, Name: r.Name, Slug: r.Slug, Mode: r.Mode, ProjectID: fromPg(r.ProjectID), Path: r.Path,
		WriteBranch: r.WriteBranch, SourceFormat: r.SourceFormat, BaseSHA: r.BaseSha, Version: int(r.Version),
		Hosts: hosts, Groups: groups, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.LastImportAt.Valid {
		t := r.LastImportAt.Time
		inv.LastImportAt = &t
	}
	return inv
}

func (s *Service) view(ctx context.Context, q *dbgen.Queries, r dbgen.Inventory) (Inventory, error) {
	c, err := q.CountInventoryContent(ctx, r.ID)
	if err != nil {
		return Inventory{}, fmt.Errorf("count inventory content: %w", err)
	}
	return toInventory(r, int(c.Hosts), int(c.Groups)), nil
}

func (s *Service) load(ctx context.Context, ws, id uuid.UUID) (dbgen.Inventory, error) {
	r, err := s.q.GetInventory(ctx, dbgen.GetInventoryParams{ID: id, WorkspaceID: ws})
	return r, mapErr(err)
}

// settings is the validated set of editable inventory fields.
type settings struct {
	name, slug, path, branch string
	project                  *uuid.UUID
}

func (s *Service) validateSettings(ctx context.Context, ws uuid.UUID, mode string, in settings) (settings, error) {
	var err error
	if in.name, err = cleanName(in.name, "name"); err != nil {
		return in, err
	}
	if in.slug == "" {
		in.slug = slugify(in.name)
	}
	if !slugRe.MatchString(in.slug) {
		return in, invalid("slug must match %s", slugRe)
	}
	if mode == ModeDB {
		if in.project != nil || in.path != "" || in.branch != "" {
			return in, invalid("project, path and write branch are only valid in git mode")
		}
		return in, nil
	}
	if in.project == nil {
		return in, invalid("git mode requires a project")
	}
	if _, err := s.q.GetProject(ctx, dbgen.GetProjectParams{ID: *in.project, WorkspaceID: ws}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return in, invalid("project does not exist in this workspace")
		}
		return in, fmt.Errorf("get project: %w", err)
	}
	if in.path, err = cleanPath(in.path); err != nil {
		return in, err
	}
	if in.branch == "" {
		in.branch = "acp/" + in.slug
	}
	if !validBranch(in.branch) {
		return in, invalid("write branch is not a valid branch name")
	}
	return in, nil
}

// Create adds an empty inventory.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (Inventory, error) {
	if in.Mode != ModeDB && in.Mode != ModeGit {
		return Inventory{}, invalid("mode must be %q or %q", ModeDB, ModeGit)
	}
	v, err := s.validateSettings(ctx, ws, in.Mode, settings{in.Name, in.Slug, in.Path, in.WriteBranch, in.ProjectID})
	if err != nil {
		return Inventory{}, err
	}
	row, err := s.q.CreateInventory(ctx, dbgen.CreateInventoryParams{
		WorkspaceID: ws, Name: v.name, Slug: v.slug, Mode: in.Mode, ProjectID: toPg(v.project), Path: v.path,
		WriteBranch: v.branch, SourceFormat: formatOf(v.path, nil), CreatedBy: pgtype.UUID{Bytes: in.CreatedBy, Valid: in.CreatedBy != uuid.Nil},
	})
	if err != nil {
		return Inventory{}, fmt.Errorf("create inventory: %w", mapErr(err))
	}
	return toInventory(row, 0, 0), nil
}

// Get returns one inventory.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Inventory, error) {
	r, err := s.load(ctx, ws, id)
	if err != nil {
		return Inventory{}, err
	}
	return s.view(ctx, s.q, r)
}

// List returns the inventories of a workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Inventory, error) {
	rows, err := s.q.ListInventories(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list inventories: %w", err)
	}
	out := make([]Inventory, 0, len(rows))
	for _, r := range rows {
		v, err := s.view(ctx, s.q, r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Update changes the settings of an inventory (optimistic: ifMatch is the
// version the caller edited, nil skips the check).
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in UpdateInput, ifMatch *int) (Inventory, error) {
	var out Inventory
	_, err := s.mutateRaw(ctx, ws, id, ifMatch, false, func(q *dbgen.Queries, cur dbgen.Inventory) error {
		v := settings{cur.Name, cur.Slug, cur.Path, cur.WriteBranch, fromPg(cur.ProjectID)}
		if in.Name != nil {
			v.name = *in.Name
		}
		if in.Slug != nil {
			v.slug = *in.Slug
		}
		if in.Path != nil {
			v.path = *in.Path
		}
		if in.WriteBranch != nil {
			v.branch = *in.WriteBranch
		}
		if in.ProjectID != nil {
			v.project = in.ProjectID
		}
		v, err := s.validateSettings(ctx, ws, cur.Mode, v)
		if err != nil {
			return err
		}
		row, err := q.UpdateInventory(ctx, dbgen.UpdateInventoryParams{ID: id, WorkspaceID: ws, Name: v.name, Slug: v.slug,
			ProjectID: toPg(v.project), Path: v.path, WriteBranch: v.branch})
		if err != nil {
			return fmt.Errorf("update inventory: %w", mapErr(err))
		}
		out, err = s.view(ctx, q, row)
		return err
	})
	return out, err
}

// Delete removes an inventory with all its groups and hosts.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteInventory(ctx, dbgen.DeleteInventoryParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete inventory: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// mutateRaw runs fn in a transaction with the inventory row locked, checks
// the optimistic version and (unless the caller already changed the version)
// bumps it. It returns the new version.
func (s *Service) mutateRaw(ctx context.Context, ws, id uuid.UUID, ifMatch *int, bump bool,
	fn func(q *dbgen.Queries, cur dbgen.Inventory) error) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	cur, err := q.LockInventory(ctx, dbgen.LockInventoryParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return 0, mapErr(err)
	}
	if ifMatch != nil && *ifMatch != int(cur.Version) {
		return 0, ErrPrecondition
	}
	if err := fn(q, cur); err != nil {
		return 0, err
	}
	version := int(cur.Version) + 1
	if bump {
		v, err := q.BumpInventoryVersion(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("bump version: %w", err)
		}
		version = int(v)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return version, nil
}

// mutate is mutateRaw for content edits: the version is bumped afterwards.
func (s *Service) mutate(ctx context.Context, ws, id uuid.UUID, ifMatch *int,
	fn func(q *dbgen.Queries, cur dbgen.Inventory) error) (int, error) {
	return s.mutateRaw(ctx, ws, id, ifMatch, true, fn)
}

// ImportInput selects the source file of an import. For git inventories
// ProjectID, Path and Format default to the stored settings; for DB
// inventories ProjectID and Path are required and are not stored.
type ImportInput struct {
	ProjectID *uuid.UUID
	Path      string
	Format    string // "ini", "yaml" or empty to detect from the extension
}

// ImportResult summarizes an import.
type ImportResult struct {
	Inventory Inventory `json:"inventory"`
	Hosts     int       `json:"hosts"`
	Groups    int       `json:"groups"`
	SHA       string    `json:"sha"`
	Warnings  []string  `json:"warnings"`
}

// formatOf picks the source format from a file name and optional content.
func formatOf(p string, _ []byte) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".yml", ".yaml", ".json":
		return "yaml"
	case "":
		if p == "" {
			return "yaml"
		}
		return "ini"
	}
	return "ini"
}

// Import replaces the content of an inventory with the parsed inventory file
// (plus group_vars/host_vars next to it) from the latest revision of a
// project, in one transaction. For git inventories this is the sync from the
// file.
func (s *Service) Import(ctx context.Context, ws, id uuid.UUID, in ImportInput, ifMatch *int) (ImportResult, error) {
	if s.co == nil {
		return ImportResult{}, errors.New("imports are not configured")
	}
	cur, err := s.load(ctx, ws, id)
	if err != nil {
		return ImportResult{}, err
	}
	project, p, format := in.ProjectID, in.Path, in.Format
	if cur.Mode == ModeGit {
		if project == nil {
			project = fromPg(cur.ProjectID)
		}
		if p == "" {
			p = cur.Path
		}
	}
	if project == nil {
		return ImportResult{}, invalid("a project is required")
	}
	if p, err = cleanPath(p); err != nil {
		return ImportResult{}, err
	}
	if format == "" {
		format = formatOf(p, nil)
	}
	if format != "ini" && format != "yaml" {
		return ImportResult{}, invalid("format must be ini or yaml")
	}
	if _, err := s.q.GetProject(ctx, dbgen.GetProjectParams{ID: *project, WorkspaceID: ws}); err != nil {
		return ImportResult{}, invalid("project does not exist in this workspace")
	}

	var doc *Doc
	var sha string
	var warnings []string
	err = s.co.WithCheckout(ctx, ws, *project, func(root *os.Root, rev string) error {
		sha = rev
		data, err := readBounded(root, p)
		if err != nil {
			return err
		}
		if vault.IsVaultFile(data) {
			return invalid("%s is vault encrypted; the inventory file itself must be readable", p)
		}
		if format == "yaml" {
			doc, err = ParseYAML(data)
		} else {
			doc, err = ParseINI(data)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		warnings, err = ApplyVarsDirs(doc, root.FS(), path.Dir(p))
		return err
	})
	if err != nil {
		if errors.Is(err, errNotFile) {
			return ImportResult{}, invalid("%s is not a regular file in the latest revision", p)
		}
		return ImportResult{}, err
	}
	if warnings == nil {
		warnings = []string{}
	}

	var res ImportResult
	_, err = s.mutateRaw(ctx, ws, id, ifMatch, false, func(q *dbgen.Queries, _ dbgen.Inventory) error {
		if err := q.ClearInventoryHosts(ctx, id); err != nil {
			return fmt.Errorf("clear hosts: %w", err)
		}
		if err := q.ClearInventoryGroups(ctx, id); err != nil {
			return fmt.Errorf("clear groups: %w", err)
		}
		if err := writeDoc(ctx, q, id, doc); err != nil {
			return err
		}
		row, err := q.SetInventoryImported(ctx, dbgen.SetInventoryImportedParams{ID: id, BaseSha: sha, SourceFormat: format})
		if err != nil {
			return fmt.Errorf("record import: %w", err)
		}
		res.Inventory, err = s.view(ctx, q, row)
		return err
	})
	if err != nil {
		return ImportResult{}, err
	}
	res.Hosts, res.Groups, res.SHA, res.Warnings = len(doc.Hosts), len(doc.Groups), sha, warnings
	return res, nil
}

var errNotFile = errors.New("not a regular file")

func readBounded(root *os.Root, p string) ([]byte, error) {
	f, err := root.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, invalid("%s does not exist in the latest revision", p)
		}
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", p, err)
	}
	if !info.Mode().IsRegular() {
		return nil, errNotFile
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSourceSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(data) > maxSourceSize {
		return nil, invalid("%s is larger than %d MiB", p, maxSourceSize>>20)
	}
	return data, nil
}

func varsJSON(m map[string]any) ([]byte, error) {
	if m == nil {
		m = map[string]any{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode vars: %w", err)
	}
	return b, nil
}

// writeDoc inserts the content of d into an empty inventory.
func writeDoc(ctx context.Context, q *dbgen.Queries, inv uuid.UUID, d *Doc) error {
	gid := map[string]uuid.UUID{}
	for _, g := range d.Groups {
		vars, err := varsJSON(g.Vars)
		if err != nil {
			return err
		}
		row, err := q.CreateInventoryGroup(ctx, dbgen.CreateInventoryGroupParams{InventoryID: inv, Name: g.Name, Vars: vars})
		if err != nil {
			return fmt.Errorf("create group %q: %w", g.Name, mapErr(err))
		}
		gid[g.Name] = row.ID
	}
	for _, g := range d.Groups {
		for _, c := range g.Children {
			if err := q.AddInventoryGroupLink(ctx, dbgen.AddInventoryGroupLinkParams{ParentID: gid[g.Name], ChildID: gid[c]}); err != nil {
				return fmt.Errorf("link group %q: %w", c, err)
			}
		}
	}
	hid := map[string]uuid.UUID{}
	for _, h := range d.Hosts {
		vars, err := varsJSON(h.Vars)
		if err != nil {
			return err
		}
		row, err := q.CreateInventoryHost(ctx, dbgen.CreateInventoryHostParams{InventoryID: inv, Name: h.Name, Vars: vars})
		if err != nil {
			return fmt.Errorf("create host %q: %w", h.Name, mapErr(err))
		}
		hid[h.Name] = row.ID
	}
	for _, g := range d.Groups {
		for _, h := range g.Hosts {
			if err := q.AddInventoryMembership(ctx, dbgen.AddInventoryMembershipParams{GroupID: gid[g.Name], HostID: hid[h]}); err != nil {
				return fmt.Errorf("add host %q to group %q: %w", h, g.Name, err)
			}
		}
	}
	return nil
}
