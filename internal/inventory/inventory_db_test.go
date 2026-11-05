package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// dirCheckouts serves a plain directory as the project checkout.
type dirCheckouts struct {
	dir string
	sha string
}

func (d *dirCheckouts) WithCheckout(_ context.Context, _, _ uuid.UUID, fn func(*os.Root, string) error) error {
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fn(root, d.sha)
}

type env struct {
	svc     *Service
	q       *dbgen.Queries
	ws      uuid.UUID
	project uuid.UUID
	co      *dirCheckouts
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "w" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), Name: "W"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	co := &dirCheckouts{dir: t.TempDir(), sha: "0123456789abcdef0123456789abcdef01234567"}
	return &env{svc: New(Options{DB: pool, Checkouts: co}), q: q, ws: ws.ID, project: prj.ID, co: co}
}

func (e *env) write(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(e.co.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *env) newInv(t *testing.T, name string) Inventory {
	t.Helper()
	inv, err := e.svc.Create(context.Background(), e.ws, CreateInput{Name: name, Mode: ModeDB})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func ptr[T any](v T) *T { return &v }

func TestInventoryCRUDAndValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	proj := e.project
	tests := []struct {
		name string
		in   CreateInput
		err  error
	}{
		{"db ok", CreateInput{Name: "Lab Inventory", Mode: ModeDB}, nil},
		{"duplicate", CreateInput{Name: "Lab Inventory", Mode: ModeDB}, ErrConflict},
		{"bad mode", CreateInput{Name: "x", Mode: "file"}, ErrInvalid},
		{"empty name", CreateInput{Name: " ", Mode: ModeDB}, ErrInvalid},
		{"db with project", CreateInput{Name: "y", Mode: ModeDB, ProjectID: &proj}, ErrInvalid},
		{"git without project", CreateInput{Name: "z", Mode: ModeGit, Path: "hosts.ini"}, ErrInvalid},
		{"git unknown project", CreateInput{Name: "z", Mode: ModeGit, Path: "hosts.ini", ProjectID: ptr(uuid.New())}, ErrInvalid},
		{"git traversal", CreateInput{Name: "z", Mode: ModeGit, Path: "../hosts.ini", ProjectID: &proj}, ErrInvalid},
		{"git absolute", CreateInput{Name: "z", Mode: ModeGit, Path: "/etc/hosts", ProjectID: &proj}, ErrInvalid},
		{"git bad branch", CreateInput{Name: "z", Mode: ModeGit, Path: "hosts.ini", ProjectID: &proj, WriteBranch: "a..b"}, ErrInvalid},
		{"git ok", CreateInput{Name: "Git Inv", Mode: ModeGit, Path: "inventories/hosts.yml", ProjectID: &proj}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := e.svc.Create(ctx, e.ws, tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err=%v want %v", err, tc.err)
			}
			if err == nil && tc.in.Mode == ModeGit && (inv.WriteBranch != "acp/git-inv" || inv.SourceFormat != "yaml" || inv.Version != 1) {
				t.Errorf("git defaults: %+v", inv)
			}
		})
	}
	list, err := e.svc.List(ctx, e.ws)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	inv := list[1] // ordered by name: Git Inv, Lab Inventory
	if _, err := e.svc.Get(ctx, uuid.New(), inv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-workspace get: %v", err)
	}
	up, err := e.svc.Update(ctx, e.ws, inv.ID, UpdateInput{Name: ptr("Renamed")}, ptr(inv.Version))
	if err != nil || up.Name != "Renamed" || up.Version != inv.Version+1 {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := e.svc.Update(ctx, e.ws, inv.ID, UpdateInput{Name: ptr("Stale")}, ptr(inv.Version)); !errors.Is(err, ErrPrecondition) {
		t.Errorf("stale update: %v", err)
	}
	if _, err := e.svc.Update(ctx, e.ws, inv.ID, UpdateInput{Path: ptr("x.ini")}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("db inventory must not take a path: %v", err)
	}
	if err := e.svc.Delete(ctx, e.ws, inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.ws, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestGroupsHostsAndRender(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	inv := e.newInv(t, "content")

	parent, ver, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: "parent", VarsYAML: "tier: base\n"}, ptr(1))
	if err != nil || ver != 2 {
		t.Fatalf("create group: %v ver=%d", err, ver)
	}
	child, _, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: "child", ParentIDs: []uuid.UUID{parent.ID}, VarsYAML: "tier: leaf"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: "child"}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate group: %v", err)
	}
	if _, _, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: "bad", VarsYAML: "- list"}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("non-mapping vars: %v", err)
	}
	if _, _, err := e.svc.CreateGroup(ctx, e.ws, inv.ID, GroupInput{Name: "ungrouped"}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("reserved name: %v", err)
	}
	if _, _, err := e.svc.UpdateGroup(ctx, e.ws, inv.ID, parent.ID, GroupUpdate{ParentIDs: &[]uuid.UUID{child.ID}}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("cycle must be rejected: %v", err)
	}
	if _, _, err := e.svc.UpdateGroup(ctx, e.ws, inv.ID, parent.ID, GroupUpdate{ParentIDs: &[]uuid.UUID{parent.ID}}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("self parent must be rejected: %v", err)
	}
	h1, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: "web01.example.test", GroupIDs: []uuid.UUID{child.ID}, VarsYAML: "ansible_host: 192.0.2.10\nsecret: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3030\n"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: "loner.example.test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: "web01.example.test"}, nil); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate host: %v", err)
	}
	if _, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: "bad name"}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("host name with space: %v", err)
	}
	if _, _, err := e.svc.CreateHost(ctx, e.ws, inv.ID, HostInput{Name: "x", GroupIDs: []uuid.UUID{uuid.New()}}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown group: %v", err)
	}
	got, err := e.svc.GetHost(ctx, e.ws, inv.ID, h1.ID)
	if err != nil || !strings.Contains(got.VarsYAML, "secret: !vault |") || len(got.GroupIDs) != 1 {
		t.Fatalf("host view: %+v %v", got, err)
	}

	// Membership add/remove and the scope helpers.
	if _, err := e.svc.AddMember(ctx, e.ws, inv.ID, parent.ID, h2.ID, nil); err != nil {
		t.Fatal(err)
	}
	names, err := e.svc.HostsInGroups(ctx, e.ws, inv.ID, []uuid.UUID{parent.ID})
	if err != nil || strings.Join(names, ",") != "loner.example.test,web01.example.test" {
		t.Errorf("HostsInGroups(parent)=%v %v", names, err)
	}
	names, _ = e.svc.HostsInGroups(ctx, e.ws, inv.ID, []uuid.UUID{child.ID, uuid.New()})
	if strings.Join(names, ",") != "web01.example.test" {
		t.Errorf("HostsInGroups(child)=%v", names)
	}
	gs, err := e.svc.GroupsOfHost(ctx, e.ws, inv.ID, "web01.example.test")
	if err != nil || len(gs) != 2 {
		t.Errorf("GroupsOfHost=%v %v (want child and parent)", gs, err)
	}
	if gs, _ := e.svc.GroupsOfHost(ctx, e.ws, inv.ID, "unknown"); len(gs) != 0 {
		t.Errorf("unknown host groups=%v", gs)
	}
	if all, _ := e.svc.HostNames(ctx, e.ws, inv.ID); len(all) != 2 {
		t.Errorf("HostNames=%v", all)
	}
	if _, err := e.svc.RemoveMember(ctx, e.ws, inv.ID, parent.ID, h2.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RemoveMember(ctx, e.ws, inv.ID, parent.ID, h2.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
	if ws2, _ := e.svc.HostsInGroups(ctx, uuid.New(), inv.ID, nil); ws2 != nil {
		t.Errorf("foreign workspace must not see hosts")
	}

	r, err := e.svc.Render(ctx, e.ws, inv.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	out := js(r)
	for _, want := range []string{`"web01.example.test":{"ansible_host":"192.0.2.10","secret":"<vault>","tier":"leaf"}`, `"parent":{"children":["child"],"hosts":[],"vars":{}}`, `"ungrouped":{"children":[],"hosts":["loner.example.test"],"vars":{}}`} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "ANSIBLE_VAULT") {
		t.Errorf("render leaks vault envelope")
	}

	// Optimistic locking on content edits.
	cur, _ := e.svc.Get(ctx, e.ws, inv.ID)
	if _, _, err := e.svc.UpdateHost(ctx, e.ws, inv.ID, h2.ID, HostUpdate{VarsYAML: ptr("a: 1")}, ptr(cur.Version-1)); !errors.Is(err, ErrPrecondition) {
		t.Errorf("stale host edit: %v", err)
	}
	if _, _, err := e.svc.UpdateHost(ctx, e.ws, inv.ID, h2.ID, HostUpdate{VarsYAML: ptr("a: 1"), GroupIDs: &[]uuid.UUID{child.ID}}, ptr(cur.Version)); err != nil {
		t.Errorf("host edit: %v", err)
	}

	// Deleting a group keeps children and hosts.
	if _, err := e.svc.DeleteGroup(ctx, e.ws, inv.ID, parent.ID, nil); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.GetGroup(ctx, e.ws, inv.ID, child.ID)
	if err != nil || len(c.ParentIDs) != 0 {
		t.Errorf("child after parent delete: %+v %v", c, err)
	}
	if _, err := e.svc.DeleteHost(ctx, e.ws, inv.ID, h1.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteHost(ctx, e.ws, inv.ID, h1.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("second host delete: %v", err)
	}
}

func TestImport(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.write(t, "inventories/hosts.ini", string(fixture(t, "hosts.ini")))
	e.write(t, "inventories/group_vars/web.yml", "tier: from-file\nextra: 1\n")
	e.write(t, "inventories/host_vars/standalone.example.test.yml", "pw: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3030\n")
	e.write(t, "inventories/group_vars/missing.yml", "x: 1\n")
	e.write(t, "secondary.yml", string(fixture(t, "hosts.yml")))

	gi, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "from git", Mode: ModeGit, ProjectID: &e.project, Path: "inventories/hosts.ini"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Import(ctx, e.ws, gi.ID, ImportInput{}, ptr(1))
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA != e.co.sha || res.Inventory.BaseSHA != e.co.sha || res.Inventory.SourceFormat != "ini" || res.Inventory.Version != 2 {
		t.Errorf("import result: %+v", res)
	}
	if res.Hosts != 8 || res.Groups != 4 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "missing.yml") {
		t.Errorf("counts/warnings: %+v", res)
	}
	d, err := e.svc.LoadDoc(ctx, e.ws, gi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := d.Group("web").Vars; v["tier"] != "from-file" || v["retries"] != float64(3) || v["extra"] != float64(1) {
		t.Errorf("web vars (file must win over inline): %v", v)
	}
	if env, ok := IsVault(d.Host("standalone.example.test").Vars["pw"]); !ok || !strings.HasPrefix(env, "$ANSIBLE_VAULT;") {
		t.Errorf("vault envelope not preserved opaque")
	}
	prev, _ := e.svc.Render(ctx, e.ws, gi.ID, false)
	if s := js(prev); strings.Contains(s, "ANSIBLE_VAULT") || !strings.Contains(s, `"pw":"<vault>"`) {
		t.Errorf("preview not masked: %s", s)
	}

	// A stale If-Match and a failing import leave the content untouched.
	if _, err := e.svc.Import(ctx, e.ws, gi.ID, ImportInput{}, ptr(1)); !errors.Is(err, ErrPrecondition) {
		t.Errorf("stale import: %v", err)
	}
	e.write(t, "broken.yml", "all: [")
	for name, in := range map[string]ImportInput{
		"broken yaml":   {Path: "broken.yml"},
		"missing file":  {Path: "nope.ini"},
		"directory":     {Path: "inventories"},
		"escape":        {Path: "../x"},
		"wrong project": {ProjectID: ptr(uuid.New())},
	} {
		if _, err := e.svc.Import(ctx, e.ws, gi.ID, in, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if cur, _ := e.svc.Get(ctx, e.ws, gi.ID); cur.Hosts != 8 || cur.Version != 2 {
		t.Errorf("failed imports changed content: %+v", cur)
	}

	// Re-import with a different file replaces everything; DB inventories
	// take project and path from the request and store neither.
	di := e.newInv(t, "db import")
	if _, err := e.svc.Import(ctx, e.ws, di.ID, ImportInput{}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("db import without source: %v", err)
	}
	res, err = e.svc.Import(ctx, e.ws, di.ID, ImportInput{ProjectID: &e.project, Path: "secondary.yml"}, nil)
	if err != nil || res.Hosts != 4 || res.Inventory.ProjectID != nil || res.Inventory.Path != "" || res.Inventory.SourceFormat != "yaml" {
		t.Fatalf("yaml import: %+v %v", res, err)
	}
	if _, err := e.svc.Import(ctx, e.ws, di.ID, ImportInput{ProjectID: &e.project, Path: "inventories/hosts.ini"}, nil); err != nil {
		t.Fatal(err)
	}
	if cur, _ := e.svc.Get(ctx, e.ws, di.ID); cur.Hosts != 8 || cur.Groups != 4 {
		t.Errorf("re-import did not replace: %+v", cur)
	}
}
