package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

type runFixture struct {
	q      *dbgen.Queries
	ws     dbgen.Workspace
	proj   dbgen.Project
	inv    dbgen.Inventory
	pool   *pgxpool.Pool
	suffix string
}

func newRunFixture(t *testing.T) runFixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	sfx := uuid.NewString()[:8]
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "rw-" + sfx, Name: "RW " + sfx})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{
		WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/x.git", DefaultRef: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := q.CreateInventory(ctx, dbgen.CreateInventoryParams{
		WorkspaceID: ws.ID, Name: "i", Slug: "i", Mode: "db", SourceFormat: "yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	return runFixture{q: q, ws: ws, proj: proj, inv: inv, pool: pool, suffix: sfx}
}

func (f runFixture) template(t *testing.T, slug string, maxConcurrent int32) dbgen.Template {
	t.Helper()
	tpl, err := f.q.CreateTemplate(context.Background(), dbgen.CreateTemplateParams{
		WorkspaceID: f.ws.ID, ProjectID: f.proj.ID, InventoryID: f.inv.ID,
		Name: slug, Slug: slug, Playbook: "site.yml",
		ExtraVars: []byte(`{}`), EnvVars: []byte(`{}`), MaxConcurrent: maxConcurrent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

func (f runFixture) enqueue(t *testing.T, tpl dbgen.Template) dbgen.Run {
	t.Helper()
	r, err := f.q.EnqueueRun(context.Background(), dbgen.EnqueueRunParams{
		WorkspaceID: f.ws.ID, TemplateID: pgtype.UUID{Bytes: tpl.ID, Valid: true}, TemplateName: tpl.Name,
		ProjectID: pgtype.UUID{Bytes: f.proj.ID, Valid: true}, ProjectRevision: "abc123",
		Trigger: "manual", Overrides: []byte(`{}`), Params: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaimRunConcurrencyLimit(t *testing.T) {
	f := newRunFixture(t)
	ctx := context.Background()
	limited := f.template(t, "limited", 1)
	wide := f.template(t, "wide", 2)
	a := f.enqueue(t, limited)
	b := f.enqueue(t, limited)
	c := f.enqueue(t, wide)

	// Many racing workers: only one run of "limited" may start, "wide" one run.
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := map[uuid.UUID]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := ClaimRun(ctx, f.pool, "w")
			if errors.Is(err, ErrNoRun) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			claimed[r.ID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(claimed) != 2 || !claimed[a.ID] || !claimed[c.ID] || claimed[b.ID] {
		t.Fatalf("unexpected claim set: %v", claimed)
	}
	if n, _ := f.q.CountRunningForTemplate(ctx, pgtype.UUID{Bytes: limited.ID, Valid: true}); n != 1 {
		t.Fatalf("running(limited)=%d", n)
	}

	// Finishing the running run frees the slot for the queued one.
	ra, _ := f.q.GetRunByID(ctx, a.ID)
	if _, err := f.q.FinishRun(ctx, dbgen.FinishRunParams{ID: a.ID, LeaseID: ra.LeaseID, Status: "succeeded", ExitCode: pgtype.Int4{Int32: 0, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	r, err := ClaimRun(ctx, f.pool, "w")
	if err != nil || r.ID != b.ID {
		t.Fatalf("expected second run after finish: %v", err)
	}
	if _, err := ClaimRun(ctx, f.pool, "w"); !errors.Is(err, ErrNoRun) {
		t.Fatalf("queue should be empty, got %v", err)
	}
}

func TestRunLostCancelHistory(t *testing.T) {
	f := newRunFixture(t)
	ctx := context.Background()
	tpl := f.template(t, "t1", 5)
	r1 := f.enqueue(t, tpl)
	r2 := f.enqueue(t, tpl)

	c1, err := ClaimRun(ctx, f.pool, "w1")
	if err != nil || c1.ID != r1.ID {
		t.Fatalf("oldest first: %v", err)
	}
	c2, err := ClaimRun(ctx, f.pool, "w2")
	if err != nil || c2.ID != r2.ID {
		t.Fatal(err)
	}
	// r1 heartbeat goes stale, r2 stays fresh.
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET last_heartbeat_at = now() - interval '2 minutes' WHERE id = $1`, r1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.HeartbeatRun(ctx, dbgen.HeartbeatRunParams{ID: r2.ID, LeaseID: c2.LeaseID}); err != nil {
		t.Fatal(err)
	}
	lost, err := f.q.MarkLostRuns(ctx, 60)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range lost {
		if l.ID == r2.ID {
			t.Fatal("fresh run must not be lost")
		}
		found = found || l.ID == r1.ID
	}
	if !found {
		t.Fatal("stale run must be marked lost")
	}
	if _, err := f.q.HeartbeatRun(ctx, dbgen.HeartbeatRunParams{ID: r1.ID, LeaseID: c1.LeaseID}); err == nil {
		t.Fatal("heartbeat on lost run must fail")
	}

	// Cancel request is visible in the heartbeat response.
	if _, err := f.q.RequestRunCancel(ctx, dbgen.RequestRunCancelParams{ID: r2.ID, WorkspaceID: f.ws.ID}); err != nil {
		t.Fatal(err)
	}
	hb, err := f.q.HeartbeatRun(ctx, dbgen.HeartbeatRunParams{ID: r2.ID, LeaseID: c2.LeaseID})
	if err != nil || !hb.Valid {
		t.Fatalf("cancel flag missing: %v", err)
	}

	// Host results, finish summary and history filters.
	for _, h := range []struct {
		host   string
		failed int32
	}{{"web1", 0}, {"web2", 1}} {
		if err := f.q.UpsertRunHostResult(ctx, dbgen.UpsertRunHostResultParams{
			RunID: r2.ID, Host: h.host, Ok: 3, Failed: h.failed, ChangedTasks: []byte(`[]`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fin, err := f.q.FinishRun(ctx, dbgen.FinishRunParams{ID: r2.ID, LeaseID: c2.LeaseID, Status: "failed", ExitCode: pgtype.Int4{Int32: 2, Valid: true}})
	if err != nil || fin.HostsOk != 1 || fin.HostsFailed != 1 {
		t.Fatalf("summary: %v ok=%d failed=%d", err, fin.HostsOk, fin.HostsFailed)
	}
	r3 := f.enqueue(t, tpl)
	if _, err := f.q.CancelQueuedRun(ctx, dbgen.CancelQueuedRunParams{ID: r3.ID, WorkspaceID: f.ws.ID}); err != nil {
		t.Fatal(err)
	}

	list := func(p dbgen.ListRunsParams) []dbgen.Run {
		p.WorkspaceID, p.PageSize = f.ws.ID, 10
		rs, err := f.q.ListRuns(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]dbgen.Run, len(rs))
		for i, r := range rs {
			out[i] = r.Run
		}
		return out
	}
	if n := len(list(dbgen.ListRunsParams{})); n != 3 {
		t.Fatalf("all=%d", n)
	}
	if rs := list(dbgen.ListRunsParams{Host: pgtype.Text{String: "web2", Valid: true}}); len(rs) != 1 || rs[0].ID != r2.ID {
		t.Fatalf("host filter: %d", len(rs))
	}
	if n := len(list(dbgen.ListRunsParams{Status: pgtype.Text{String: "lost", Valid: true}})); n != 1 {
		t.Fatalf("status filter=%d", n)
	}
	// Keyset pagination: page of 2 then the rest.
	p1, err := f.q.ListRuns(ctx, dbgen.ListRunsParams{WorkspaceID: f.ws.ID, PageSize: 2})
	if err != nil || len(p1) != 2 {
		t.Fatalf("page1: %v %d", err, len(p1))
	}
	p2, err := f.q.ListRuns(ctx, dbgen.ListRunsParams{
		WorkspaceID: f.ws.ID, PageSize: 2,
		CursorCreatedAt: p1[1].Run.CreatedAt, CursorID: pgtype.UUID{Bytes: p1[1].Run.ID, Valid: true},
	})
	if err != nil || len(p2) != 1 {
		t.Fatalf("page2: %v %d", err, len(p2))
	}
}

func TestRunLogRetentionAndSchedules(t *testing.T) {
	f := newRunFixture(t)
	ctx := context.Background()
	tpl := f.template(t, "t2", 1)
	done := f.enqueue(t, tpl)
	live := f.enqueue(t, tpl)
	for _, id := range []uuid.UUID{done.ID, live.ID} {
		for seq := int64(1); seq <= 3; seq++ {
			if n, err := f.q.AppendRunLogChunk(ctx, dbgen.AppendRunLogChunkParams{RunID: id, Seq: seq, Data: "line"}); err != nil || n != 1 {
				t.Fatalf("append: %v n=%d", err, n)
			}
		}
	}
	if n, _ := f.q.AppendRunLogChunk(ctx, dbgen.AppendRunLogChunkParams{RunID: done.ID, Seq: 3, Data: "dup"}); n != 0 {
		t.Fatal("duplicate seq must be ignored")
	}
	ch, err := f.q.ListRunLogChunks(ctx, dbgen.ListRunLogChunksParams{RunID: done.ID, Seq: 1, Limit: 10})
	if err != nil || len(ch) != 2 || ch[0].Seq != 2 {
		t.Fatalf("after seq: %v %d", err, len(ch))
	}
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET status='succeeded', finished_at = now() WHERE id = $1`, done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE run_log_chunks SET created_at = now() - interval '100 days' WHERE run_id IN ($1, $2)`, done.ID, live.ID); err != nil {
		t.Fatal(err)
	}
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-90 * 24 * time.Hour), Valid: true}
	if n, err := f.q.DeleteOldRunLogChunks(ctx, cutoff); err != nil || n < 3 {
		t.Fatalf("retention: %v n=%d", err, n)
	}
	if last, _ := f.q.LastRunLogSeq(ctx, live.ID); last != 3 {
		t.Fatal("chunks of unfinished runs must be kept")
	}
	if last, _ := f.q.LastRunLogSeq(ctx, done.ID); last != 0 {
		t.Fatal("old chunks of finished runs must be deleted")
	}
	if _, err := f.q.GetRunByID(ctx, done.ID); err != nil {
		t.Fatal("run row must be kept")
	}

	// Due schedules are claimed once and advanced.
	past := pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	sc, err := f.q.CreateSchedule(ctx, dbgen.CreateScheduleParams{
		WorkspaceID: f.ws.ID, TemplateID: tpl.ID, CronExpr: "*/5 * * * *", Timezone: "UTC",
		Enabled: true, MissedPolicy: "skip", Overrides: []byte(`{}`), NextRunAt: past,
	})
	if err != nil {
		t.Fatal(err)
	}
	tx1, _ := f.pool.Begin(ctx)
	defer func() { _ = tx1.Rollback(ctx) }()
	got, err := dbgen.New(tx1).ClaimDueSchedules(ctx, 10)
	if err != nil || !containsSchedule(got, sc.ID) {
		t.Fatalf("claim: %v", err)
	}
	tx2, _ := f.pool.Begin(ctx)
	defer func() { _ = tx2.Rollback(ctx) }()
	other, err := dbgen.New(tx2).ClaimDueSchedules(ctx, 10)
	if err != nil || containsSchedule(other, sc.ID) {
		t.Fatal("locked schedule must be skipped by a second claimer")
	}
	next := pgtype.Timestamptz{Time: time.Now().Add(5 * time.Minute), Valid: true}
	if err := dbgen.New(tx1).AdvanceSchedule(ctx, dbgen.AdvanceScheduleParams{ID: sc.ID, NextRunAt: next, LastRunAt: past}); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := f.q.GetSchedule(ctx, dbgen.GetScheduleParams{ID: sc.ID, WorkspaceID: f.ws.ID})
	if !after.LastRunAt.Valid || after.NextRunAt.Time.Before(time.Now()) {
		t.Fatal("schedule not advanced")
	}
}

func containsSchedule(list []dbgen.Schedule, id uuid.UUID) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}
