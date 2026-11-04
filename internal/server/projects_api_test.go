package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

type fakeProjects struct {
	err     error
	created []projects.CreateInput
	updated []projects.UpdateInput
	confirm string

	hookKnown   uuid.UUID
	hookOut     projects.HookOutcome
	hookCalls   int
	hookBodyLen int
}

func (f *fakeProjects) p() projects.Project {
	return projects.Project{ID: uuid.New(), Name: "p", Slug: "p", URL: "https://git.example.org/o/r.git", DefaultRef: "main"}
}

func (f *fakeProjects) Create(_ context.Context, _ uuid.UUID, in projects.CreateInput) (projects.Project, error) {
	f.created = append(f.created, in)
	return f.p(), f.err
}

func (f *fakeProjects) List(context.Context, uuid.UUID) ([]projects.Project, error) {
	return []projects.Project{f.p()}, f.err
}

func (f *fakeProjects) Get(context.Context, uuid.UUID, uuid.UUID) (projects.Project, error) {
	return f.p(), f.err
}

func (f *fakeProjects) Update(_ context.Context, _, _ uuid.UUID, in projects.UpdateInput) (projects.Project, error) {
	f.updated = append(f.updated, in)
	return f.p(), f.err
}

func (f *fakeProjects) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeProjects) ScanHostKeys(context.Context, uuid.UUID, uuid.UUID) ([]projects.HostKeyInfo, error) {
	return []projects.HostKeyInfo{{Type: "ssh-ed25519", Fingerprint: "SHA256:abc"}}, f.err
}

func (f *fakeProjects) ConfirmHostKey(_ context.Context, _, _ uuid.UUID, fp string) (projects.Project, error) {
	f.confirm = fp
	return f.p(), f.err
}

func (f *fakeProjects) Test(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeProjects) Sync(context.Context, uuid.UUID, uuid.UUID) (projects.Revision, error) {
	return projects.Revision{SHA: "abc", SyncedAt: time.Now()}, f.err
}

func (f *fakeProjects) Revisions(context.Context, uuid.UUID, uuid.UUID, int) ([]projects.Revision, error) {
	return []projects.Revision{{SHA: "abc"}}, f.err
}

func (f *fakeProjects) LatestRevision(context.Context, uuid.UUID, uuid.UUID) (projects.Revision, error) {
	return projects.Revision{SHA: "abc"}, f.err
}

func projectsRouter(role string, fp *fakeProjects, audits *[]string) http.Handler {
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, audits: audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	return Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithProjects(fp))
}

func TestProjectsAPIPermissions(t *testing.T) {
	id := uuid.NewString()
	create := `{"name":"p","url":"https://git.example.org/o/r.git"}`
	tests := []struct {
		name, role, method, path, body string
		want                           int
		audit                          string
	}{
		{"viewer lists", policy.RoleViewer, "GET", "/projects", "", 200, ""},
		{"viewer gets", policy.RoleViewer, "GET", "/projects/" + id, "", 200, ""},
		{"viewer reads revisions", policy.RoleViewer, "GET", "/projects/" + id + "/revisions", "", 200, ""},
		{"viewer reads latest", policy.RoleViewer, "GET", "/projects/" + id + "/revisions/latest", "", 200, ""},
		{"viewer cannot create", policy.RoleViewer, "POST", "/projects", create, 403, ""},
		{"operator cannot create", policy.RoleOperator, "POST", "/projects", create, 403, ""},
		{"editor creates", policy.RoleEditor, "POST", "/projects", create, 201, "project.create"},
		{"unknown field", policy.RoleEditor, "POST", "/projects", `{"name":"p","url":"x","bogus":1}`, 400, ""},
		{"editor updates", policy.RoleEditor, "PUT", "/projects/" + id, `{"credential_secret_id":null}`, 200, "project.update"},
		{"empty update", policy.RoleEditor, "PUT", "/projects/" + id, `{}`, 422, ""},
		{"operator cannot update", policy.RoleOperator, "PUT", "/projects/" + id, `{"name":"x"}`, 403, ""},
		{"editor deletes", policy.RoleEditor, "DELETE", "/projects/" + id, "", 204, "project.delete"},
		{"viewer cannot delete", policy.RoleViewer, "DELETE", "/projects/" + id, "", 403, ""},
		{"editor scans host key", policy.RoleEditor, "POST", "/projects/" + id + "/hostkey/scan", "", 200, ""},
		{"operator cannot scan", policy.RoleOperator, "POST", "/projects/" + id + "/hostkey/scan", "", 403, ""},
		{"editor confirms", policy.RoleEditor, "POST", "/projects/" + id + "/hostkey/confirm", `{"fingerprint":"SHA256:abc"}`, 200, "project.hostkey_pin"},
		{"operator cannot confirm", policy.RoleOperator, "POST", "/projects/" + id + "/hostkey/confirm", `{"fingerprint":"SHA256:abc"}`, 403, ""},
		{"operator syncs", policy.RoleOperator, "POST", "/projects/" + id + "/sync", "", 200, "project.sync"},
		{"viewer cannot sync", policy.RoleViewer, "POST", "/projects/" + id + "/sync", "", 403, ""},
		{"operator tests", policy.RoleOperator, "POST", "/projects/" + id + "/test", "", 200, ""},
		{"viewer cannot test", policy.RoleViewer, "POST", "/projects/" + id + "/test", "", 403, ""},
		{"bad id", policy.RoleViewer, "GET", "/projects/nope", "", 404, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var audits []string
			h := projectsRouter(tc.role, &fakeProjects{}, &audits)
			req := httptest.NewRequestWithContext(context.Background(), tc.method, "/api/v1/workspaces/default"+tc.path, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
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

func TestProjectsAPIErrorMapping(t *testing.T) {
	id := uuid.NewString()
	tests := []struct {
		err    error
		path   string
		want   int
		code   string
		audits int
	}{
		{projects.ErrNotFound, "/projects/" + id + "/sync", 404, "not_found", 0},
		{projects.ErrHostKeyNotPinned, "/projects/" + id + "/sync", 409, "hostkey_not_pinned", 0},
		{projects.ErrSyncInProgress, "/projects/" + id + "/sync", 409, "sync_in_progress", 0},
		{projects.ErrHostKeyMismatch, "/projects/" + id + "/hostkey/scan", 409, "hostkey_mismatch", 0},
		{projects.ErrNotSSH, "/projects/" + id + "/hostkey/scan", 422, "not_ssh", 0},
		{projects.ErrConflict, "/projects/" + id + "/test", 409, "conflict", 0},
		{&projects.RemoteError{Msg: "git ls-remote: exit 128: fatal: repository not found"}, "/projects/" + id + "/sync", 502, "remote_failed", 1},
		{context.DeadlineExceeded, "/projects/" + id + "/sync", 500, "", 0},
	}
	for _, tc := range tests {
		var audits []string
		h := projectsRouter(policy.RoleAdmin, &fakeProjects{err: tc.err}, &audits)
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/workspaces/default"+tc.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want || (tc.code != "" && !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`)) {
			t.Errorf("%v: status %d body %s, want %d %s", tc.err, rec.Code, rec.Body.String(), tc.want, tc.code)
		}
		if len(audits) != tc.audits {
			t.Errorf("%v: audits %v", tc.err, audits)
		}
	}
}
