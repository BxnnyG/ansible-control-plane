package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/vault"
)

// ErrNotGitInventory is returned when a commit or diff is requested for an
// inventory that is not backed by a project file.
var ErrNotGitInventory = fmt.Errorf("%w: only git inventories can be committed", ErrInvalid)

// ErrNotImported is returned when a git inventory has no base revision yet.
var ErrNotImported = fmt.Errorf("%w: import the inventory from its project before committing", ErrInvalid)

// GitWriter writes files to a project's repository; *projects.Service
// implements it.
type GitWriter interface {
	WriteBack(ctx context.Context, ws, id uuid.UUID, req projects.WriteRequest) (projects.WriteResult, error)
	Preview(ctx context.Context, ws, id uuid.UUID, baseSHA string, plan projects.PlanFunc) ([]string, string, error)
}

// CommitInput describes a write-back of a git inventory.
type CommitInput struct {
	Message string // optional; a default is derived from the inventory name
	Author  projects.Author
}

// CommitResult is the outcome of Commit.
type CommitResult struct {
	Inventory Inventory `json:"inventory"`
	Branch    string    `json:"branch"`
	SHA       string    `json:"sha"`
	Paths     []string  `json:"paths"`
	Created   bool      `json:"branch_created"`
	NoOp      bool      `json:"no_op"`
}

// DiffResult is the preview of a commit. Vault values appear as their
// encrypted envelope, as they would be committed.
type DiffResult struct {
	BaseSHA string   `json:"base_sha"`
	Branch  string   `json:"branch"`
	Changed bool     `json:"changed"`
	Paths   []string `json:"paths"`
	Diff    string   `json:"diff"`
}

// gitTarget validates that inv can be written back.
func gitTarget(inv dbgen.Inventory) (project uuid.UUID, branch string, err error) {
	if inv.Mode != ModeGit || !inv.ProjectID.Valid {
		return uuid.Nil, "", ErrNotGitInventory
	}
	if inv.BaseSha == "" {
		return uuid.Nil, "", ErrNotImported
	}
	branch = inv.WriteBranch
	if branch == "" {
		branch = "acp/" + inv.Slug
	}
	return inv.ProjectID.Bytes, branch, nil
}

// Commit serializes the stored content back into the project's repository on
// the write branch: a commit authored by the acting user with a sign-off,
// pushed without force. The inventory row stays locked meanwhile, so no edit
// can slip in between the snapshot and the recorded base revision. A branch
// that moved yields *projects.ConflictError (errors.Is projects.ErrGitConflict).
func (s *Service) Commit(ctx context.Context, ws, id uuid.UUID, in CommitInput, ifMatch *int) (CommitResult, error) {
	if s.w == nil {
		return CommitResult{}, errors.New("git write-back is not configured")
	}
	author, err := cleanIdentity(in.Author)
	if err != nil {
		return CommitResult{}, err
	}
	var res CommitResult
	_, err = s.mutateRaw(ctx, ws, id, ifMatch, false, func(q *dbgen.Queries, cur dbgen.Inventory) error {
		var err error
		res, err = s.writeBack(ctx, q, ws, cur, in.Message, author)
		return err
	})
	return res, err
}

// writeBack serializes the stored content of the locked inventory cur into its
// project's write branch and records the new base revision. Callers hold the
// inventory row lock in the transaction behind q.
func (s *Service) writeBack(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, cur dbgen.Inventory, message string, author projects.Author) (CommitResult, error) {
	project, branch, err := gitTarget(cur)
	if err != nil {
		return CommitResult{}, err
	}
	snap, err := loadSnapshot(ctx, q, cur.ID)
	if err != nil {
		return CommitResult{}, err
	}
	doc, err := snap.doc()
	if err != nil {
		return CommitResult{}, err
	}
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "Update inventory " + cur.Name + " via ansible-control-plane"
	}
	wr, err := s.w.WriteBack(ctx, ws, project, projects.WriteRequest{
		Branch: branch, BaseSHA: cur.BaseSha, Message: msg, Author: author, Plan: planFor(cur, doc)})
	if err != nil {
		return CommitResult{}, err
	}
	row := cur
	if !wr.NoOp && wr.SHA != cur.BaseSha {
		if row, err = q.SetInventoryBaseSHA(ctx, dbgen.SetInventoryBaseSHAParams{ID: cur.ID, BaseSha: wr.SHA}); err != nil {
			return CommitResult{}, fmt.Errorf("record base revision: %w", err)
		}
	}
	view, err := s.view(ctx, q, row)
	if err != nil {
		return CommitResult{}, err
	}
	res := CommitResult{Inventory: view, Branch: wr.Branch, SHA: wr.SHA, Paths: wr.Paths, Created: wr.Created, NoOp: wr.NoOp}
	if res.Paths == nil {
		res.Paths = []string{}
	}
	return res, nil
}

// Diff previews what Commit would write: a unified diff against the files at
// the inventory's base revision. Nothing is changed anywhere.
func (s *Service) Diff(ctx context.Context, ws, id uuid.UUID) (DiffResult, error) {
	if s.w == nil {
		return DiffResult{}, errors.New("git write-back is not configured")
	}
	cur, err := s.load(ctx, ws, id)
	if err != nil {
		return DiffResult{}, err
	}
	project, branch, err := gitTarget(cur)
	if err != nil {
		return DiffResult{}, err
	}
	snap, err := loadSnapshot(ctx, s.q, id)
	if err != nil {
		return DiffResult{}, err
	}
	doc, err := snap.doc()
	if err != nil {
		return DiffResult{}, err
	}
	paths, diff, err := s.w.Preview(ctx, ws, project, cur.BaseSha, planFor(cur, doc))
	if err != nil {
		return DiffResult{}, err
	}
	if paths == nil {
		paths = []string{}
	}
	return DiffResult{BaseSHA: cur.BaseSha, Branch: branch, Changed: len(paths) > 0, Paths: paths, Diff: diff}, nil
}

// cleanIdentity makes the user's name and email safe for a Git identity.
func cleanIdentity(a projects.Author) (projects.Author, error) {
	clean := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f || r == '<' || r == '>' {
				return -1
			}
			return r
		}, s)
		return strings.TrimSpace(s)
	}
	a.Name, a.Email = clean(a.Name), clean(a.Email)
	if a.Email == "" || !utf8.ValidString(a.Name+a.Email) {
		return a, invalid("your account has no email address to attribute the commit to")
	}
	if a.Name == "" {
		a.Name, _, _ = strings.Cut(a.Email, "@")
	}
	return a, nil
}

// planFor builds the PlanFunc that serializes doc as the files of inv.
func planFor(inv dbgen.Inventory, doc *Doc) projects.PlanFunc {
	return func(base *os.Root) (projects.Plan, error) {
		return planFiles(base, inv.Path, inv.SourceFormat, doc)
	}
}

// varsSource is one group_vars/host_vars file read at the base revision.
type varsSource struct {
	kind string // "group_vars" or "host_vars"
	name string // group or host
	file string // path relative to the repository root
	vars map[string]any
}

type entityKey struct{ kind, name string }

// collectVarsSources reads the vars files that ApplyVarsDirs merges into the
// model, in the order they were applied, remembering where they came from.
// Unreadable or whole-file vault files are skipped like the importer does.
func collectVarsSources(doc *Doc, fsys fs.FS, invDir string) ([]varsSource, error) {
	var out []varsSource
	for _, kind := range []string{"group_vars", "host_vars"} {
		dir := path.Join(invDir, kind)
		entries, err := fs.ReadDir(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range entries {
			name, plain := varsEntryName(e)
			if plain && !knownExt(e) && !entityExists(doc, kind, name) {
				continue
			}
			rel := path.Join(dir, e.Name())
			info, err := fs.Stat(fsys, rel)
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", rel, err)
			}
			var files []string
			if info.IsDir() {
				err = fs.WalkDir(fsys, rel, func(fp string, d fs.DirEntry, werr error) error {
					if werr != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), "~") || !knownExt(d) {
						return werr
					}
					files = append(files, fp)
					return nil
				})
				if err != nil {
					return nil, fmt.Errorf("walk %s: %w", rel, err)
				}
			} else {
				files = []string{rel}
			}
			for _, f := range files {
				fi, err := fs.Stat(fsys, f)
				if err != nil {
					return nil, fmt.Errorf("stat %s: %w", f, err)
				}
				if !fi.Mode().IsRegular() || fi.Size() > MaxVarsFileSize {
					continue
				}
				data, err := fs.ReadFile(fsys, f)
				if err != nil {
					return nil, fmt.Errorf("read %s: %w", f, err)
				}
				if vault.IsVaultFile(data) {
					continue
				}
				m, err := ParseVarsYAML(string(data))
				if err != nil {
					return nil, fmt.Errorf("%s: %w", f, err)
				}
				out = append(out, varsSource{kind: kind, name: name, file: f, vars: m})
			}
		}
	}
	return out, nil
}

func entityExists(d *Doc, kind, name string) bool {
	_, ok := entityVars(d, entityKey{kind, name})
	return ok
}

func entityVars(d *Doc, e entityKey) (map[string]any, bool) {
	if e.kind == "group_vars" {
		if g := d.group(e.name); g != nil {
			return g.Vars, true
		}
		return nil, false
	}
	if h := d.Host(e.name); h != nil {
		return h.Vars, true
	}
	return nil, false
}

// stripOwned returns a copy of d without the variables that live in vars
// files (owner maps entity -> key -> file).
func stripOwned(d *Doc, owner map[entityKey]map[string]string) *Doc {
	cp := func(kind, name string, vars map[string]any) map[string]any {
		out := map[string]any{}
		own := owner[entityKey{kind, name}]
		for k, v := range vars {
			if _, ok := own[k]; !ok {
				out[k] = v
			}
		}
		return out
	}
	n := &Doc{}
	for _, h := range d.Hosts {
		n.Hosts = append(n.Hosts, &Host{Name: h.Name, Vars: cp("host_vars", h.Name, h.Vars)})
	}
	for _, g := range d.Groups {
		n.Groups = append(n.Groups, &Group{Name: g.Name, Vars: cp("group_vars", g.Name, g.Vars),
			Children: g.Children, Hosts: g.Hosts})
	}
	return n
}

// canonical renders d into a comparable form; an implicit empty "all" group
// is ignored.
func canonical(d *Doc) string {
	type grp struct {
		Name     string
		Vars     map[string]any
		Children []string
		Hosts    []string
	}
	var out struct {
		Hosts  []*Host
		Groups []grp
	}
	for _, h := range d.Hosts {
		v := h.Vars
		if v == nil {
			v = map[string]any{}
		}
		out.Hosts = append(out.Hosts, &Host{Name: h.Name, Vars: v})
	}
	for _, g := range d.Groups {
		if g.Name == GroupAll && len(g.Vars) == 0 && len(g.Children) == 0 && len(g.Hosts) == 0 {
			continue
		}
		v := g.Vars
		if v == nil {
			v = map[string]any{}
		}
		out.Groups = append(out.Groups, grp{g.Name, v, nilToEmpty(g.Children), nilToEmpty(g.Hosts)})
	}
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].Name < out.Hosts[j].Name })
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Name < out.Groups[j].Name })
	b, _ := json.Marshal(out)
	return string(b)
}

func nilToEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameVars(a, b map[string]any) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	var an, bn any
	_ = json.Unmarshal(aj, &an)
	_ = json.Unmarshal(bj, &bn)
	return reflect.DeepEqual(an, bn)
}

// planFiles computes the files that turn the repository at the base revision
// into the content of cur: the inventory file (format kept) plus every
// group_vars/host_vars file whose variables changed. Variables that came from
// a vars file go back to that file, all others stay inline.
func planFiles(base *os.Root, invPath, format string, cur *Doc) (projects.Plan, error) {
	data, err := readBounded(base, invPath)
	if err != nil {
		if errors.Is(err, errNotFile) {
			return projects.Plan{}, invalid("%s is not a regular file at the base revision", invPath)
		}
		return projects.Plan{}, err
	}
	var doc0 *Doc
	if format == "ini" {
		doc0, err = ParseINI(data)
	} else {
		doc0, err = ParseYAML(data)
	}
	if err != nil {
		return projects.Plan{}, fmt.Errorf("%s: %w", invPath, err)
	}
	dir := path.Dir(invPath)
	if _, err := ApplyVarsDirs(doc0, base.FS(), dir); err != nil {
		return projects.Plan{}, err
	}
	sources, err := collectVarsSources(doc0, base.FS(), dir)
	if err != nil {
		return projects.Plan{}, err
	}

	// Which file defines which key (later files win, as in Ansible).
	owner := map[entityKey]map[string]string{}
	var used []varsSource
	for _, src := range sources {
		e := entityKey{src.kind, src.name}
		if _, ok := entityVars(doc0, e); !ok {
			continue // ignored by the importer as well
		}
		if owner[e] == nil {
			owner[e] = map[string]string{}
		}
		for k := range src.vars {
			owner[e][k] = src.file
		}
		used = append(used, src)
	}
	// New variables of an entity that has vars files go to its last file, so
	// they are not shadowed or scattered across files.
	last := map[entityKey]string{}
	for _, src := range used {
		last[entityKey{src.kind, src.name}] = src.file
	}
	for e, file := range last {
		now, ok := entityVars(cur, e)
		prev, _ := entityVars(doc0, e)
		if !ok {
			continue
		}
		for k := range now {
			if _, existed := prev[k]; !existed {
				owner[e][k] = file
			}
		}
	}

	plan := projects.Plan{Watch: []string{invPath, path.Join(dir, "group_vars"), path.Join(dir, "host_vars")}}
	for _, src := range used {
		e := entityKey{src.kind, src.name}
		old := map[string]any{}
		for k, v := range src.vars {
			if owner[e][k] == src.file {
				old[k] = v
			}
		}
		now := map[string]any{}
		if vars, ok := entityVars(cur, e); ok {
			for k, v := range vars {
				if owner[e][k] == src.file {
					now[k] = v
				}
			}
		}
		switch {
		case sameVars(old, now):
		case len(now) == 0:
			plan.Changes = append(plan.Changes, projects.FileChange{Path: src.file, Delete: true})
		default:
			text, err := MarshalVarsYAML(now)
			if err != nil {
				return plan, err
			}
			plan.Changes = append(plan.Changes, projects.FileChange{Path: src.file, Content: []byte(text)})
		}
	}

	if canonical(stripOwned(doc0, owner)) != canonical(stripOwned(cur, owner)) {
		inline := stripOwned(cur, owner)
		var out []byte
		if format == "ini" {
			out, err = MarshalINI(inline)
		} else {
			out, err = MarshalYAML(inline)
		}
		if err != nil {
			return plan, err
		}
		plan.Changes = append(plan.Changes, projects.FileChange{Path: invPath, Content: out})
	}
	return plan, nil
}
