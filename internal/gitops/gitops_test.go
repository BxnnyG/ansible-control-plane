package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sh(t *testing.T, dir string, args ...string) string {
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

// newRemote creates a bare repo seeded with one commit on main.
func newRemote(t *testing.T) (bare string, r Remote) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "bare.git")
	sh(t, root, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(root, "seed")
	sh(t, root, "init", "-q", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "a.yml"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sh(t, seed, "add", ".")
	sh(t, seed, "commit", "-q", "-m", "init")
	sh(t, seed, "push", "-q", bare, "main")
	return bare, Remote{URL: "file://" + bare, Auth: None()}
}

func testClient() *Client { return &Client{AllowFileTransport: true} }

func TestSyncAndLsRemote(t *testing.T) {
	bare, r := newRemote(t)
	c := testClient()
	ctx := context.Background()
	if err := c.LsRemote(ctx, r); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "work")
	sha, err := c.Sync(ctx, dir, r, "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := sh(t, bare, "rev-parse", "main"); sha != want {
		t.Fatalf("sha %s want %s", sha, want)
	}
	// local dirt is discarded
	_ = os.WriteFile(filepath.Join(dir, "a.yml"), []byte("dirty"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "junk"), []byte("x"), 0o600)
	if _, err := c.Sync(ctx, dir, r, "main"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.yml")); string(b) != "a\n" { //nolint:gosec // test path
		t.Fatalf("not reset: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Fatal("untracked file not cleaned")
	}
}

func TestCommitPushAndRejection(t *testing.T) {
	_, r := newRemote(t)
	c := testClient()
	ctx := context.Background()
	d1 := filepath.Join(t.TempDir(), "d1")
	d2 := filepath.Join(t.TempDir(), "d2")
	for _, d := range []string{d1, d2} {
		if _, err := c.Sync(ctx, d, r, "main"); err != nil {
			t.Fatal(err)
		}
		if err := c.CreateOrCheckoutBranch(ctx, d, "main"); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(d, content string) {
		t.Helper()
		_ = os.WriteFile(filepath.Join(d, "b.yml"), []byte(content), 0o600)
		sha, err := c.Commit(ctx, d, []string{"b.yml"}, "Bob", "bob@example.com", "add b", true)
		if err != nil || sha == "" {
			t.Fatalf("commit: %v", err)
		}
	}
	commit(d1, "one")
	if err := c.Push(ctx, d1, r, "main"); err != nil {
		t.Fatal(err)
	}
	if msg := sh(t, d1, "log", "-1", "--format=%B"); !strings.Contains(msg, "Signed-off-by: Bob") {
		t.Fatalf("no signoff: %q", msg)
	}
	commit(d2, "two")
	if err := c.Push(ctx, d2, r, "main"); !errors.Is(err, ErrNonFastForward) {
		t.Fatalf("want ErrNonFastForward, got %v", err)
	}
	if _, err := c.Commit(ctx, d1, []string{"b.yml"}, "Bob", "bob@example.com", "again", false); !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("want ErrNothingToCommit, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	c := &Client{}
	for _, u := range []string{"", "-oProxyCommand=x", "ext::sh -c id", "file::x", "file:///tmp/x", "/tmp/x", "ftp://h/x", "https://h/x y"} {
		if err := c.validateURL(u); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("url %q accepted", u)
		}
	}
	for _, u := range []string{"https://example.com/a.git", "ssh://git@example.com:22/a.git", "git@example.com:org/a.git"} {
		if err := c.validateURL(u); err != nil {
			t.Errorf("url %q rejected: %v", u, err)
		}
	}
	ctx := context.Background()
	for _, ref := range []string{"", "-x", "a..b", "a b", "a~1", "x.lock"} {
		if err := c.validateRef(ctx, ref); err == nil {
			t.Errorf("ref %q accepted", ref)
		}
	}
	for _, ref := range []string{"main", "feat/x", "v1.0.0", "HEAD"} {
		if err := c.validateRef(ctx, ref); err != nil {
			t.Errorf("ref %q rejected: %v", ref, err)
		}
	}
	for _, p := range [][]string{nil, {"../x"}, {"/etc/passwd"}, {"-A"}, {""}} {
		if validatePaths(p) == nil {
			t.Errorf("paths %v accepted", p)
		}
	}
}

func TestRedact(t *testing.T) {
	in := "fatal: unable to access 'https://user:s3cret@example.com/r.git/': Authorization: Basic dTpzM2NyZXQ= token tok123"
	got := redact(in, "tok123", "dTpzM2NyZXQ=")
	for _, bad := range []string{"s3cret", "tok123", "dTpzM2NyZXQ="} {
		if strings.Contains(got, bad) {
			t.Errorf("%q leaked in %q", bad, got)
		}
	}
}

func TestPrepareCredentials(t *testing.T) {
	base := t.TempDir()
	c := &Client{TempBase: base}
	inv, err := c.prepare(&Remote{URL: "ssh://h/x", Auth: SSHKey([]byte("KEY"), []byte("h ssh-ed25519 AAAA"))})
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(inv.env, "\n")
	for _, want := range []string{"IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s", want)
		}
	}
	keys, _ := filepath.Glob(filepath.Join(base, "acp-git-*", "id"))
	if len(keys) != 1 {
		t.Fatal("key file missing")
	}
	if st, _ := os.Stat(keys[0]); st.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", st.Mode().Perm())
	}
	inv.cleanup()
	if left, _ := os.ReadDir(base); len(left) != 0 {
		t.Error("temp dir not removed")
	}

	inv, err = c.prepare(&Remote{URL: "https://h/x", Auth: HTTPS("u", "tok")})
	if err != nil {
		t.Fatal(err)
	}
	defer inv.cleanup()
	if !strings.Contains(strings.Join(inv.env, "\n"), "=http.extraHeader") {
		t.Error("extraHeader not set")
	}
}

func TestParseKeyscan(t *testing.T) {
	keys := parseKeyscan("# h:22 SSH-2.0\nh ssh-ed25519 aGVsbG8=\n\nbad\n")
	if len(keys) != 1 || keys[0].Type != "ssh-ed25519" || !strings.HasPrefix(keys[0].Fingerprint, "SHA256:") {
		t.Fatalf("unexpected %+v", keys)
	}
}

func TestWritebackPrimitives(t *testing.T) {
	bare, r := newRemote(t)
	c := testClient()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "work")
	base, err := c.Sync(ctx, dir, r, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := c.FetchBranch(ctx, dir, r, "acp/x"); err != nil || found {
		t.Fatalf("missing branch: found=%v err=%v", found, err)
	}
	if err := c.CheckoutDetached(ctx, dir, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.yml"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new*.yml"), []byte("n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := c.StagedDiff(ctx, dir, []string{"a.yml", "new*.yml"})
	if err != nil || !strings.Contains(diff, "+b") || !strings.Contains(diff, "new*.yml") {
		t.Fatalf("diff %q err %v", diff, err)
	}
	if _, err := c.Commit(ctx, dir, []string{"a.yml"}, "Ann", "ann@example.com", "edit", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(ctx, dir, r, "acp/x"); err != nil {
		t.Fatal(err)
	}
	head, found, err := c.FetchBranch(ctx, dir, r, "acp/x")
	if err != nil || !found || head != sh(t, bare, "rev-parse", "acp/x") {
		t.Fatalf("fetch branch: %s %v %v", head, found, err)
	}
	if got, err := c.ChangedPaths(ctx, dir, base, head, []string{"a.yml", "other"}); err != nil || len(got) != 1 || got[0] != "a.yml" {
		t.Fatalf("changed %v %v", got, err)
	}
	if err := c.Reset(ctx, dir, base); err != nil {
		t.Fatal(err)
	}
	if !c.HasCommit(ctx, dir, head) || c.HasCommit(ctx, dir, "deadbeefdeadbeef") {
		t.Fatal("HasCommit")
	}
	if _, err := os.Stat(filepath.Join(dir, "new*.yml")); err == nil {
		t.Fatal("untracked file survived reset")
	}
}
