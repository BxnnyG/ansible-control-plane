package vaultops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/vault"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
)

// fakeProjects serves a temp directory as the checkout and applies write-backs
// to it without Git.
type fakeProjects struct {
	dir      string
	scan     string
	requests []projects.WriteRequest
	conflict bool
}

func (f *fakeProjects) WithCheckout(_ context.Context, _, _ uuid.UUID, fn func(*os.Root, string) error) error {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fn(root, "sha1")
}

func (f *fakeProjects) LatestRevision(context.Context, uuid.UUID, uuid.UUID) (projects.Revision, error) {
	return projects.Revision{SHA: "sha1", Scan: []byte(f.scan)}, nil
}

func (f *fakeProjects) WriteBack(_ context.Context, _, _ uuid.UUID, req projects.WriteRequest) (projects.WriteResult, error) {
	f.requests = append(f.requests, req)
	if f.conflict {
		return projects.WriteResult{}, &projects.ConflictError{Reason: "moved"}
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return projects.WriteResult{}, err
	}
	defer func() { _ = root.Close() }()
	plan, err := req.Plan(root)
	if err != nil {
		return projects.WriteResult{}, err
	}
	var res projects.WriteResult
	for _, c := range plan.Changes {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(f.dir, c.Path)), 0o750); err != nil {
			return res, err
		}
		if err := os.WriteFile(filepath.Join(f.dir, c.Path), c.Content, 0o600); err != nil {
			return res, err
		}
		res.Paths = append(res.Paths, c.Path)
	}
	res.Branch, res.SHA = req.Branch, "sha2"
	return res, nil
}

type fakePasswords map[string]string

func (f fakePasswords) Password(_ context.Context, _ uuid.UUID, label string) (vaultids.Password, error) {
	pw, ok := f[label]
	if !ok {
		return vaultids.Password{}, vaultids.ErrNotFound
	}
	return vaultids.NewPassword(label, pw), nil
}

const testPassword = "test-password"

func setup(t *testing.T, pws fakePasswords) (*Service, *fakeProjects) {
	t.Helper()
	f := &fakeProjects{dir: t.TempDir(), scan: `{"vault_files":[{"path":"a.yml","version":"1.1"}],"inline_vault":null}`}
	return New(f, pws), f
}

func write(t *testing.T, f *fakeProjects, rel, content string) {
	t.Helper()
	p := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, f *fakeProjects, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, rel)) //nolint:gosec // test helper reading a temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func str(s string) *string { return &s }

var ctx = context.Background()
var ws, pid = uuid.New(), uuid.New()

func TestList(t *testing.T) {
	s, _ := setup(t, nil)
	l, err := s.List(ctx, ws, pid)
	if err != nil || l.SHA != "sha1" || len(l.VaultFiles) != 1 || l.InlineVault == nil || len(l.InlineVault) != 0 {
		t.Fatalf("list: %+v %v", l, err)
	}
}

func TestEncryptDecryptWholeFile(t *testing.T) {
	s, f := setup(t, fakePasswords{"default": testPassword, "prod": "other-password"})
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "group_vars/secret.yml", Plaintext: str("pw: hunter2\n"), Identity: "prod"}); err != nil {
		t.Fatal(err)
	}
	enc := read(t, f, "group_vars/secret.yml")
	if !vault.IsVaultFile([]byte(enc)) || strings.Contains(enc, "hunter2") {
		t.Fatalf("not encrypted: %q", enc)
	}
	if h, _ := vault.ParseHeader([]byte(enc)); h.VaultID != "prod" || h.Version != "1.2" {
		t.Errorf("header %+v", h)
	}
	req := f.requests[0]
	if req.Branch != DefaultBranch || req.BaseSHA != "sha1" || !strings.Contains(req.Message, "group_vars/secret.yml") {
		t.Errorf("request: %+v", req)
	}
	d, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "group_vars/secret.yml"})
	if err != nil || string(d.Plaintext) != "pw: hunter2\n" || d.Identity != "prod" {
		t.Fatalf("decrypt: %v %v", d.Identity, err)
	}
	// encrypting an encrypted file is refused
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "group_vars/secret.yml", Plaintext: str("x"), Identity: "prod"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("re-encrypt: %v", err)
	}
}

func TestEncryptExistingFileInPlace(t *testing.T) {
	s, f := setup(t, fakePasswords{"default": testPassword})
	write(t, f, "vars.yml", "a: 1\n")
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "vars.yml", WriteOptions: WriteOptions{Branch: "acp/x", Message: "msg"}}); err != nil {
		t.Fatal(err)
	}
	pt, err := vault.Decrypt([]byte(read(t, f, "vars.yml")), testPassword)
	if err != nil || string(pt) != "a: 1\n" {
		t.Fatalf("%q %v", pt, err)
	}
	if h, _ := vault.ParseHeader([]byte(read(t, f, "vars.yml"))); h.Version != "1.1" {
		t.Errorf("default identity must give 1.1, got %+v", h)
	}
	if f.requests[0].Branch != "acp/x" || f.requests[0].Message != "msg" {
		t.Errorf("request: %+v", f.requests[0])
	}
}

func TestInlineEncryptDecrypt(t *testing.T) {
	s, f := setup(t, fakePasswords{"default": testPassword})
	write(t, f, "vars.yml", "# keep me\ndb:\n  user: app\nitems:\n  - name: a\n")
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "vars.yml", KeyPath: "db.password", Plaintext: str("s3cr3t"), Identity: "default"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "vars.yml", KeyPath: "items[0].token", Plaintext: str("tok"), Identity: "default"}); err != nil {
		t.Fatal(err)
	}
	out := read(t, f, "vars.yml")
	if !strings.Contains(out, "# keep me") || !strings.Contains(out, "password: !vault |") || strings.Contains(out, "s3cr3t") {
		t.Fatalf("file:\n%s", out)
	}
	found, err := vault.FindInlineVault([]byte(out))
	if err != nil || len(found) != 2 || found[0].Path != "db.password" || found[1].Path != "items[0].token" {
		t.Fatalf("found %+v %v", found, err)
	}
	d, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "vars.yml", KeyPath: "db.password"})
	if err != nil || string(d.Plaintext) != "s3cr3t" {
		t.Fatalf("decrypt: %v", err)
	}
	if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "vars.yml", KeyPath: "db.user"}); !errors.Is(err, ErrNotVault) {
		t.Errorf("plain key: %v", err)
	}
	if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "vars.yml", KeyPath: "db.nope"}); !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("missing key: %v", err)
	}
	if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "vars.yml"}); !errors.Is(err, ErrNotVault) {
		t.Errorf("plain file: %v", err)
	}
}

func TestDecryptIdentityResolution(t *testing.T) {
	envProd, _ := vault.Encrypt([]byte("a"), "prod-pw", "prod")
	envOld, _ := vault.Encrypt([]byte("b"), testPassword, "old")
	s, f := setup(t, fakePasswords{"default": testPassword, "prod": "prod-pw"})
	write(t, f, "prod.vault", string(envProd))
	write(t, f, "old.vault", string(envOld))
	if d, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "prod.vault"}); err != nil || string(d.Plaintext) != "a" || d.Identity != "prod" {
		t.Errorf("by header id: %+v %v", d.Identity, err)
	}
	// unknown vault-id falls back to the default identity
	if d, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "old.vault"}); err != nil || string(d.Plaintext) != "b" || d.Identity != "default" {
		t.Errorf("fallback: %+v %v", d.Identity, err)
	}
	// wrong password
	s2 := New(f, fakePasswords{"default": "wrong", "prod": "wrong"})
	if _, err := s2.Decrypt(ctx, ws, pid, DecryptInput{Path: "prod.vault"}); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong password: %v", err)
	}
	// no identity at all
	s3 := New(f, fakePasswords{})
	if _, err := s3.Decrypt(ctx, ws, pid, DecryptInput{Path: "prod.vault"}); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("no identity: %v", err)
	}
	if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "missing.vault"}); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("missing file: %v", err)
	}
	for _, bad := range []string{"../etc/passwd", "/abs", ".git/config", "a/../b", ""} {
		if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: bad}); !errors.Is(err, ErrInvalid) {
			t.Errorf("path %q: %v", bad, err)
		}
	}
}

func TestScriptProviderPassesThrough(t *testing.T) {
	f := &fakeProjects{dir: t.TempDir()}
	s := New(f, scriptPW{})
	env, _ := vault.Encrypt([]byte("a"), testPassword, "")
	write(t, f, "x.vault", string(env))
	if _, err := s.Decrypt(ctx, ws, pid, DecryptInput{Path: "x.vault"}); !errors.Is(err, vaultids.ErrProviderNotSupported) {
		t.Errorf("decrypt: %v", err)
	}
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "y.yml", Plaintext: str("a")}); !errors.Is(err, vaultids.ErrProviderNotSupported) {
		t.Errorf("encrypt: %v", err)
	}
}

type scriptPW struct{}

func (scriptPW) Password(context.Context, uuid.UUID, string) (vaultids.Password, error) {
	return vaultids.Password{}, vaultids.ErrProviderNotSupported
}

func TestPlaintextLimit(t *testing.T) {
	s, _ := setup(t, fakePasswords{"default": testPassword})
	big := strings.Repeat("a", MaxPlaintext+1)
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "a.yml", Plaintext: &big}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v", err)
	}
}

func TestRekey(t *testing.T) {
	s, f := setup(t, fakePasswords{"default": testPassword, "prod": "prod-pw"})
	whole, _ := vault.Encrypt([]byte("whole: 1\n"), testPassword, "")
	write(t, f, "whole.yml", string(whole))
	write(t, f, "inline.yml", "plain: 1\n")
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "inline.yml", KeyPath: "secret", Plaintext: str("v"), Identity: "default"}); err != nil {
		t.Fatal(err)
	}
	f.requests = nil
	res, err := s.Rekey(ctx, ws, pid, RekeyInput{Paths: []string{"whole.yml", "inline.yml"}, To: "prod", WriteOptions: WriteOptions{Branch: "acp/rk"}})
	if err != nil || len(res.Paths) != 2 {
		t.Fatalf("rekey: %+v %v", res, err)
	}
	if pt, err := vault.Decrypt([]byte(read(t, f, "whole.yml")), "prod-pw"); err != nil || string(pt) != "whole: 1\n" {
		t.Errorf("whole: %q %v", pt, err)
	}
	if h, _ := vault.ParseHeader([]byte(read(t, f, "whole.yml"))); h.VaultID != "prod" {
		t.Errorf("header %+v", h)
	}
	inl, _ := vault.FindInlineVault([]byte(read(t, f, "inline.yml")))
	if len(inl) != 1 {
		t.Fatalf("inline: %+v", inl)
	}
	if pt, err := vault.Decrypt(inl[0].Envelope, "prod-pw"); err != nil || string(pt) != "v" {
		t.Errorf("inline: %q %v", pt, err)
	}
	// a plain file has nothing to rekey
	write(t, f, "plain.yml", "a: 1\n")
	if _, err := s.Rekey(ctx, ws, pid, RekeyInput{Paths: []string{"plain.yml"}, To: "prod"}); !errors.Is(err, ErrNotVault) {
		t.Errorf("plain: %v", err)
	}
	if _, err := s.Rekey(ctx, ws, pid, RekeyInput{Paths: []string{"whole.yml"}, From: "ghost", To: "prod"}); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("unknown from: %v", err)
	}
	if _, err := s.Rekey(ctx, ws, pid, RekeyInput{Paths: []string{"whole.yml"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no target: %v", err)
	}
}

func TestConflictPassesThrough(t *testing.T) {
	s, f := setup(t, fakePasswords{"default": testPassword})
	f.conflict = true
	_, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "a.yml", Plaintext: str("x")})
	if !errors.Is(err, projects.ErrGitConflict) {
		t.Errorf("got %v", err)
	}
	if _, err := s.Encrypt(ctx, ws, pid, EncryptInput{Path: "a.yml", Plaintext: str("x"), WriteOptions: WriteOptions{Branch: "bad..branch"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("branch: %v", err)
	}
}

func TestParseKeyPath(t *testing.T) {
	ok := map[string]int{"a": 1, "a.b": 2, "a[0]": 2, "items[2].token": 3, "a[0][1]": 3, "a[0].b.c": 4}
	for p, n := range ok {
		segs, err := parseKeyPath(p)
		if err != nil || len(segs) != n {
			t.Errorf("%q: %v %v", p, segs, err)
		}
	}
	for _, p := range []string{"", ".a", "a.", "a..b", "a[", "a[x]", "a[-1]", "[0"} {
		if _, err := parseKeyPath(p); err == nil {
			t.Errorf("%q must fail", p)
		}
	}
}
