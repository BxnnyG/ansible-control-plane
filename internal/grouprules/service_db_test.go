package grouprules_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/grouprules"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

type fakeFleet struct{ hosts []fleet.HostView }

func (f *fakeFleet) Hosts(context.Context, uuid.UUID, fleet.HostFilter, fleet.Visible) ([]fleet.HostView, error) {
	return f.hosts, nil
}

func hv(fqdn, hostname string, tags []string, ips ...string) fleet.HostView {
	return fleet.HostView{ID: uuid.New(), Name: hostname, Hostname: hostname, FQDN: fqdn, IPs: ips, Tags: tags}
}

type env struct {
	ctx   context.Context
	q     *dbgen.Queries
	ws    uuid.UUID
	inv   *inventory.Service
	rules *grouprules.Service
	fleet *fakeFleet
	prj   *projects.Service
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
	prj := projects.New(projects.Options{Store: q, Git: &gitops.Client{AllowFileTransport: true}, DataDir: t.TempDir()})
	inv := inventory.New(inventory.Options{DB: pool, Checkouts: prj, Writer: prj})
	fl := &fakeFleet{}
	return &env{ctx: ctx, q: q, ws: ws.ID, inv: inv, fleet: fl, prj: prj,
		rules: grouprules.New(grouprules.Options{DB: pool, Fleet: fl, Inventories: inv})}
}

func (e *env) dbInventory(t *testing.T, name string) inventory.Inventory {
	t.Helper()
	inv, err := e.inv.Create(e.ctx, e.ws, inventory.CreateInput{Name: name, Mode: inventory.ModeDB})
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func (e *env) rule(t *testing.T, inv uuid.UUID, mode string) grouprules.Rule {
	t.Helper()
	r, err := e.rules.Create(e.ctx, e.ws, grouprules.Input{Name: "r-" + uuid.NewString()[:6], Kind: grouprules.KindSubnet,
		Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`), InventoryID: inv, Group: "lan", Mode: mode}, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) members(t *testing.T, inv uuid.UUID, group string) []string {
	t.Helper()
	st, err := e.inv.GroupState(e.ctx, e.ws, inv, group)
	if err != nil {
		t.Fatal(err)
	}
	return st.Members
}

func jsonEq(a json.RawMessage, b string) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

var author = projects.Author{Name: "Ann Admin", Email: "ann@example.com"}

func TestRuleCRUDValidation(t *testing.T) {
	e := setup(t)
	inv := e.dbInventory(t, "lab")
	base := grouprules.Input{Name: "n", Kind: grouprules.KindProxmoxTag, Params: json.RawMessage(`{"tag":"Web"}`), InventoryID: inv.ID, Group: "web"}
	r, err := e.rules.Create(e.ctx, e.ws, base, uuid.Nil)
	if err != nil || r.Mode != grouprules.ModeAddOnly || !r.Enabled || !jsonEq(r.Params, `{"tag":"web"}`) {
		t.Fatalf("create: %+v %v", r, err)
	}
	if _, err := e.rules.Create(e.ctx, e.ws, base, uuid.Nil); !errors.Is(err, grouprules.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	bad := []grouprules.Input{
		{Name: "", Kind: grouprules.KindSubnet, Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`), InventoryID: inv.ID, Group: "g"},
		{Name: "x", Kind: "nope", InventoryID: inv.ID, Group: "g"},
		{Name: "x", Kind: grouprules.KindSubnet, Params: json.RawMessage(`{"cidr":"bad"}`), InventoryID: inv.ID, Group: "g"},
		{Name: "x", Kind: grouprules.KindSubnet, Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`), InventoryID: inv.ID, Group: "all"},
		{Name: "x", Kind: grouprules.KindSubnet, Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`), InventoryID: inv.ID, Group: "g", Mode: "magic"},
		{Name: "x", Kind: grouprules.KindSubnet, Params: json.RawMessage(`{"cidr":"192.0.2.0/24"}`), InventoryID: uuid.New(), Group: "g"},
	}
	for i, in := range bad {
		if _, err := e.rules.Create(e.ctx, e.ws, in, uuid.Nil); !errors.Is(err, grouprules.ErrInvalid) {
			t.Errorf("bad[%d]: %v", i, err)
		}
	}
	name := "renamed"
	off := false
	u, err := e.rules.Update(e.ctx, e.ws, r.ID, grouprules.Update{Name: &name, Enabled: &off})
	if err != nil || u.Name != "renamed" || u.Enabled {
		t.Fatalf("update: %+v %v", u, err)
	}
	if list, _ := e.rules.List(e.ctx, e.ws); len(list) != 1 {
		t.Errorf("list: %d", len(list))
	}
	if err := e.rules.Delete(e.ctx, e.ws, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rules.Get(e.ctx, e.ws, r.ID); !errors.Is(err, grouprules.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestApplyAddOnlyDBInventory(t *testing.T) {
	e := setup(t)
	inv := e.dbInventory(t, "lab")
	// a pre-existing, manually created member and host
	if _, _, err := e.inv.CreateHost(e.ctx, e.ws, inv.ID, inventory.HostInput{Name: "manual.lan"}, nil); err != nil {
		t.Fatal(err)
	}
	inv, _ = e.inv.Get(e.ctx, e.ws, inv.ID)
	e.fleet.hosts = []fleet.HostView{
		hv("a.lan", "a", nil, "192.0.2.1"),
		hv("", "short", nil, "192.0.2.2"),
		hv("far.lan", "far", nil, "198.51.100.1"),
		hv("manual.lan", "manual", nil, "192.0.2.9"),
	}
	r := e.rule(t, inv.ID, grouprules.ModeAddOnly)

	pv, err := e.rules.Preview(e.ctx, e.ws, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Plan.GroupCreate || pv.Plan.Matched != 3 || len(pv.Plan.Add) != 3 || len(pv.Plan.Remove) != 0 || pv.InventoryMode != "db" {
		t.Fatalf("preview: %+v", pv.Plan)
	}
	if got := e.members(t, inv.ID, "lan"); len(got) != 0 {
		t.Fatalf("preview changed the inventory: %v", got)
	}
	cur, _ := e.inv.Get(e.ctx, e.ws, inv.ID)
	if cur.Version != inv.Version {
		t.Fatal("preview bumped the version")
	}

	ap, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{Author: author})
	if err != nil {
		t.Fatal(err)
	}
	if !ap.Result.GroupCreated || !reflect.DeepEqual(ap.Result.HostsCreated, []string{"a.lan", "short"}) || len(ap.Result.Added) != 3 {
		t.Fatalf("result: %+v", ap.Result)
	}
	if got := e.members(t, inv.ID, "lan"); !reflect.DeepEqual(got, []string{"a.lan", "manual.lan", "short"}) {
		t.Fatalf("members: %v", got)
	}
	hosts, _ := e.inv.ListHosts(e.ctx, e.ws, inv.ID)
	for _, h := range hosts {
		if h.Name == "short" && !strings.Contains(h.VarsYAML, "192.0.2.2") {
			t.Errorf("short lacks ansible_host: %q", h.VarsYAML)
		}
		if h.Name == "a.lan" && strings.Contains(h.VarsYAML, "ansible_host") {
			t.Errorf("fqdn host got ansible_host: %q", h.VarsYAML)
		}
	}
	if after, _ := e.inv.Get(e.ctx, e.ws, inv.ID); after.Version != inv.Version+1 {
		t.Errorf("version %d, want %d", after.Version, inv.Version+1)
	}

	// idempotent: second apply is a no-op without a version bump
	ap, err = e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{Author: author})
	if err != nil || ap.Result.Changed() || len(ap.Plan.Add) != 0 || ap.Plan.Keep != 3 {
		t.Fatalf("second apply: %+v %v", ap, err)
	}
	if after, _ := e.inv.Get(e.ctx, e.ws, inv.ID); after.Version != inv.Version+1 {
		t.Error("no-op apply bumped the version")
	}

	// add-only never removes, even when hosts stop matching
	e.fleet.hosts = nil
	if _, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{Author: author}); err != nil {
		t.Fatal(err)
	}
	if got := e.members(t, inv.ID, "lan"); len(got) != 3 {
		t.Errorf("add-only removed members: %v", got)
	}
}

func TestApplySyncRemovesOnlyRuleAddedMembers(t *testing.T) {
	e := setup(t)
	inv := e.dbInventory(t, "lab")
	grp, _, err := e.inv.CreateGroup(e.ctx, e.ws, inv.ID, inventory.GroupInput{Name: "lan"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// manual.lan is a manual member of the group and also matches the rule
	if _, _, err := e.inv.CreateHost(e.ctx, e.ws, inv.ID, inventory.HostInput{Name: "manual.lan", GroupIDs: []uuid.UUID{grp.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.inv.CreateHost(e.ctx, e.ws, inv.ID, inventory.HostInput{Name: "hand.lan", GroupIDs: []uuid.UUID{grp.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	e.fleet.hosts = []fleet.HostView{
		hv("a.lan", "a", nil, "192.0.2.1"),
		hv("b.lan", "b", nil, "192.0.2.2"),
		hv("manual.lan", "manual", nil, "192.0.2.3"),
	}
	r := e.rule(t, inv.ID, grouprules.ModeSync)
	if _, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{Author: author}); err != nil {
		t.Fatal(err)
	}
	if got := e.members(t, inv.ID, "lan"); !reflect.DeepEqual(got, []string{"a.lan", "b.lan", "hand.lan", "manual.lan"}) {
		t.Fatalf("after first apply: %v", got)
	}

	// b and manual stop matching, a new host c appears
	e.fleet.hosts = []fleet.HostView{hv("a.lan", "a", nil, "192.0.2.1"), hv("c.lan", "c", nil, "192.0.2.4")}
	pv, err := e.rules.Preview(e.ctx, e.ws, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pv.Plan.Remove, []string{"b.lan"}) || len(pv.Plan.Add) != 1 || pv.Plan.Add[0].Name != "c.lan" {
		t.Fatalf("preview: %+v", pv.Plan)
	}
	ap, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{Author: author})
	if err != nil || !reflect.DeepEqual(ap.Result.Removed, []string{"b.lan"}) {
		t.Fatalf("apply: %+v %v", ap, err)
	}
	if got := e.members(t, inv.ID, "lan"); !reflect.DeepEqual(got, []string{"a.lan", "c.lan", "hand.lan", "manual.lan"}) {
		t.Fatalf("manual memberships must stay: %v", got)
	}
	// the removed host itself remains in the inventory
	if hosts, _ := e.inv.ListHosts(e.ctx, e.ws, inv.ID); !slices.ContainsFunc(hosts, func(h inventory.HostView) bool { return h.Name == "b.lan" }) {
		t.Error("sync deleted the host instead of the membership")
	}

	// a tracked host removed manually is forgotten, not re-removed or errored
	hosts, _ := e.inv.ListHosts(e.ctx, e.ws, inv.ID)
	for _, h := range hosts {
		if h.Name == "c.lan" {
			grpID := grp.ID
			if _, err := e.inv.RemoveMember(e.ctx, e.ws, inv.ID, grpID, h.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.fleet.hosts = nil
	pv, _ = e.rules.Preview(e.ctx, e.ws, r.ID)
	if !reflect.DeepEqual(pv.Plan.Untrack, []string{"c.lan"}) || !reflect.DeepEqual(pv.Plan.Remove, []string{"a.lan"}) {
		t.Fatalf("stale tracking: %+v", pv.Plan)
	}

	// retargeting forgets the tracking: nothing is removed from the new group
	g2 := "other"
	if _, err := e.rules.Update(e.ctx, e.ws, r.ID, grouprules.Update{Group: &g2}); err != nil {
		t.Fatal(err)
	}
	tracked, _ := e.q.ListGroupRuleMembers(e.ctx, r.ID)
	if len(tracked) != 0 {
		t.Errorf("tracking survived retarget: %v", tracked)
	}
}

func TestApplyDisabledAndChangedRule(t *testing.T) {
	e := setup(t)
	inv := e.dbInventory(t, "lab")
	e.fleet.hosts = []fleet.HostView{hv("a.lan", "a", nil, "192.0.2.1")}
	r := e.rule(t, inv.ID, grouprules.ModeAddOnly)
	off := false
	if _, err := e.rules.Update(e.ctx, e.ws, r.ID, grouprules.Update{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{}); !errors.Is(err, grouprules.ErrDisabled) {
		t.Errorf("disabled apply: %v", err)
	}
	// preview still works for a disabled rule
	if pv, err := e.rules.Preview(e.ctx, e.ws, r.ID); err != nil || len(pv.Plan.Add) != 1 {
		t.Errorf("preview of disabled rule: %+v %v", pv.Plan, err)
	}
	// stale If-Match is rejected before anything is written
	one := inv.Version + 5
	on := true
	_, _ = e.rules.Update(e.ctx, e.ws, r.ID, grouprules.Update{Enabled: &on})
	if _, err := e.rules.Apply(e.ctx, e.ws, r.ID, grouprules.ApplyInput{IfMatch: &one}); !errors.Is(err, inventory.ErrPrecondition) {
		t.Errorf("stale If-Match: %v", err)
	}
	if got := e.members(t, inv.ID, "lan"); len(got) != 0 {
		t.Errorf("rejected apply wrote: %v", got)
	}
}

// --- Git-synced inventories ---

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // test helper
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type gitEnv struct {
	*env
	bare, seed string
	gi         inventory.Inventory
}

func setupGit(t *testing.T) *gitEnv {
	t.Helper()
	e := setup(t)
	root := t.TempDir()
	bare, seed := filepath.Join(root, "bare.git"), filepath.Join(root, "seed")
	gitCmd(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitCmd(t, root, "init", "-q", "-b", "main", seed)
	if err := os.MkdirAll(filepath.Join(seed, "inv"), 0o750); err != nil {
		t.Fatal(err)
	}
	content := "all:\n  children:\n    web:\n      hosts:\n        web1.lan: {}\n"
	if err := os.WriteFile(filepath.Join(seed, "inv", "hosts.yml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, seed, "add", ".")
	gitCmd(t, seed, "commit", "-q", "-m", "init")
	gitCmd(t, seed, "push", "-q", bare, "main")
	p, err := e.prj.Create(e.ctx, e.ws, projects.CreateInput{Name: "p", URL: "file://" + bare})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.prj.Sync(e.ctx, e.ws, p.ID); err != nil {
		t.Fatal(err)
	}
	inv, err := e.inv.Create(e.ctx, e.ws, inventory.CreateInput{Name: "Lab", Mode: inventory.ModeGit, ProjectID: &p.ID, Path: "inv/hosts.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.inv.Import(e.ctx, e.ws, inv.ID, inventory.ImportInput{}, nil); err != nil {
		t.Fatal(err)
	}
	inv, _ = e.inv.Get(e.ctx, e.ws, inv.ID)
	return &gitEnv{env: e, bare: bare, seed: seed, gi: inv}
}

func TestApplyGitInventoryWritesBackToWriteBranch(t *testing.T) {
	g := setupGit(t)
	g.fleet.hosts = []fleet.HostView{hv("a.lan", "a", nil, "192.0.2.1"), hv("", "short", nil, "192.0.2.2")}
	r := g.rule(t, g.gi.ID, grouprules.ModeSync)

	ap, err := g.rules.Apply(g.ctx, g.ws, r.ID, grouprules.ApplyInput{User: uuid.Nil, Author: author})
	if err != nil {
		t.Fatal(err)
	}
	if ap.InventoryMode != "git" || ap.Result.Commit == nil || ap.Result.Commit.SHA == "" || ap.Result.Commit.Branch != "acp/lab" {
		t.Fatalf("commit result: %+v", ap.Result.Commit)
	}
	file := gitCmd(t, g.bare, "show", "acp/lab:inv/hosts.yml")
	for _, want := range []string{"lan:", "a.lan", "short", "192.0.2.2", "web1.lan"} {
		if !strings.Contains(file, want) {
			t.Errorf("committed file lacks %q:\n%s", want, file)
		}
	}
	if msg := gitCmd(t, g.bare, "log", "-1", "--format=%an <%ae>%n%B", "acp/lab"); !strings.Contains(msg, "Ann Admin <ann@example.com>") || !strings.Contains(msg, "Signed-off-by") {
		t.Errorf("commit metadata: %s", msg)
	}
	if got := gitCmd(t, g.bare, "rev-parse", "main"); got == ap.Result.Commit.SHA {
		t.Error("main must not move")
	}
	after, _ := g.inv.Get(g.ctx, g.ws, g.gi.ID)
	if after.BaseSHA != ap.Result.Commit.SHA {
		t.Errorf("base sha %s, want %s", after.BaseSHA, ap.Result.Commit.SHA)
	}
	// a second apply changes nothing and makes no commit
	ap2, err := g.rules.Apply(g.ctx, g.ws, r.ID, grouprules.ApplyInput{Author: author})
	if err != nil || ap2.Result.Changed() || ap2.Result.Commit != nil {
		t.Fatalf("no-op apply: %+v %v", ap2.Result, err)
	}
}

func TestApplyGitConflictRollsBackEverything(t *testing.T) {
	g := setupGit(t)
	g.fleet.hosts = []fleet.HostView{hv("a.lan", "a", nil, "192.0.2.1")}
	r := g.rule(t, g.gi.ID, grouprules.ModeSync)
	if _, err := g.rules.Apply(g.ctx, g.ws, r.ID, grouprules.ApplyInput{Author: author}); err != nil {
		t.Fatal(err)
	}
	before, _ := g.inv.Get(g.ctx, g.ws, g.gi.ID)

	// someone changes the inventory file on the write branch
	gitCmd(t, g.seed, "fetch", "-q", g.bare, "acp/lab")
	gitCmd(t, g.seed, "checkout", "-q", "-B", "acp/lab", "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(g.seed, "inv", "hosts.yml"), []byte("all:\n  hosts:\n    other: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, g.seed, "commit", "-qam", "concurrent edit")
	gitCmd(t, g.seed, "push", "-q", g.bare, "acp/lab")
	head := gitCmd(t, g.bare, "rev-parse", "acp/lab")

	g.fleet.hosts = []fleet.HostView{hv("c.lan", "c", nil, "192.0.2.5")}
	_, err := g.rules.Apply(g.ctx, g.ws, r.ID, grouprules.ApplyInput{Author: author})
	if !errors.Is(err, projects.ErrGitConflict) {
		t.Fatalf("want git conflict, got %v", err)
	}
	if got := gitCmd(t, g.bare, "rev-parse", "acp/lab"); got != head {
		t.Error("write branch moved despite the conflict")
	}
	after, _ := g.inv.Get(g.ctx, g.ws, g.gi.ID)
	if after.Version != before.Version || after.BaseSHA != before.BaseSHA {
		t.Errorf("inventory changed: version %d->%d", before.Version, after.Version)
	}
	if got := g.members(t, g.gi.ID, "lan"); !reflect.DeepEqual(got, []string{"a.lan"}) {
		t.Errorf("database membership changed: %v", got)
	}
	tracked, _ := g.q.ListGroupRuleMembers(g.ctx, r.ID)
	if !reflect.DeepEqual(tracked, []string{"a.lan"}) {
		t.Errorf("tracking changed: %v", tracked)
	}
}
