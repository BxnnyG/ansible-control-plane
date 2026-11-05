package schedules

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
	svc  *Service
	runs *runs.Service
	ws   uuid.UUID
	user uuid.UUID
	tpl  runs.Template
}

func pgText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
func sp(s string) *string         { return &s }

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
		OidcIssuer: pgText("https://idp.example.test"), OidcSubject: pgText(uuid.NewString()), Email: "u@example.test", DisplayName: "U"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{ProjectID: prj.ID,
		Sha: "0123456789abcdef0123456789abcdef01234567", Scan: []byte(`{"playbooks":[{"path":"site.yml","hosts":["all"]}]}`), Warnings: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	inv := inventory.New(inventory.Options{DB: pool})
	iv, err := inv.Create(ctx, ws.ID, inventory.CreateInput{Name: "inv", Mode: inventory.ModeDB})
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := inv.CreateGroup(ctx, ws.ID, iv.ID, inventory.GroupInput{Name: "web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := inv.CreateHost(ctx, ws.ID, iv.ID, inventory.HostInput{Name: "web1.example.test", GroupIDs: []uuid.UUID{g.ID}}, nil); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	prov, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	rs := runs.New(runs.Options{DB: pool, Graphs: inv, Sealer: prov})
	tpl, err := rs.CreateTemplate(ctx, ws.ID, runs.TemplateInput{Name: sp("deploy"), ProjectID: &prj.ID, InventoryID: &iv.ID,
		Playbook: sp("site.yml"), CreatedBy: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, q: q, runs: rs, ws: ws.ID, user: user.ID, tpl: tpl,
		svc: New(Options{DB: pool, Runs: rs, Secret: secrets.NewService(q, prov)})}
}

func (e *env) create(t *testing.T, in Input) Schedule {
	t.Helper()
	if in.Cron == nil {
		in.Cron = sp("*/5 * * * *")
	}
	s, err := e.svc.Create(context.Background(), e.ws, e.tpl.ID, e.user, in)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// makeDue moves next_run_at into the past by ago.
func (e *env) makeDue(t *testing.T, id uuid.UUID, ago time.Duration) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), "UPDATE schedules SET next_run_at = now() - $2::interval WHERE id = $1", id, ago.String()); err != nil {
		t.Fatal(err)
	}
}

func (e *env) runCount(t *testing.T, trigger string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM runs WHERE workspace_id = $1 AND trigger = $2", e.ws, trigger).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduleCRUDValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for name, in := range map[string]Input{
		"no cron":         {},
		"bad cron":        {Cron: sp("nope")},
		"bad tz":          {Cron: sp("* * * * *"), Timezone: sp("Mars/Base")},
		"bad policy":      {Cron: sp("* * * * *"), MissedPolicy: sp("later")},
		"override denied": {Cron: sp("* * * * *"), Overrides: &runs.Overrides{ExtraVars: map[string]any{"x": 1}}},
		"bad limit":       {Cron: sp("* * * * *"), Overrides: &runs.Overrides{Limit: sp("~web.*")}},
	} {
		if _, err := e.svc.Create(ctx, e.ws, e.tpl.ID, e.user, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := e.svc.Create(ctx, e.ws, uuid.New(), e.user, Input{Cron: sp("* * * * *")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown template err = %v", err)
	}
	s := e.create(t, Input{Name: sp("nightly"), Cron: sp("0 3 * * *"), Timezone: sp("Europe/Berlin")})
	if s.NextRunAt == nil || !s.NextRunAt.After(time.Now()) || s.MissedPolicy != PolicySkip || !s.Enabled {
		t.Errorf("created: %+v", s)
	}
	off := false
	u, err := e.svc.Update(ctx, e.ws, s.ID, Input{Enabled: &off})
	if err != nil || u.NextRunAt != nil || u.Cron != "0 3 * * *" {
		t.Fatalf("disable: %+v %v", u, err)
	}
	on := true
	if u, err = e.svc.Update(ctx, e.ws, s.ID, Input{Enabled: &on}); err != nil || u.NextRunAt == nil {
		t.Fatalf("enable: %+v %v", u, err)
	}
	list, err := e.svc.List(ctx, e.ws, e.tpl.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	if err := e.svc.Delete(ctx, e.ws, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, e.ws, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := e.svc.Delete(ctx, e.ws, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestRunDue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	t.Run("due schedule starts a run and advances", func(t *testing.T) {
		s := e.create(t, Input{})
		e.makeDue(t, s.ID, 5*time.Second)
		n, err := e.svc.RunDue(ctx, quiet)
		if err != nil || n != 1 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		got, _ := e.svc.Get(ctx, e.ws, s.ID)
		if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) || got.LastRunAt == nil {
			t.Errorf("after run: %+v", got)
		}
		var trig, by, sched string
		err = e.pool.QueryRow(ctx, "SELECT trigger, triggered_by::text, schedule_id::text FROM runs WHERE schedule_id = $1", s.ID).Scan(&trig, &by, &sched)
		if err != nil || trig != "schedule" || by != e.user.String() {
			t.Errorf("run row: %v %v %v %v", trig, by, sched, err)
		}
		if n, _ := e.svc.RunDue(ctx, quiet); n != 0 {
			t.Errorf("second tick started %d runs", n)
		}
	})

	t.Run("missed skip", func(t *testing.T) {
		s := e.create(t, Input{MissedPolicy: sp(PolicySkip)})
		e.makeDue(t, s.ID, time.Hour)
		if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 0 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		got, _ := e.svc.Get(ctx, e.ws, s.ID)
		if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) || got.LastRunAt != nil {
			t.Errorf("skip: %+v", got)
		}
		var c int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE schedule_id = $1", s.ID).Scan(&c)
		if c != 0 {
			t.Errorf("skip policy created %d runs", c)
		}
	})

	t.Run("missed run_once", func(t *testing.T) {
		s := e.create(t, Input{MissedPolicy: sp(PolicyRunOnce), Cron: sp("* * * * *")})
		e.makeDue(t, s.ID, 3*time.Hour) // 180 missed slots collapse into one run
		if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 1 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		var c int
		_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM runs WHERE schedule_id = $1", s.ID).Scan(&c)
		if c != 1 {
			t.Errorf("run_once created %d runs", c)
		}
	})

	t.Run("disabled is skipped", func(t *testing.T) {
		s := e.create(t, Input{})
		e.makeDue(t, s.ID, time.Second)
		if _, err := e.pool.Exec(ctx, "UPDATE schedules SET enabled = false WHERE id = $1", s.ID); err != nil {
			t.Fatal(err)
		}
		if n, _ := e.svc.RunDue(ctx, quiet); n != 0 {
			t.Errorf("disabled schedule started %d runs", n)
		}
	})

	t.Run("template deleted disables schedule", func(t *testing.T) {
		s := e.create(t, Input{})
		e.makeDue(t, s.ID, time.Second)
		// A template removed behind the scheduler's back (simulated: enqueue reports not found).
		e.svc.runs = notFoundRunner{e.svc.runs}
		defer func() { e.svc.runs = e.runs }()
		if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 0 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		got, _ := e.svc.Get(ctx, e.ws, s.ID)
		if got.Enabled || got.NextRunAt != nil {
			t.Errorf("schedule not disabled: %+v", got)
		}
	})

	t.Run("failing enqueue still advances", func(t *testing.T) {
		s := e.create(t, Input{})
		if _, err := e.pool.Exec(ctx, "UPDATE project_revisions SET scan = '{\"playbooks\":[]}'"); err != nil { // playbook vanished
			t.Fatal(err)
		}
		e.makeDue(t, s.ID, time.Second)
		if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 0 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		got, _ := e.svc.Get(ctx, e.ws, s.ID)
		if !got.Enabled || got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
			t.Errorf("not advanced: %+v", got)
		}
	})
}

type notFoundRunner struct{ Runner }

func (notFoundRunner) Enqueue(context.Context, uuid.UUID, uuid.UUID, runs.Overrides, string, runs.Actor, *uuid.UUID) (runs.Run, error) {
	return runs.Run{}, runs.ErrNotFound
}

func TestRunDueConcurrentClaim(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for range 5 {
		e.makeDue(t, e.create(t, Input{}).ID, time.Second)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := e.svc.RunDue(ctx, quiet)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	if total != 5 || e.runCount(t, "schedule") != 5 {
		t.Errorf("started %d runs, %d rows; want exactly 5", total, e.runCount(t, "schedule"))
	}
}

func sign(secret string, body []byte) func(string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	sig := "sha256=" + hex.EncodeToString(m.Sum(nil))
	return func(h string) string {
		if h == "X-ACP-Signature" {
			return sig
		}
		return ""
	}
}

func TestTemplateWebhook(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	none := func(string) string { return "" }

	if _, err := e.svc.HandleWebhook(ctx, e.tpl.ID, none, nil); !errors.Is(err, ErrRejected) {
		t.Fatalf("disabled webhook: %v", err)
	}
	wh, err := e.svc.RotateWebhook(ctx, e.ws, e.tpl.ID)
	if err != nil || !wh.Enabled || len(wh.Secret) < 32 {
		t.Fatalf("rotate: %+v %v", wh, err)
	}
	if got, _ := e.svc.GetWebhook(ctx, e.ws, e.tpl.ID); got.Secret != "" || !got.HasSecret || !got.Enabled {
		t.Errorf("GetWebhook leaks or misreports: %+v", got)
	}
	if _, err := e.svc.HandleWebhook(ctx, e.tpl.ID, sign("wrong", nil), nil); !errors.Is(err, ErrRejected) {
		t.Errorf("bad signature: %v", err)
	}
	if _, err := e.svc.HandleWebhook(ctx, uuid.New(), sign(wh.Secret, nil), nil); !errors.Is(err, ErrRejected) {
		t.Errorf("unknown template: %v", err)
	}
	run, err := e.svc.HandleWebhook(ctx, e.tpl.ID, sign(wh.Secret, nil), nil)
	if err != nil || run.Trigger != "webhook" || run.TriggeredBy != nil {
		t.Fatalf("webhook run: %+v %v", run, err)
	}
	// Overrides are only accepted when the template allows them.
	body := []byte(`{"extra_vars":{"x":1}}`)
	if _, err := e.svc.HandleWebhook(ctx, e.tpl.ID, sign(wh.Secret, body), body); !errors.Is(err, runs.ErrInvalid) {
		t.Errorf("overrides on locked template: %v", err)
	}
	body = []byte(`{"limit":"web1.example.test"}`)
	if run, err = e.svc.HandleWebhook(ctx, e.tpl.ID, sign(wh.Secret, body), body); err != nil || run.Params.Limit != "web1.example.test" {
		t.Errorf("limit override: %+v %v", run, err)
	}
	// Rotation invalidates the old secret.
	wh2, err := e.svc.RotateWebhook(ctx, e.ws, e.tpl.ID)
	if err != nil || wh2.Secret == wh.Secret {
		t.Fatalf("rotate again: %v", err)
	}
	if _, err := e.svc.HandleWebhook(ctx, e.tpl.ID, sign(wh.Secret, nil), nil); !errors.Is(err, ErrRejected) {
		t.Errorf("old secret still valid: %v", err)
	}
	if err := e.svc.DisableWebhook(ctx, e.ws, e.tpl.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.HandleWebhook(ctx, e.tpl.ID, sign(wh2.Secret, nil), nil); !errors.Is(err, ErrRejected) {
		t.Errorf("disabled webhook accepted: %v", err)
	}
	if e.runCount(t, "webhook") != 2 {
		t.Errorf("webhook runs = %d, want 2", e.runCount(t, "webhook"))
	}
}
