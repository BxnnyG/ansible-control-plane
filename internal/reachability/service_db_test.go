package reachability

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
	svc  *Service
	ws   uuid.UUID
	inv  uuid.UUID
	host uuid.UUID
}

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
	inv, err := q.CreateInventory(ctx, dbgen.CreateInventoryParams{WorkspaceID: ws.ID, Name: "i", Slug: "i", Mode: "db", SourceFormat: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := q.CreateInventoryHost(ctx, dbgen.CreateInventoryHostParams{InventoryID: inv.ID, Name: "src-a", Vars: []byte(`{}`)})
	if err != nil {
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
	rs := runs.New(runs.Options{DB: pool, Sealer: prov})
	return &env{pool: pool, q: q, ws: ws.ID, inv: inv.ID, host: h.ID, svc: New(Options{DB: pool, Runs: rs})}
}

func (e *env) configured(t *testing.T, interval int) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.svc.SetConfig(ctx, e.ws, ConfigInput{InventoryID: &e.inv, IntervalSeconds: interval}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateSource(ctx, e.ws, e.host); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateTarget(ctx, e.ws, TargetInput{Name: "gw", Address: "192.0.2.1", Check: CheckICMP}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) probeRuns(t *testing.T) []dbgen.Run {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), "SELECT id FROM runs WHERE workspace_id = $1 AND params->>'builtin' = 'probe'", e.ws)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []dbgen.Run
	for rows.Next() {
		var r dbgen.Run
		if err := rows.Scan(&r.ID); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestConfigValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	other := uuid.New()
	for name, in := range map[string]ConfigInput{
		"interval too small":  {InventoryID: &e.inv, IntervalSeconds: 5},
		"interval too large":  {InventoryID: &e.inv, IntervalSeconds: 86401},
		"enabled without inv": {IntervalSeconds: 60},
		"unknown inventory":   {InventoryID: &other, IntervalSeconds: 60},
		"unknown ssh secret":  {InventoryID: &e.inv, SSHSecretID: &other, IntervalSeconds: 60},
	} {
		if _, err := e.svc.SetConfig(ctx, e.ws, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	c, err := e.svc.GetConfig(ctx, e.ws)
	if err != nil || c.IntervalSeconds != 0 || c.InventoryID != nil {
		t.Fatalf("default config: %+v %v", c, err)
	}
	if _, err := e.svc.SetConfig(ctx, e.ws, ConfigInput{InventoryID: &e.inv, IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.svc.GetConfig(ctx, e.ws); c.NextRunAt == nil {
		t.Error("enabling the interval must make the first probe due")
	}
	if c, err := e.svc.SetConfig(ctx, e.ws, ConfigInput{InventoryID: &e.inv, IntervalSeconds: 0}); err != nil || c.NextRunAt != nil {
		t.Errorf("disable: %+v %v", c, err)
	}
	// Sources must belong to the probe inventory; a source blocks changing it.
	if _, err := e.svc.CreateSource(ctx, e.ws, uuid.New()); !errors.Is(err, ErrInvalid) {
		t.Errorf("foreign host: %v", err)
	}
	if _, err := e.svc.CreateSource(ctx, e.ws, e.host); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateSource(ctx, e.ws, e.host); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate source: %v", err)
	}
	if _, err := e.svc.SetConfig(ctx, e.ws, ConfigInput{}); !errors.Is(err, ErrConflict) {
		t.Errorf("changing inventory with sources: %v", err)
	}
}

func TestTargetCRUD(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tg, err := e.svc.CreateTarget(ctx, e.ws, TargetInput{Name: "ssh", Address: "192.0.2.5", Check: CheckTCP, Port: new(22)})
	if err != nil || !tg.Enabled || tg.Port == nil || *tg.Port != 22 {
		t.Fatalf("create: %+v %v", tg, err)
	}
	if _, err := e.svc.CreateTarget(ctx, e.ws, TargetInput{Name: "ssh", Address: "x", Check: CheckICMP}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	off := false
	up, err := e.svc.UpdateTarget(ctx, e.ws, tg.ID, TargetInput{Name: "ssh2", Address: "192.0.2.6", Check: CheckICMP, Enabled: &off})
	if err != nil || up.Enabled || up.Port != nil || up.Check != CheckICMP {
		t.Fatalf("update: %+v %v", up, err)
	}
	if _, err := e.svc.UpdateTarget(ctx, e.ws, uuid.New(), TargetInput{Name: "n", Address: "a", Check: CheckICMP}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	if err := e.svc.DeleteTarget(ctx, e.ws, tg.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteTarget(ctx, e.ws, tg.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestSchedulerEnqueuesProbe(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.configured(t, 0)

	// Off by default: interval 0 never enqueues.
	if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 0 {
		t.Fatalf("scheduler off: %d %v", n, err)
	}
	if _, err := e.svc.SetConfig(ctx, e.ws, ConfigInput{InventoryID: &e.inv, IntervalSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.RunDue(ctx, quiet); err != nil || n != 1 {
		t.Fatalf("first tick: %d %v", n, err)
	}
	rs := e.probeRuns(t)
	if len(rs) != 1 {
		t.Fatalf("%d probe runs", len(rs))
	}
	row, err := e.q.GetRunByID(ctx, rs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := runs.DecodeParams(row.Params)
	if row.TemplateID.Valid || row.ProjectID.Valid || row.Status != runs.StatusQueued || row.Trigger != runs.TriggerSchedule ||
		p.Builtin != runproto.BuiltinProbe || p.Playbook != runs.ProbePlaybook || p.Limit != "src-a" || p.InventoryID != e.inv {
		t.Errorf("run: %+v params %+v", row, p)
	}
	tg, ok := p.ExtraVars["acp_probe_targets"].([]any)
	if !ok || len(tg) != 1 || tg[0].(map[string]any)["check"] != "icmp" {
		t.Errorf("targets var: %v", p.ExtraVars)
	}
	// Advanced: not due again, and a queued probe is never doubled.
	if n, _ := e.svc.RunDue(ctx, quiet); n != 0 {
		t.Errorf("second tick enqueued %d", n)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE reachability_config SET next_run_at = now() - interval '1 second' WHERE workspace_id = $1", e.ws); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.svc.RunDue(ctx, quiet); n != 0 || len(e.probeRuns(t)) != 1 {
		t.Errorf("a probe run is still queued: enqueued %d, total %d", n, len(e.probeRuns(t)))
	}
	if c, _ := e.svc.GetConfig(ctx, e.ws); c.NextRunAt == nil || c.LastRunAt == nil || !c.NextRunAt.After(*c.LastRunAt) {
		t.Errorf("schedule not advanced: %+v", c)
	}
}

func TestRunNow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.svc.RunNow(ctx, e.ws, runs.Actor{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconfigured: %v", err)
	}
	e.configured(t, 0)
	r, err := e.svc.RunNow(ctx, e.ws, runs.Actor{})
	if err != nil || r.Trigger != runs.TriggerAPI || r.TemplateID != nil {
		t.Fatalf("run now: %+v %v", r, err)
	}
	if _, err := e.svc.RunNow(ctx, e.ws, runs.Actor{}); !errors.Is(err, ErrConflict) {
		t.Errorf("second run now: %v", err)
	}
}

func TestRunNowNeedsEnabledTargets(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.configured(t, 0)
	ts, _ := e.svc.ListTargets(ctx, e.ws)
	off := false
	if _, err := e.svc.UpdateTarget(ctx, e.ws, ts[0].ID, TargetInput{Name: ts[0].Name, Address: ts[0].Address, Check: ts[0].Check, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RunNow(ctx, e.ws, runs.Actor{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no enabled target: %v", err)
	}
}

func TestMatrixIngest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.configured(t, 0)
	run, err := e.svc.RunNow(ctx, e.ws, runs.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	tgts, _ := e.svc.ListTargets(ctx, e.ws)
	ig := Ingestor{Q: e.q, RunID: run.ID, WorkspaceID: e.ws, InventoryID: e.inv, Hosts: map[string]bool{"src-a": true}}
	payload := `{"` + tgts[0].ID.String() + `":{"status":"ok","latency_ms":7,"detail":""}}`
	if err := ig.StoreHost(ctx, "src-a", []byte(payload)); err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.GetMatrix(ctx, e.ws)
	if err != nil || len(m.Sources) != 1 || len(m.Targets) != 1 || len(m.Results) != 1 {
		t.Fatalf("matrix: %+v %v", m, err)
	}
	if r := m.Results[0]; r.Status != StatusOK || r.LatencyMS == nil || *r.LatencyMS != 7 || r.RunID == nil || *r.RunID != run.ID {
		t.Errorf("result: %+v", r)
	}
	if err := ig.MarkUnreachable(ctx, "src-a"); err != nil {
		t.Fatal(err)
	}
	if m, _ = e.svc.GetMatrix(ctx, e.ws); m.Results[0].Status != StatusUnreachable || m.Results[0].LatencyMS != nil {
		t.Errorf("after unreachable: %+v", m.Results[0])
	}
	// A schema violation is rejected and stores nothing.
	if err := ig.StoreHost(ctx, "src-a", []byte(`{"x":1}`)); !errors.Is(err, ErrPayload) {
		t.Errorf("bad payload: %v", err)
	}
}
