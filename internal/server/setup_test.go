package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

type fakeInfo struct{}

func (fakeInfo) Info() auth.Info {
	return auth.Info{OIDC: true, BreakGlass: true, IssuerHost: "idp.example.com"}
}

func setupHandler(role string, audits *[]string) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid, DisplayName: "T"}})))
		})
	}
	az := policy.New(st, nil, authn, quiet)
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st))
}

func doReq(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSetupGetWithoutAuthInfo(t *testing.T) {
	rec := doReq(setupHandler(policy.RoleViewer, nil), http.MethodGet, "/api/v1/workspaces/default/setup", "")
	var got setupResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	if got.Completed || got.Name != "Default" || len(got.Auth.RoleMap) != 4 || got.Auth.OIDC {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestSetupResponseWithInfo(t *testing.T) {
	got := setupResponseFor(dbgen.Workspace{Name: "X"}, fakeInfo{})
	if !got.Auth.OIDC || !got.Auth.BreakGlass || got.Auth.IssuerHost != "idp.example.com" {
		t.Errorf("unexpected: %+v", got.Auth)
	}
}

func TestSetupWritesRequireManage(t *testing.T) {
	for _, role := range []string{policy.RoleViewer, policy.RoleOperator, policy.RoleEditor} {
		h := setupHandler(role, nil)
		if c := doReq(h, http.MethodPut, "/api/v1/workspaces/default/setup", `{"name":"N"}`).Code; c != 403 {
			t.Errorf("%s PUT: %d", role, c)
		}
		if c := doReq(h, http.MethodPost, "/api/v1/workspaces/default/setup/complete", "").Code; c != 403 {
			t.Errorf("%s complete: %d", role, c)
		}
	}
}

func TestSetupUpdateAndComplete(t *testing.T) {
	var audits []string
	h := setupHandler(policy.RoleAdmin, &audits)
	rec := doReq(h, http.MethodPut, "/api/v1/workspaces/default/setup", `{"name":"  Home Lab  "}`)
	var got setupResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Name != "Home Lab" {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = doReq(h, http.MethodPost, "/api/v1/workspaces/default/setup/complete", "")
	got = setupResponse{}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !got.Completed || got.CompletedAt == nil {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if len(audits) != 2 || audits[0] != "workspace.rename" || audits[1] != "workspace.setup.complete" {
		t.Errorf("audits: %v", audits)
	}
}

func TestSetupRejectsBadNames(t *testing.T) {
	h := setupHandler(policy.RoleAdmin, nil)
	for name, body := range map[string]string{
		"empty":    `{"name":"   "}`,
		"long":     `{"name":"` + strings.Repeat("a", 81) + `"}`,
		"control":  `{"name":"a\u0000b"}`,
		"newline":  `{"name":"a\nb"}`,
		"unknown":  `{"name":"ok","slug":"x"}`,
		"notjson":  `name=x`,
		"wrongtyp": `{"name":5}`,
	} {
		c := doReq(h, http.MethodPut, "/api/v1/workspaces/default/setup", body).Code
		if c != 422 && c != 400 {
			t.Errorf("%s: status %d", name, c)
		}
	}
}

func TestSetupAuditSurvivesCancelledRequest(t *testing.T) {
	uid := uuid.New()
	var ctxErrs []error
	var audits []string
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid,
		role: policy.RoleAdmin, audits: &audits, auditCtxErrs: &ctxErrs}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/api/v1/workspaces/default/setup", strings.NewReader(`{"name":"X"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(ctxErrs) != 1 || ctxErrs[0] != nil || len(audits) != 1 {
		t.Fatalf("audit ctx errs=%v audits=%v", ctxErrs, audits)
	}
}
