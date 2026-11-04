package inventory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

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

// wbEnv wires the inventory service to a real projects service and a local bare repo.
type wbEnv struct {
	*env
	prj        *projects.Service
	bare, seed string
	pid        uuid.UUID
	dataDir    string
}

func setupWB(t *testing.T, files map[string]string) *wbEnv {
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

	root := t.TempDir()
	bare, seed := filepath.Join(root, "bare.git"), filepath.Join(root, "seed")
	gitCmd(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitCmd(t, root, "init", "-q", "-b", "main", seed)
	for name, content := range files {
		p := filepath.Join(seed, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, seed, "add", ".")
	gitCmd(t, seed, "commit", "-q", "-m", "init")
	gitCmd(t, seed, "push", "-q", bare, "main")

	dataDir := t.TempDir()
	prj := projects.New(projects.Options{Store: q, Git: &gitops.Client{AllowFileTransport: true}, DataDir: dataDir})
	p, err := prj.Create(ctx, ws.ID, projects.CreateInput{Name: "p", URL: "file://" + bare})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prj.Sync(ctx, ws.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	e := &env{svc: New(Options{DB: pool, Checkouts: prj, Writer: prj}), q: q, ws: ws.ID, project: p.ID}
	return &wbEnv{env: e, prj: prj, bare: bare, seed: seed, pid: p.ID, dataDir: dataDir}
}

func (w *wbEnv) gitInv(t *testing.T, path string) Inventory {
	t.Helper()
	ctx := context.Background()
	inv, err := w.svc.Create(ctx, w.ws, CreateInput{Name: "Lab", Mode: ModeGit, ProjectID: &w.pid, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Import(ctx, w.ws, inv.ID, ImportInput{}, nil); err != nil {
		t.Fatal(err)
	}
	inv, err = w.svc.Get(ctx, w.ws, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

const wbHosts = `all:
  children:
    web:
      hosts:
        web1: {}
        web2:
          ansible_port: 2222
      vars:
        http_port: 80
`

var wbAuthor = projects.Author{Name: "Ann Admin", Email: "ann@example.com"}

func TestCommitYAMLWithVarsFiles(t *testing.T) {
	w := setupWB(t, map[string]string{
		"inv/hosts.yml":          wbHosts,
		"inv/group_vars/web.yml": "ntp: pool.example.org\nsecret_pw: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3132333435\n",
		"inv/host_vars/web1.yml": "role: primary\n",
	})
	ctx := context.Background()
	inv := w.gitInv(t, "inv/hosts.yml")

	// untouched import: nothing to write
	d, err := w.svc.Diff(ctx, w.ws, inv.ID)
	if err != nil || d.Changed || d.Diff != "" {
		t.Fatalf("diff of unchanged inventory: %+v %v", d, err)
	}
	if r, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil); err != nil || !r.NoOp {
		t.Fatalf("noop commit: %+v %v", r, err)
	}
	if out := gitCmd(t, w.bare, "branch", "--list", "acp/*"); out != "" {
		t.Fatalf("no-op created a branch: %q", out)
	}

	// edit: new inline var on a group, change a file-backed var, add a host
	groups, _ := w.svc.ListGroups(ctx, w.ws, inv.ID)
	hosts, _ := w.svc.ListHosts(ctx, w.ws, inv.ID)
	var web GroupView
	for _, g := range groups {
		if g.Name == "web" {
			web = g
		}
	}
	if !strings.Contains(web.VarsYAML, "!vault") {
		t.Fatalf("vault value lost on import: %q", web.VarsYAML)
	}
	vars := strings.Replace(web.VarsYAML, "pool.example.org", "time.example.org", 1) + "extra: 1\n"
	if _, _, err := w.svc.UpdateGroup(ctx, w.ws, inv.ID, web.ID, GroupUpdate{VarsYAML: &vars}, nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if h.Name == "web1" {
			v := "role: secondary\n"
			if _, _, err := w.svc.UpdateHost(ctx, w.ws, inv.ID, h.ID, HostUpdate{VarsYAML: &v}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := w.svc.CreateHost(ctx, w.ws, inv.ID, HostInput{Name: "web3", GroupIDs: []uuid.UUID{web.ID}}, nil); err != nil {
		t.Fatal(err)
	}

	d, err = w.svc.Diff(ctx, w.ws, inv.ID)
	if err != nil || !d.Changed || d.Branch != "acp/lab" {
		t.Fatalf("diff: %+v %v", d, err)
	}
	if !strings.Contains(d.Diff, "web3") ||
		!strings.Contains(d.Diff, "time.example.org") || !strings.Contains(d.Diff, "$ANSIBLE_VAULT;1.1;AES256") {
		t.Errorf("diff misses expected content:\n%s", d.Diff)
	}
	if out := gitCmd(t, w.bare, "branch", "--list", "acp/*"); out != "" {
		t.Fatalf("diff created a branch: %q", out)
	}

	inv, _ = w.svc.Get(ctx, w.ws, inv.ID)
	res, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Message: "Add web3", Author: wbAuthor}, ptr(inv.Version))
	if err != nil {
		t.Fatal(err)
	}
	if res.NoOp || !res.Created || res.Branch != "acp/lab" || res.Inventory.BaseSHA != res.SHA || res.Inventory.Version != inv.Version {
		t.Fatalf("commit result: %+v", res)
	}
	if want := []string{"inv/group_vars/web.yml", "inv/host_vars/web1.yml", "inv/hosts.yml"}; strings.Join(res.Paths, ",") != strings.Join(want, ",") {
		t.Errorf("paths %v want %v", res.Paths, want)
	}
	show := func(p string) string { return gitCmd(t, w.bare, "show", "acp/lab:"+p) }
	if got := show("inv/group_vars/web.yml"); !strings.Contains(got, "time.example.org") || !strings.Contains(got, "extra: 1") || !strings.Contains(got, "!vault") {
		t.Errorf("group_vars/web.yml:\n%s", got)
	}
	if got := show("inv/host_vars/web1.yml"); !strings.Contains(got, "role: secondary") {
		t.Errorf("host_vars/web1.yml:\n%s", got)
	}
	if got := show("inv/hosts.yml"); !strings.Contains(got, "web3") {
		t.Errorf("hosts.yml:\n%s", got)
	}
	meta := gitCmd(t, w.bare, "log", "-1", "--format=%an <%ae>|%B", "acp/lab")
	if !strings.HasPrefix(meta, "Ann Admin <ann@example.com>|Add web3") || !strings.Contains(meta, "Signed-off-by: Ann Admin <ann@example.com>") {
		t.Errorf("commit metadata %q", meta)
	}
	// the cache is back on main and the next diff/commit is a no-op
	if got := gitCmd(t, filepath.Join(w.prjCache(), w.pid.String()), "rev-parse", "HEAD"); got != gitCmd(t, w.bare, "rev-parse", "main") {
		t.Errorf("cache checkout at %s", got)
	}
	if r, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil); err != nil || !r.NoOp {
		t.Fatalf("second commit: %+v %v", r, err)
	}
	// round trip: importing the branch state yields the same model
	gitCmd(t, w.seed, "fetch", "-q", w.bare, "acp/lab")
	gitCmd(t, w.seed, "checkout", "-q", "-B", "main", "FETCH_HEAD")
	gitCmd(t, w.seed, "push", "-q", "-f", w.bare, "main")
	if _, err := w.prj.Sync(ctx, w.ws, w.pid); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Import(ctx, w.ws, inv.ID, ImportInput{}, nil); err != nil {
		t.Fatal(err)
	}
	if d, err := w.svc.Diff(ctx, w.ws, inv.ID); err != nil || d.Changed {
		t.Fatalf("diff after round trip: %+v %v", d, err)
	}
}

// prjCache is the clone directory root: <DataDir>/projects/<id>.
func (w *wbEnv) prjCache() string { return filepath.Join(w.dataDir, "projects") }

func TestCommitConflicts(t *testing.T) {
	w := setupWB(t, map[string]string{"hosts.yml": wbHosts})
	ctx := context.Background()
	inv := w.gitInv(t, "hosts.yml")
	if _, _, err := w.svc.CreateHost(ctx, w.ws, inv.ID, HostInput{Name: "web9"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil); err != nil {
		t.Fatal(err)
	}
	cur, _ := w.svc.Get(ctx, w.ws, inv.ID)

	// someone edits the inventory file on the write branch
	gitCmd(t, w.seed, "fetch", "-q", w.bare, "acp/lab")
	gitCmd(t, w.seed, "checkout", "-q", "-B", "other", "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(w.seed, "hosts.yml"), []byte(wbHosts+"# manual\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, w.seed, "commit", "-qam", "manual")
	gitCmd(t, w.seed, "push", "-q", w.bare, "other:acp/lab")

	if _, _, err := w.svc.CreateHost(ctx, w.ws, inv.ID, HostInput{Name: "web10"}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil)
	var ce *projects.ConflictError
	if !errors.As(err, &ce) || len(ce.Paths) != 1 || ce.Paths[0] != "hosts.yml" {
		t.Fatalf("want conflict on hosts.yml, got %v", err)
	}
	after, _ := w.svc.Get(ctx, w.ws, inv.ID)
	if after.BaseSHA != cur.BaseSHA {
		t.Errorf("base revision changed on conflict: %s -> %s", cur.BaseSHA, after.BaseSHA)
	}
	if _, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, ptr(1)); !errors.Is(err, ErrPrecondition) {
		t.Errorf("stale version: %v", err)
	}
}

func TestCommitINI(t *testing.T) {
	w := setupWB(t, map[string]string{"hosts.ini": "[web]\nweb1 ansible_port=22\n"})
	ctx := context.Background()
	inv := w.gitInv(t, "hosts.ini")
	if inv.SourceFormat != "ini" {
		t.Fatalf("format %q", inv.SourceFormat)
	}
	groups, _ := w.svc.ListGroups(ctx, w.ws, inv.ID)
	vars := "pw: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3132333435\n"
	if _, _, err := w.svc.UpdateGroup(ctx, w.ws, inv.ID, groups[0].ID, GroupUpdate{VarsYAML: &vars}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil); !errors.Is(err, ErrVaultInINI) {
		t.Fatalf("commit: want ErrVaultInINI, got %v", err)
	}
	if _, err := w.svc.Diff(ctx, w.ws, inv.ID); !errors.Is(err, ErrVaultInINI) {
		t.Fatalf("diff: want ErrVaultInINI, got %v", err)
	}
	plain := "x: 1\n"
	if _, _, err := w.svc.UpdateGroup(ctx, w.ws, inv.ID, groups[0].ID, GroupUpdate{VarsYAML: &plain}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: wbAuthor}, nil); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, w.bare, "show", "acp/lab:hosts.ini"); !strings.Contains(got, "[web:vars]") || !strings.Contains(got, "x=1") {
		t.Errorf("hosts.ini:\n%s", got)
	}
}

func TestCommitRejectsOtherInventories(t *testing.T) {
	w := setupWB(t, map[string]string{"hosts.yml": wbHosts})
	ctx := context.Background()
	dbInv := w.newInv(t, "DB only")
	if _, err := w.svc.Commit(ctx, w.ws, dbInv.ID, CommitInput{Author: wbAuthor}, nil); !errors.Is(err, ErrNotGitInventory) {
		t.Errorf("db inventory: %v", err)
	}
	gi, err := w.svc.Create(ctx, w.ws, CreateInput{Name: "Fresh", Mode: ModeGit, ProjectID: &w.pid, Path: "hosts.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Diff(ctx, w.ws, gi.ID); !errors.Is(err, ErrNotImported) {
		t.Errorf("not imported: %v", err)
	}
	inv := w.gitInv(t, "hosts.yml")
	if _, err := w.svc.Commit(ctx, w.ws, inv.ID, CommitInput{Author: projects.Author{Name: "x"}}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing email: %v", err)
	}
}
