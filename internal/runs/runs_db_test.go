package runs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// fakeAuthz allows only the listed hosts.
type fakeAuthz struct {
	allowed map[string]bool
	calls   int
}

func (f *fakeAuthz) AuthorizeHosts(_ context.Context, _ auth.Principal, _ uuid.UUID, perm policy.Permission, _ uuid.UUID, hosts []string) error {
	f.calls++
	if perm != policy.RunExecute {
		return errors.New("unexpected permission")
	}
	for _, h := range hosts {
		if !f.allowed[h] {
			return policy.ErrForbidden
		}
	}
	return nil
}

type env struct {
	svc       *Service
	q         *dbgen.Queries
	az        *fakeAuthz
	ws        uuid.UUID
	project   uuid.UUID
	inventory uuid.UUID
	user      dbgen.User
	principal auth.Principal
}

const scanJSON = `{"playbooks":[{"path":"site.yml","hosts":["all"],"roles":["web"]},{"path":"db.yml"}],
"roles":[{"name":"web","path":"roles/web","entry_points":["main"],"surveys":[{"role":"web","entry_point":"main",
"fields":[{"name":"web_port","type":"int"}]}]}]}`

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
	user, err := q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
		OidcIssuer: pgText("https://idp.example.test"), OidcSubject: pgText(uuid.NewString()), Email: "u@example.test", DisplayName: "U",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{
		ProjectID: prj.ID, Sha: "0123456789abcdef0123456789abcdef01234567", Scan: []byte(scanJSON), Warnings: []byte("[]"),
	}); err != nil {
		t.Fatal(err)
	}
	inv := inventory.New(inventory.Options{DB: pool})
	iv, err := inv.Create(ctx, ws.ID, inventory.CreateInput{Name: "inv", Mode: inventory.ModeDB})
	if err != nil {
		t.Fatal(err)
	}
	web, _, err := inv.CreateGroup(ctx, ws.ID, iv.ID, inventory.GroupInput{Name: "web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbg, _, err := inv.CreateGroup(ctx, ws.ID, iv.ID, inventory.GroupInput{Name: "db"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]uuid.UUID{"web1.example.test": web.ID, "web2.example.test": web.ID, "db1.example.test": dbg.ID} {
		if _, _, err := inv.CreateHost(ctx, ws.ID, iv.ID, inventory.HostInput{Name: name, GroupIDs: []uuid.UUID{g}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	az := &fakeAuthz{allowed: map[string]bool{"web1.example.test": true, "web2.example.test": true}}
	return &env{
		svc: New(Options{DB: pool, Graphs: inv, Authz: az}), q: q, az: az, ws: ws.ID, project: prj.ID, inventory: iv.ID,
		user: user, principal: auth.Principal{User: user},
	}
}

func (e *env) template(t *testing.T, mutate func(*TemplateInput)) Template {
	t.Helper()
	in := TemplateInput{Name: sp("deploy"), ProjectID: &e.project, InventoryID: &e.inventory, Playbook: sp("site.yml"), CreatedBy: e.user.ID}
	if mutate != nil {
		mutate(&in)
	}
	tpl, err := e.svc.CreateTemplate(context.Background(), e.ws, in)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func (e *env) actor() Actor { return Actor{UserID: e.user.ID, Principal: &e.principal} }

func TestTemplateValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	other := uuid.New()
	tests := []struct {
		name string
		in   func(*TemplateInput)
		want string
	}{
		{"missing playbook in scan", func(in *TemplateInput) { in.Playbook = sp("nope.yml") }, "does not exist in the last scan"},
		{"foreign project", func(in *TemplateInput) { in.ProjectID = &other }, "project does not exist"},
		{"foreign inventory", func(in *TemplateInput) { in.InventoryID = &other }, "inventory does not exist"},
		{"foreign ssh secret", func(in *TemplateInput) { in.SSHSecretID.Set, in.SSHSecretID.Value = true, &other }, "ssh_secret_id does not exist"},
		{"foreign vault identity", func(in *TemplateInput) { in.VaultIdentityIDs = &[]uuid.UUID{other} }, "vault identity does not exist"},
		{"bad limit", func(in *TemplateInput) { in.DefaultLimit = sp("~x") }, "regular expression"},
		{"bad survey", func(in *TemplateInput) {
			in.Survey.Set, in.Survey.Value = true, rawPtr(`[{"name":"a","type":"zzz"}]`)
		}, "unknown type"},
		{"bad env name", func(in *TemplateInput) { in.EnvVars = map[string]any{"PATH": "/x"} }, "not allowed"},
		{"bad env value", func(in *TemplateInput) { in.EnvVars = map[string]any{"A": []any{1}} }, "must be a string"},
		{"bad max concurrent", func(in *TemplateInput) { in.MaxConcurrent = ip(0) }, "max_concurrent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := TemplateInput{Name: sp("t-" + uuid.NewString()[:6]), ProjectID: &e.project, InventoryID: &e.inventory, Playbook: sp("site.yml")}
			tc.in(&in)
			_, err := e.svc.CreateTemplate(ctx, e.ws, in)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrInvalid containing %q", err, tc.want)
			}
		})
	}
	// Unsynced project.
	p2, err := e.q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: e.ws, Name: "p2", Slug: "p2", Url: "https://git.example.test/o/r2.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateTemplate(ctx, e.ws, TemplateInput{Name: sp("x"), ProjectID: &p2.ID, InventoryID: &e.inventory, Playbook: sp("site.yml")}); !errors.Is(err, ErrNotSynced) {
		t.Errorf("unsynced project: %v", err)
	}
	// Duplicate name.
	e.template(t, nil)
	if _, err := e.svc.CreateTemplate(ctx, e.ws, TemplateInput{Name: sp("deploy"), ProjectID: &e.project, InventoryID: &e.inventory, Playbook: sp("site.yml")}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
}

func TestTemplateUpdateAndDefaults(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tpl := e.template(t, nil)
	if tpl.Slug != "deploy" || tpl.MaxConcurrent != 1 || !tpl.AllowLimitOverride || tpl.AllowVarsOverride || tpl.VaultIdentityIDs == nil {
		t.Errorf("defaults: %+v", tpl)
	}
	up, err := e.svc.UpdateTemplate(ctx, e.ws, tpl.ID, TemplateInput{DefaultLimit: sp("web"), AllowVarsOverride: bp(true), MaxConcurrent: ip(3)})
	if err != nil {
		t.Fatal(err)
	}
	if up.DefaultLimit != "web" || !up.AllowVarsOverride || up.MaxConcurrent != 3 || up.Name != "deploy" || up.Playbook != "site.yml" {
		t.Errorf("update: %+v", up)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.ws, uuid.New(), TemplateInput{Description: sp("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := e.svc.UpdateTemplate(ctx, e.ws, tpl.ID, TemplateInput{Playbook: sp("gone.yml")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("playbook change: %v", err)
	}
}

func TestEnqueue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tpl := e.template(t, func(in *TemplateInput) {
		in.ExtraVars = map[string]any{"env": "prod"}
		in.Survey.Set, in.Survey.Value = true, rawPtr(`[{"name":"web_port","type":"int","default":8080},{"name":"pw","type":"str","secret":true}]`)
	})
	t.Run("defaults snapshot", func(t *testing.T) {
		run, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{Limit: sp("web"), ExtraVars: map[string]any{"pw": "s3cr3t"}}, TriggerManual, e.actor(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != StatusQueued || run.Trigger != TriggerManual || run.ProjectRevision == "" || run.TriggeredBy == nil || *run.TriggeredBy != e.user.ID {
			t.Errorf("run: %+v", run)
		}
		if !slices.Equal(run.Params.Hosts, []string{"web1.example.test", "web2.example.test"}) || run.Params.ExtraVars["web_port"] != float64(8080) {
			t.Errorf("params %+v", run.Params)
		}
	})
	t.Run("out of scope", func(t *testing.T) {
		// db1 is not allowed by the fake authorizer and the template has no limit.
		_, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{}, TriggerManual, e.actor(), nil)
		if !errors.Is(err, policy.ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})
	t.Run("limit inside scope", func(t *testing.T) {
		run, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{Limit: sp("web:!web2.example.test"), ExtraVars: map[string]any{"pw": "s3cr3t", "web_port": 9000}}, TriggerAPI, e.actor(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(run.Params.Hosts, []string{"web1.example.test"}) || run.Params.ExtraVars["web_port"] != float64(9000) || run.Params.ExtraVars["env"] != "prod" {
			t.Errorf("params: %+v", run.Params)
		}
		if run.Params.ExtraVars["pw"] != MaskedValue || strings.Contains(string(run.Overrides), "s3cr3t") {
			t.Errorf("secret survey value leaked: %v / %s", run.Params.ExtraVars["pw"], run.Overrides)
		}
		// The stored (unmasked) snapshot keeps the value for the worker bundle.
		raw, err := e.q.GetRun(ctx, dbgen.GetRunParams{ID: run.ID, WorkspaceID: e.ws})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := DecodeParams(raw.Params)
		if p.ExtraVars["pw"] != "s3cr3t" || !slices.Equal(p.SecretVars, []string{"pw"}) {
			t.Errorf("stored params: %v", p.SecretVars)
		}
	})
	t.Run("limit without match", func(t *testing.T) {
		_, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{Limit: sp("nothing")}, TriggerManual, e.actor(), nil)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "no hosts") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("schedule skips host authorization", func(t *testing.T) {
		before := e.az.calls
		run, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{ExtraVars: map[string]any{"pw": "x"}}, TriggerSchedule, Actor{UserID: e.user.ID}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if e.az.calls != before || len(run.Params.Hosts) != 3 {
			t.Errorf("calls %d->%d hosts %v", before, e.az.calls, run.Params.Hosts)
		}
	})
	t.Run("interactive needs principal", func(t *testing.T) {
		if _, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{}, TriggerManual, Actor{}, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("pins revision at enqueue time", func(t *testing.T) {
		run, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{Limit: sp("web1.example.test"), ExtraVars: map[string]any{"pw": "x"}}, TriggerManual, e.actor(), nil)
		if err != nil {
			t.Fatal(err)
		}
		newSHA := "fedcba9876543210fedcba9876543210fedcba98"
		time.Sleep(10 * time.Millisecond)
		if _, err := e.q.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{ProjectID: e.project, Sha: newSHA, Scan: []byte(scanJSON), Warnings: []byte("[]")}); err != nil {
			t.Fatal(err)
		}
		run2, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{Limit: sp("web1.example.test"), ExtraVars: map[string]any{"pw": "x"}}, TriggerManual, e.actor(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if run.ProjectRevision == newSHA || run2.ProjectRevision != newSHA {
			t.Errorf("revisions %s / %s", run.ProjectRevision, run2.ProjectRevision)
		}
	})
}

func TestScannedSurveyIsLenient(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tpl := e.template(t, func(in *TemplateInput) { in.AllowVarsOverride = bp(true); in.DefaultLimit = sp("web") })
	if _, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{ExtraVars: map[string]any{"web_port": "not-a-number"}}, TriggerManual, e.actor(), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("scanned type check: %v", err)
	}
	if _, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{ExtraVars: map[string]any{"web_port": 80, "free": "form"}}, TriggerManual, e.actor(), nil); err != nil {
		t.Errorf("valid vars: %v", err)
	}
}

func TestListCancelAndDelete(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tpl := e.template(t, func(in *TemplateInput) { in.DefaultLimit = sp("web") })
	other := e.template(t, func(in *TemplateInput) { in.Name = sp("other"); in.DefaultLimit = sp("web") })
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		r, err := e.svc.Enqueue(ctx, e.ws, tpl.ID, Overrides{}, TriggerManual, e.actor(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	o, err := e.svc.Enqueue(ctx, e.ws, other.ID, Overrides{}, TriggerAPI, e.actor(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Pagination, newest first.
	var seen []uuid.UUID
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		pg, err := e.svc.ListRuns(ctx, e.ws, Filter{TemplateID: &tpl.ID, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range pg.Items {
			seen = append(seen, r.ID)
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	slices.Reverse(ids)
	if !slices.Equal(seen, ids) {
		t.Errorf("paged order %v want %v", seen, ids)
	}
	// Filters.
	for name, f := range map[string]Filter{
		"trigger": {Trigger: TriggerAPI}, "user": {UserID: &e.user.ID, Trigger: TriggerAPI}, "template": {TemplateID: &other.ID},
	} {
		pg, err := e.svc.ListRuns(ctx, e.ws, f)
		if err != nil || len(pg.Items) != 1 || pg.Items[0].ID != o.ID {
			t.Errorf("filter %s: %v %+v", name, err, pg.Items)
		}
	}
	if pg, err := e.svc.ListRuns(ctx, e.ws, Filter{Status: StatusRunning}); err != nil || len(pg.Items) != 0 {
		t.Errorf("status filter: %v %d", err, len(pg.Items))
	}
	if _, err := e.svc.ListRuns(ctx, e.ws, Filter{Status: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad status: %v", err)
	}
	// Visibility filter + host filter.
	pg, err := e.svc.ListRuns(ctx, e.ws, Filter{Visible: func(r Run) bool { return r.ID == o.ID }})
	if err != nil || len(pg.Items) != 1 {
		t.Errorf("visible: %v %d", err, len(pg.Items))
	}
	if err := e.q.UpsertRunHostResult(ctx, dbgen.UpsertRunHostResultParams{RunID: o.ID, Host: "web1.example.test", ChangedTasks: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	if pg, err := e.svc.ListRuns(ctx, e.ws, Filter{Host: "web1.example.test"}); err != nil || len(pg.Items) != 1 {
		t.Errorf("host filter: %v %d", err, len(pg.Items))
	}
	d, err := e.svc.GetRun(ctx, e.ws, o.ID)
	if err != nil || len(d.HostResults) != 1 || d.HostResults[0].Host != "web1.example.test" {
		t.Errorf("get run: %v %+v", err, d.HostResults)
	}
	if _, err := e.svc.GetRun(ctx, uuid.New(), o.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign workspace: %v", err)
	}

	// Cancel: queued -> cancelled; again -> conflict; running -> cancel requested.
	c, err := e.svc.CancelRun(ctx, e.ws, ids[0])
	if err != nil || c.Status != StatusCancelled {
		t.Fatalf("cancel queued: %v %+v", err, c.Status)
	}
	if _, err := e.svc.CancelRun(ctx, e.ws, ids[0]); !errors.Is(err, ErrConflict) {
		t.Errorf("cancel finished: %v", err)
	}
	if _, err := e.svc.CancelRun(ctx, e.ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel unknown: %v", err)
	}
	claimed, err := e.q.ClaimRun(ctx, "w1")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := e.svc.CancelRun(ctx, e.ws, claimed.ID)
	if err != nil || rc.Status != StatusRunning || rc.CancelRequestedAt == nil {
		t.Errorf("cancel running: %v %+v", err, rc)
	}

	// Deleting a template cancels its queued runs and keeps the history.
	n, err := e.svc.DeleteTemplate(ctx, e.ws, other.ID)
	if err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	d, err = e.svc.GetRun(ctx, e.ws, o.ID)
	if err != nil || d.Status != StatusCancelled || d.TemplateID != nil || d.TemplateName != "other" {
		t.Errorf("after delete: %v %+v", err, d.Run)
	}
	if _, err := e.svc.DeleteTemplate(ctx, e.ws, other.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func rawPtr(s string) *json.RawMessage { r := json.RawMessage(s); return &r }
func ip(i int) *int                    { return &i }
func bp(b bool) *bool                  { return &b }
