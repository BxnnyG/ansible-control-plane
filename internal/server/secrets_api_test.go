package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

type fakeSecrets struct {
	created []secrets.CreateInput
	updated []secrets.UpdateInput
	err     error
}

func (f *fakeSecrets) meta(name, typ string) secrets.Meta {
	return secrets.Meta{ID: uuid.New(), Name: name, Type: typ, Metadata: map[string]any{}}
}

func (f *fakeSecrets) Create(_ context.Context, _ uuid.UUID, in secrets.CreateInput) (secrets.Meta, error) {
	f.created = append(f.created, in)
	if f.err != nil {
		return secrets.Meta{}, f.err
	}
	return f.meta(in.Name, in.Type), nil
}

func (f *fakeSecrets) List(context.Context, uuid.UUID) ([]secrets.Meta, error) {
	return []secrets.Meta{f.meta("a", "token")}, nil
}

func (f *fakeSecrets) Get(context.Context, uuid.UUID, uuid.UUID) (secrets.Meta, error) {
	return f.meta("a", "token"), nil
}

func (f *fakeSecrets) Update(_ context.Context, _, _ uuid.UUID, in secrets.UpdateInput) (secrets.Meta, error) {
	f.updated = append(f.updated, in)
	return f.meta("a", "token"), nil
}

func (f *fakeSecrets) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func TestSecretsAPI(t *testing.T) {
	const val = "super-secret-value-123"
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer cannot list", policy.RoleViewer, "GET", "/secrets", "", 403, ""},
		{"editor lists metadata", policy.RoleEditor, "GET", "/secrets", "", 200, ""},
		{"editor cannot create", policy.RoleEditor, "POST", "/secrets", `{"name":"a","type":"token","value":"` + val + `"}`, 403, ""},
		{"admin creates", policy.RoleAdmin, "POST", "/secrets", `{"name":"a","type":"token","value":"` + val + `"}`, 201, "secret.create"},
		{"unknown field rejected", policy.RoleAdmin, "POST", "/secrets", `{"name":"a","type":"token","value":"x","extra":1}`, 400, ""},
		{"editor gets one", policy.RoleEditor, "GET", "/secrets/" + uuid.NewString(), "", 200, ""},
		{"bad id", policy.RoleAdmin, "GET", "/secrets/nope", "", 404, ""},
		{"admin updates", policy.RoleAdmin, "PUT", "/secrets/" + uuid.NewString(), `{"value":"` + val + `"}`, 200, "secret.update"},
		{"empty update", policy.RoleAdmin, "PUT", "/secrets/" + uuid.NewString(), `{}`, 422, ""},
		{"editor cannot delete", policy.RoleEditor, "DELETE", "/secrets/" + uuid.NewString(), "", 403, ""},
		{"owner deletes", policy.RoleOwner, "DELETE", "/secrets/" + uuid.NewString(), "", 204, "secret.delete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uid := uuid.New()
			var audits []string
			st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: tc.role, audits: &audits}
			authn := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
				})
			}
			fs := &fakeSecrets{}
			h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithSecrets(fs))
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "/api/v1/workspaces/default"+tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), val) {
				t.Error("response contains the secret value")
			}
			if tc.audit != "" && (len(audits) != 1 || audits[0] != tc.audit) {
				t.Errorf("audits %v, want %s", audits, tc.audit)
			}
			if tc.audit == "" && tc.want < 400 && len(audits) != 0 {
				t.Errorf("unexpected audits %v", audits)
			}
		})
	}
}

func TestSecretsAPIErrorMapping(t *testing.T) {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, user: uid, role: policy.RoleAdmin}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	for err, want := range map[error]int{secrets.ErrConflict: 409, secrets.ErrInvalid: 422, context.DeadlineExceeded: 500} {
		fs := &fakeSecrets{err: err}
		h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithSecrets(fs))
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default/secrets",
			strings.NewReader(`{"name":"a","type":"token","value":"v"}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%v: status %d, want %d", err, rec.Code, want)
		}
	}
}
