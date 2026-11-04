package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultops"
)

const vaultPlain = "plain-test-value-123"

type fakeVaultIDs struct {
	err     error
	created []vaultids.Input
}

func (f *fakeVaultIDs) ident() vaultids.Identity {
	return vaultids.Identity{ID: uuid.New(), Label: "default", Provider: vaultids.ProviderSecret}
}

func (f *fakeVaultIDs) Create(_ context.Context, _ uuid.UUID, in vaultids.Input) (vaultids.Identity, error) {
	f.created = append(f.created, in)
	return f.ident(), f.err
}

func (f *fakeVaultIDs) List(context.Context, uuid.UUID) ([]vaultids.Identity, error) {
	return []vaultids.Identity{f.ident()}, f.err
}

func (f *fakeVaultIDs) Get(context.Context, uuid.UUID, uuid.UUID) (vaultids.Identity, error) {
	return f.ident(), f.err
}

func (f *fakeVaultIDs) Update(context.Context, uuid.UUID, uuid.UUID, vaultids.Input) (vaultids.Identity, error) {
	return f.ident(), f.err
}

func (f *fakeVaultIDs) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

type fakeVaultOps struct {
	err      error
	decrypts []vaultops.DecryptInput
	encrypts []vaultops.EncryptInput
	rekeys   []vaultops.RekeyInput
	binary   bool
}

func (f *fakeVaultOps) List(context.Context, uuid.UUID, uuid.UUID) (vaultops.Listing, error) {
	return vaultops.Listing{SHA: "abc"}, f.err
}

func (f *fakeVaultOps) Decrypt(_ context.Context, _, _ uuid.UUID, in vaultops.DecryptInput) (vaultops.Decrypted, error) {
	f.decrypts = append(f.decrypts, in)
	if f.binary {
		return vaultops.Decrypted{Plaintext: []byte{0xff, 0xfe}, Identity: "default"}, f.err
	}
	return vaultops.Decrypted{Plaintext: []byte(vaultPlain), Identity: "default"}, f.err
}

func (f *fakeVaultOps) Encrypt(_ context.Context, _, _ uuid.UUID, in vaultops.EncryptInput) (projects.WriteResult, error) {
	f.encrypts = append(f.encrypts, in)
	return projects.WriteResult{Branch: "acp/vault", SHA: "def", Paths: []string{in.Path}}, f.err
}

func (f *fakeVaultOps) Rekey(_ context.Context, _, _ uuid.UUID, in vaultops.RekeyInput) (projects.WriteResult, error) {
	f.rekeys = append(f.rekeys, in)
	return projects.WriteResult{Branch: "acp/vault", SHA: "def", Paths: in.Paths}, f.err
}

// failingAuditStore fails every audit write.
type failingAuditStore struct{ fakeAPIStore }

func (failingAuditStore) InsertAudit(context.Context, dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error) {
	return dbgen.InsertAuditRow{}, errors.New("audit down")
}

func vaultRouter(role string, store func(fakeAPIStore) APIStore, ids *fakeVaultIDs, ops *fakeVaultOps, audits *[]string) http.Handler {
	uid := uuid.New()
	base := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits}
	st := store(base)
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(base, nil, authn, quiet), st), WithVault(ids, ops))
}

func plainStore(s fakeAPIStore) APIStore { return s }

func doVault(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestVaultAPIPermissions(t *testing.T) {
	prj := "/projects/" + uuid.NewString() + "/vault"
	ident := "/vault-identities/" + uuid.NewString()
	create := `{"label":"default","provider":"secret","secret_id":"` + uuid.NewString() + `"}`
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer cannot list identities", policy.RoleViewer, "GET", "/vault-identities", "", 403, ""},
		{"editor lists identities", policy.RoleEditor, "GET", "/vault-identities", "", 200, ""},
		{"editor gets identity", policy.RoleEditor, "GET", ident, "", 200, ""},
		{"editor cannot create", policy.RoleEditor, "POST", "/vault-identities", create, 403, ""},
		{"admin creates", policy.RoleAdmin, "POST", "/vault-identities", create, 201, "vault_identity.create"},
		{"admin updates", policy.RoleAdmin, "PUT", ident, create, 200, "vault_identity.update"},
		{"editor cannot delete", policy.RoleEditor, "DELETE", ident, "", 403, ""},
		{"admin deletes", policy.RoleAdmin, "DELETE", ident, "", 204, "vault_identity.delete"},
		{"bad identity id", policy.RoleAdmin, "GET", "/vault-identities/nope", "", 404, ""},
		{"unknown field", policy.RoleAdmin, "POST", "/vault-identities", `{"label":"a","x":1}`, 400, ""},
		{"editor lists project vault", policy.RoleEditor, "GET", prj, "", 200, ""},
		{"operator cannot list project vault", policy.RoleOperator, "GET", prj, "", 403, ""},
		{"editor cannot decrypt", policy.RoleEditor, "POST", prj + "/decrypt", `{"path":"a.yml"}`, 403, ""},
		{"admin decrypts", policy.RoleAdmin, "POST", prj + "/decrypt", `{"path":"a.yml"}`, 200, "vault.decrypt"},
		{"editor cannot encrypt", policy.RoleEditor, "POST", prj + "/encrypt", `{"path":"a.yml","plaintext":"x"}`, 403, ""},
		{"admin encrypts", policy.RoleAdmin, "POST", prj + "/encrypt", `{"path":"a.yml","plaintext":"x","identity":"prod"}`, 200, "vault.encrypt"},
		{"editor cannot rekey", policy.RoleEditor, "POST", prj + "/rekey", `{"paths":["a.yml"],"to":"prod"}`, 403, ""},
		{"admin rekeys", policy.RoleAdmin, "POST", prj + "/rekey", `{"paths":["a.yml"],"to":"prod"}`, 200, "vault.rekey"},
		{"bad project id", policy.RoleAdmin, "GET", "/projects/nope/vault", "", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			rec := doVault(vaultRouter(tc.role, plainStore, &fakeVaultIDs{}, &fakeVaultOps{}, &audits), tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			var got []string
			for _, a := range audits {
				if a != "authz.denied" {
					got = append(got, a)
				}
			}
			if tc.audit == "" && len(got) != 0 || tc.audit != "" && (len(got) != 1 || got[0] != tc.audit) {
				t.Errorf("audits %v, want %q", got, tc.audit)
			}
		})
	}
}

func TestVaultAPIDecrypt(t *testing.T) {
	var audits []string
	ops := &fakeVaultOps{}
	h := vaultRouter(policy.RoleAdmin, plainStore, &fakeVaultIDs{}, ops, &audits)
	rec := doVault(h, "POST", "/projects/"+uuid.NewString()+"/vault/decrypt", `{"path":"group_vars/s.yml","key_path":"db.password"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), vaultPlain) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	if len(ops.decrypts) != 1 || ops.decrypts[0].KeyPath != "db.password" {
		t.Errorf("decrypt input: %+v", ops.decrypts)
	}
	ops.binary = true
	rec = doVault(h, "POST", "/projects/"+uuid.NewString()+"/vault/decrypt", `{"path":"a.bin"}`)
	if !strings.Contains(rec.Body.String(), `"encoding":"base64"`) {
		t.Errorf("binary plaintext must be base64: %s", rec.Body.String())
	}
}

func TestVaultAPIDecryptFailsClosedWithoutAudit(t *testing.T) {
	h := vaultRouter(policy.RoleAdmin, func(s fakeAPIStore) APIStore { return failingAuditStore{s} }, &fakeVaultIDs{}, &fakeVaultOps{}, nil)
	rec := doVault(h, "POST", "/projects/"+uuid.NewString()+"/vault/decrypt", `{"path":"a.yml"}`)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), vaultPlain) {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestVaultAPIErrorMapping(t *testing.T) {
	prj := "/projects/" + uuid.NewString() + "/vault"
	tests := []struct {
		err  error
		want int
		code string
	}{
		{vaultids.ErrProviderNotSupported, 422, "provider_not_supported_in_app"},
		{vaultops.ErrNotVault, 422, "not_vault"},
		{vaultops.ErrNoIdentity, 422, "no_identity"},
		{vaultops.ErrDecrypt, 422, "decrypt_failed"},
		{vaultops.ErrInvalid, 422, "invalid"},
		{vaultops.ErrTooLarge, 413, "too_large"},
		{vaultops.ErrFileNotFound, 404, "file_not_found"},
		{vaultops.ErrKeyNotFound, 404, "key_not_found"},
		{projects.ErrNotFound, 404, "not_found"},
		{&projects.ConflictError{Paths: []string{"a.yml"}, Reason: "moved"}, 409, "git_conflict"},
		{context.DeadlineExceeded, 500, ""},
	}
	for _, tc := range tests {
		for _, path := range []string{prj + "/decrypt", prj + "/encrypt", prj + "/rekey"} {
			var audits []string
			h := vaultRouter(policy.RoleAdmin, plainStore, &fakeVaultIDs{}, &fakeVaultOps{err: tc.err}, &audits)
			body := map[string]string{"/decrypt": `{"path":"a.yml"}`, "/encrypt": `{"path":"a.yml","plaintext":"x"}`,
				"/rekey": `{"paths":["a.yml"],"to":"prod"}`}[path[strings.LastIndex(path, "/"):]]
			rec := doVault(h, "POST", path, body)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.code) {
				t.Errorf("%v on %s: status %d body %s", tc.err, path, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), vaultPlain) {
				t.Errorf("error response leaks plaintext")
			}
			if tc.want >= 400 && strings.HasSuffix(path, "/decrypt") && len(audits) != 1 {
				t.Errorf("failed decrypt must be audited once, got %v", audits)
			}
		}
	}
	// identity CRUD errors
	for err, want := range map[error]int{vaultids.ErrNotFound: 404, vaultids.ErrConflict: 409, vaultids.ErrInvalid: 422} {
		h := vaultRouter(policy.RoleAdmin, plainStore, &fakeVaultIDs{err: err}, &fakeVaultOps{}, nil)
		if rec := doVault(h, "POST", "/vault-identities", `{"label":"a"}`); rec.Code != want {
			t.Errorf("%v: status %d, want %d", err, rec.Code, want)
		}
	}
}

func TestVaultAPIEncryptPassesFields(t *testing.T) {
	ops := &fakeVaultOps{}
	h := vaultRouter(policy.RoleAdmin, plainStore, &fakeVaultIDs{}, ops, nil)
	rec := doVault(h, "POST", "/projects/"+uuid.NewString()+"/vault/encrypt",
		`{"path":"v.yml","key_path":"a.b","plaintext":"`+vaultPlain+`","identity":"prod","branch":"acp/x","message":"m"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), vaultPlain) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := ops.encrypts[0]
	if in.Path != "v.yml" || in.KeyPath != "a.b" || in.Plaintext == nil || *in.Plaintext != vaultPlain || in.Identity != "prod" ||
		in.Branch != "acp/x" || in.Message != "m" {
		t.Errorf("input: %+v", in)
	}
}
