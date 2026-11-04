package projects

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

func git(t *testing.T, dir string, args ...string) string {
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

// remoteRepo is a bare repo with a work clone to add commits.
type remoteRepo struct {
	t          *testing.T
	bare, seed string
}

func newRepo(t *testing.T) *remoteRepo {
	t.Helper()
	root := t.TempDir()
	r := &remoteRepo{t: t, bare: filepath.Join(root, "bare.git"), seed: filepath.Join(root, "seed")}
	git(t, root, "init", "-q", "--bare", "-b", "main", r.bare)
	git(t, root, "init", "-q", "-b", "main", r.seed)
	r.commit("a.yml")
	return r
}

func (r *remoteRepo) commit(file string) string {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.seed, file), []byte(uuid.NewString()+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	git(r.t, r.seed, "add", ".")
	git(r.t, r.seed, "commit", "-q", "-m", "c "+file)
	git(r.t, r.seed, "push", "-q", r.bare, "main")
	return git(r.t, r.seed, "rev-parse", "HEAD")
}

func (r *remoteRepo) url() string { return "file://" + r.bare }

// fakeGit delegates to a real client but fakes host key scanning and can block syncs.
type fakeGit struct {
	*gitops.Client
	keys    []gitops.HostKey
	gate    chan struct{} // when set, Sync waits until it is closed
	entered chan struct{}
}

func (f *fakeGit) ScanHostKey(context.Context, string, int) ([]gitops.HostKey, error) {
	return f.keys, nil
}

func (f *fakeGit) Sync(ctx context.Context, dir string, r gitops.Remote, ref string) (string, error) {
	if f.gate != nil {
		f.entered <- struct{}{}
		<-f.gate
	}
	return f.Client.Sync(ctx, dir, r, ref)
}

func fakeKey(seed string) gitops.HostKey {
	blob := []byte("blob-" + seed)
	sum := sha256.Sum256(blob)
	return gitops.HostKey{Type: "ssh-ed25519",
		KnownHosts:  "git.example.org ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob),
		Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])}
}

func genSSHKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

type env struct {
	svc  *Service
	git  *fakeGit
	sec  *secrets.Service
	q    *dbgen.Queries
	ws   uuid.UUID
	data string
	p    *pgxpool.Pool
}

func setup(t *testing.T, scanner Scanner) *env {
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
	prov, err := secrets.NewLocalProvider([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewService(q, prov)
	g := &fakeGit{Client: &gitops.Client{AllowFileTransport: true}}
	data := t.TempDir()
	return &env{svc: New(Options{Store: q, Secrets: sec, Git: g, Scanner: scanner, DataDir: data}), git: g, sec: sec, q: q, ws: ws.ID, data: data, p: pool}
}

func (e *env) secret(t *testing.T, name, typ string, val []byte) uuid.UUID {
	t.Helper()
	m, err := e.sec.Create(context.Background(), e.ws, secrets.CreateInput{Name: name, Type: typ, Value: val})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

type countScanner struct{ n int }

func (c *countScanner) Scan(_ context.Context, dir string) (json.RawMessage, error) {
	c.n++
	if _, err := os.Stat(filepath.Join(dir, "a.yml")); err != nil {
		return nil, err
	}
	return []byte(`{"files":1}`), nil
}

func TestCRUDAndValidation(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "My Repo", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "my-repo" || p.DefaultRef != "main" || p.LastSyncStatus != "never" || p.HostKeyPinned {
		t.Fatalf("unexpected project %+v", p)
	}
	if _, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "My Repo", Slug: "other", URL: repo.url()}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	sshKey := e.secret(t, "key", secrets.TypeSSHKey, genSSHKey(t))
	tok := e.secret(t, "tok", secrets.TypeToken, []byte("tok-123"))
	pw := e.secret(t, "pw", secrets.TypePassword, []byte("pw-123"))
	bad := []CreateInput{
		{Name: "", URL: repo.url()},
		{Name: "x", Slug: "Bad Slug", URL: repo.url()},
		{Name: "x", URL: "ftp://example.org/x"},
		{Name: "x", URL: "https://user:pw@example.org/x.git"},                            //nolint:gosec // placeholder, rejection test
		{Name: "x", URL: "git@git.example.org:o/r.git"},                                  // ssh needs a key
		{Name: "x", URL: "git@git.example.org:o/r.git", CredentialSecretID: &tok},        // wrong type
		{Name: "x", URL: "https://git.example.org/o/r.git", CredentialSecretID: &sshKey}, // wrong type
		{Name: "x", URL: "https://git.example.org/o/r.git", CredentialSecretID: &pw},     // password needs user
		{Name: "x", URL: repo.url(), CredentialSecretID: ptr(uuid.New())},                // unknown secret
		{Name: "x", URL: repo.url(), SyncIntervalSeconds: ptr(5)},                        // too frequent
		{Name: "x", URL: repo.url(), DefaultRef: "--upload-pack=x"},                      // option-like ref
	}
	for i, in := range bad {
		if _, err := e.svc.Create(ctx, e.ws, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad[%d]: want ErrInvalid, got %v", i, err)
		}
	}
	ok, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "ssh", URL: "git@git.example.org:o/r.git", CredentialSecretID: &sshKey, SyncIntervalSeconds: ptr(300)})
	if err != nil {
		t.Fatal(err)
	}
	if !ok.SSH || ok.SyncIntervalSeconds == nil || *ok.SyncIntervalSeconds != 300 {
		t.Errorf("ssh project: %+v", ok)
	}
	// update: clear interval, rename; wrong id and workspace are not found
	name := "ssh2"
	up, err := e.svc.Update(ctx, e.ws, ok.ID, UpdateInput{Name: &name, SyncIntervalSeconds: Field[int]{Set: true}})
	if err != nil || up.Name != "ssh2" || up.SyncIntervalSeconds != nil || up.CredentialSecretID == nil {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := e.svc.Get(ctx, uuid.New(), ok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign workspace: %v", err)
	}
	list, err := e.svc.List(ctx, e.ws)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := e.svc.Delete(ctx, e.ws, ok.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.ws, ok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSyncRecordsRevisions(t *testing.T) {
	sc := &countScanner{}
	e := setup(t, sc)
	ctx := context.Background()
	repo := newRepo(t)
	tok := e.secret(t, "tok", secrets.TypeToken, []byte("tok-123"))
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "r", URL: repo.url(), CredentialSecretID: &tok})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LatestRevision(ctx, e.ws, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("latest before sync: %v", err)
	}
	if err := e.svc.Test(ctx, e.ws, p.ID); err != nil {
		t.Fatal(err)
	}
	sha1 := git(t, repo.bare, "rev-parse", "main")
	rev, err := e.svc.Sync(ctx, e.ws, p.ID)
	if err != nil || rev.SHA != sha1 || !strings.Contains(string(rev.Scan), `"files"`) {
		t.Fatalf("sync: %+v %v", rev, err)
	}
	if _, err := e.svc.Sync(ctx, e.ws, p.ID); err != nil { // same SHA: still one revision
		t.Fatal(err)
	}
	sha2 := repo.commit("b.yml")
	if rev, err = e.svc.Sync(ctx, e.ws, p.ID); err != nil || rev.SHA != sha2 {
		t.Fatalf("sync 2: %+v %v", rev, err)
	}
	revs, err := e.svc.Revisions(ctx, e.ws, p.ID, 10)
	if err != nil || len(revs) != 2 || revs[0].SHA != sha2 || revs[1].SHA != sha1 {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
	if latest, err := e.svc.LatestRevision(ctx, e.ws, p.ID); err != nil || latest.SHA != sha2 {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	got, _ := e.svc.Get(ctx, e.ws, p.ID)
	if got.LastSyncStatus != "ok" || got.LastSyncSHA != sha2 || got.LastSyncAt == nil {
		t.Errorf("status: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.data, "projects", p.ID.String(), "b.yml")); err != nil {
		t.Errorf("cache clone missing: %v", err)
	}

	// failing sync records the error and keeps the last SHA
	if _, err := e.svc.Update(ctx, e.ws, p.ID, UpdateInput{URL: ptr("file://" + filepath.Join(t.TempDir(), "missing.git"))}); err != nil {
		t.Fatal(err)
	}
	var re *RemoteError
	if _, err := e.svc.Sync(ctx, e.ws, p.ID); !errors.As(err, &re) {
		t.Fatalf("want RemoteError, got %v", err)
	}
	got, _ = e.svc.Get(ctx, e.ws, p.ID)
	if got.LastSyncStatus != "error" || got.LastSyncError == "" || got.LastSyncSHA != sha2 {
		t.Errorf("error status: %+v", got)
	}

	cache := filepath.Join(e.data, "projects", p.ID.String())
	if err := e.svc.Delete(ctx, e.ws, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Errorf("cache not removed: %v", err)
	}
}

func TestHostKeyPinning(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	sshKey := e.secret(t, "key", secrets.TypeSSHKey, genSSHKey(t))
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "s", URL: "ssh://git@git.example.org:2222/o/r.git", CredentialSecretID: &sshKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Sync(ctx, e.ws, p.ID); !errors.Is(err, ErrHostKeyNotPinned) {
		t.Fatalf("sync without pin: %v", err)
	}
	if err := e.svc.Test(ctx, e.ws, p.ID); !errors.Is(err, ErrHostKeyNotPinned) {
		t.Fatalf("test without pin: %v", err)
	}
	k1, k2 := fakeKey("one"), fakeKey("two")
	e.git.keys = []gitops.HostKey{k1, k2}
	keys, err := e.svc.ScanHostKeys(ctx, e.ws, p.ID)
	if err != nil || len(keys) != 2 || keys[0].Fingerprint != k1.Fingerprint {
		t.Fatalf("scan: %+v %v", keys, err)
	}
	if got, _ := e.svc.Get(ctx, e.ws, p.ID); got.HostKeyPinned {
		t.Fatal("scan must not store anything")
	}
	if _, err := e.svc.ConfirmHostKey(ctx, e.ws, p.ID, "nonsense"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad format: %v", err)
	}
	if _, err := e.svc.ConfirmHostKey(ctx, e.ws, p.ID, fakeKey("other").Fingerprint); !errors.Is(err, ErrHostKeyMismatch) {
		t.Errorf("mismatch: %v", err)
	}
	pinned, err := e.svc.ConfirmHostKey(ctx, e.ws, p.ID, k2.Fingerprint)
	if err != nil || !pinned.HostKeyPinned || len(pinned.HostKeyFingerprints) != 1 || pinned.HostKeyFingerprints[0] != k2.Fingerprint {
		t.Fatalf("confirm: %+v %v", pinned, err)
	}
	// changing the repository path on the same host keeps the pin, changing the host clears it
	same, err := e.svc.Update(ctx, e.ws, p.ID, UpdateInput{URL: ptr("ssh://git@git.example.org:2222/o/other.git")})
	if err != nil || !same.HostKeyPinned {
		t.Fatalf("same host: %+v %v", same, err)
	}
	moved, err := e.svc.Update(ctx, e.ws, p.ID, UpdateInput{URL: ptr("ssh://git@git.example.net:2222/o/other.git")})
	if err != nil || moved.HostKeyPinned {
		t.Fatalf("moved host: %+v %v", moved, err)
	}
	// non-ssh projects cannot be scanned
	repo := newRepo(t)
	h, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "h", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ScanHostKeys(ctx, e.ws, h.ID); !errors.Is(err, ErrNotSSH) {
		t.Errorf("not ssh: %v", err)
	}
}

func TestSyncsDoNotOverlap(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "r", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	e.git.gate, e.git.entered = make(chan struct{}), make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	var first error
	go func() {
		defer wg.Done()
		_, first = e.svc.Sync(ctx, e.ws, p.ID)
	}()
	<-e.git.entered
	if _, err := e.svc.Sync(ctx, e.ws, p.ID); !errors.Is(err, ErrSyncInProgress) {
		t.Errorf("overlapping sync: %v", err)
	}
	close(e.git.gate)
	wg.Wait()
	if first != nil {
		t.Errorf("first sync: %v", first)
	}
}
