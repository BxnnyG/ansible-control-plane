package projects

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func planOf(changes ...FileChange) PlanFunc {
	return func(*os.Root) (Plan, error) { return Plan{Changes: changes, Watch: []string{"a.yml"}}, nil }
}

func TestWriteBack(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "wb", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.svc.Sync(ctx, e.ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	author := Author{Name: "Ann Admin", Email: "ann@example.com"}
	req := func(content string) WriteRequest {
		return WriteRequest{Branch: "acp/inv", BaseSHA: rev.SHA, Message: "edit inventory", Author: author,
			Plan: planOf(FileChange{Path: "inv/hosts.yml", Content: []byte(content)})}
	}

	// new branch is created from the base revision, commit has author and sign-off
	res, err := e.svc.WriteBack(ctx, e.ws, p.ID, req("one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.NoOp || res.SHA == "" || len(res.Paths) != 1 {
		t.Fatalf("result %+v", res)
	}
	if got := git(t, repo.bare, "rev-parse", "acp/inv"); got != res.SHA {
		t.Fatalf("remote branch %s want %s", got, res.SHA)
	}
	if got := git(t, repo.bare, "log", "-1", "--format=%an <%ae>|%B", "acp/inv"); !strings.HasPrefix(got, "Ann Admin <ann@example.com>|edit inventory") ||
		!strings.Contains(got, "Signed-off-by: Ann Admin <ann@example.com>") {
		t.Errorf("commit metadata: %q", got)
	}
	if got := git(t, repo.bare, "rev-parse", "acp/inv~1"); got != rev.SHA {
		t.Errorf("branch parent %s want base %s", got, rev.SHA)
	}
	// main is untouched and the cache is back on the synced revision
	if got := git(t, repo.bare, "rev-parse", "main"); got != rev.SHA {
		t.Errorf("main moved: %s", got)
	}
	dir := e.svc.cacheDir(p.ID)
	if got := git(t, dir, "rev-parse", "HEAD"); got != rev.SHA {
		t.Errorf("cache at %s want %s", got, rev.SHA)
	}
	if _, err := os.Stat(filepath.Join(dir, "inv")); err == nil {
		t.Error("written file leaked into the cache checkout")
	}

	// same content again: nothing to commit
	again := req("one\n")
	again.BaseSHA = res.SHA
	if r2, err := e.svc.WriteBack(ctx, e.ws, p.ID, again); err != nil || !r2.NoOp || r2.SHA != res.SHA {
		t.Fatalf("noop: %+v %v", r2, err)
	}

	// second edit on top of the previous commit fast-forwards the branch
	next := req("two\n")
	next.BaseSHA = res.SHA
	r3, err := e.svc.WriteBack(ctx, e.ws, p.ID, next)
	if err != nil || r3.Created || r3.NoOp {
		t.Fatalf("second: %+v %v", r3, err)
	}

	// a file changed on the branch since the edited revision is a conflict
	git(t, repo.seed, "fetch", "-q", repo.bare, "acp/inv")
	git(t, repo.seed, "checkout", "-q", "-B", "work", "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(repo.seed, "inv", "hosts.yml"), []byte("someone else\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo.seed, "commit", "-qam", "manual edit")
	git(t, repo.seed, "push", "-q", repo.bare, "work:acp/inv")
	var ce *ConflictError
	stale := req("three\n")
	stale.BaseSHA = res.SHA
	if _, err := e.svc.WriteBack(ctx, e.ws, p.ID, stale); !errors.As(err, &ce) || !errors.Is(err, ErrGitConflict) ||
		len(ce.Paths) != 1 || ce.Paths[0] != "inv/hosts.yml" {
		t.Fatalf("want conflict on inv/hosts.yml, got %v", err)
	}
	// ... also when only a watched file changed
	watched := stale
	watched.Plan = planOf(FileChange{Path: "inv/other.yml", Content: []byte("x\n")})
	git(t, repo.seed, "fetch", "-q", repo.bare, "acp/inv")
	git(t, repo.seed, "checkout", "-q", "-B", "w2", "FETCH_HEAD")
	if err := os.WriteFile(filepath.Join(repo.seed, "a.yml"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo.seed, "commit", "-qam", "watched")
	git(t, repo.seed, "push", "-q", repo.bare, "w2:acp/inv")
	if _, err := e.svc.WriteBack(ctx, e.ws, p.ID, watched); !errors.As(err, &ce) || ce.Paths[0] != "a.yml" {
		t.Fatalf("want conflict on watched a.yml, got %v", err)
	}
	// an unrelated file is fine even though the branch moved
	other := stale
	other.Plan = func(*os.Root) (Plan, error) {
		return Plan{Changes: []FileChange{{Path: "inv/other.yml", Content: []byte("x\n")}}}, nil
	}
	if r, err := e.svc.WriteBack(ctx, e.ws, p.ID, other); err != nil || r.NoOp {
		t.Fatalf("unrelated file: %+v %v", r, err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != rev.SHA {
		t.Errorf("cache at %s want %s after conflicts", got, rev.SHA)
	}
}

func TestWriteBackRemoteMovesDuringPush(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "race", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.svc.Sync(ctx, e.ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// the plan runs after the fetch; moving the branch there simulates a push race
	racy := func(*os.Root) (Plan, error) {
		git(t, repo.seed, "checkout", "-q", "-B", "race", rev.SHA)
		repo.commit("other.yml") // commits on the current branch (race)
		git(t, repo.seed, "push", "-q", repo.bare, "race:refs/heads/acp/racy")
		return Plan{Changes: []FileChange{{Path: "x.yml", Content: []byte("x\n")}}}, nil
	}
	// first create the branch so that the fetch sees it
	git(t, repo.seed, "push", "-q", repo.bare, rev.SHA+":refs/heads/acp/racy")
	_, err = e.svc.WriteBack(ctx, e.ws, p.ID, WriteRequest{Branch: "acp/racy", BaseSHA: rev.SHA, Message: "m",
		Author: Author{Name: "A", Email: "a@example.com"}, Plan: racy})
	if !errors.Is(err, ErrGitConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestWriteBackRejectsEscapes(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "esc", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := e.svc.Sync(ctx, e.ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", filepath.Join(repo.seed, "link")); err != nil {
		t.Fatal(err)
	}
	git(t, repo.seed, "add", ".")
	git(t, repo.seed, "commit", "-qm", "symlink")
	git(t, repo.seed, "push", "-q", repo.bare, "main")
	rev, err = e.svc.Sync(ctx, e.ws, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../x", "/etc/x", "a/../../x", ".git/config", "link/x", "link", "a//b", ""} {
		_, err := e.svc.WriteBack(ctx, e.ws, p.ID, WriteRequest{Branch: "acp/e", BaseSHA: rev.SHA, Message: "m",
			Author: Author{Name: "A", Email: "a@example.com"}, Plan: planOf(FileChange{Path: path, Content: []byte("x")})})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("path %q: want ErrInvalid, got %v", path, err)
		}
	}
	if out := git(t, repo.bare, "branch", "--list", "acp/*"); out != "" {
		t.Errorf("branch created despite rejected paths: %q", out)
	}
}
