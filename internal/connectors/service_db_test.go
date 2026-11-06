package connectors_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/alertwebhook"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/fake"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const apiToken = "tok-super-secret-value"

type env struct {
	svc    *connectors.Service
	pool   *pgxpool.Pool
	q      *dbgen.Queries
	script *fake.Script
	ws     uuid.UUID
	secret uuid.UUID
	synced atomic.Int32
	linked atomic.Int32
	sec    *secrets.Service
}

type linkFunc func(context.Context, uuid.UUID) error

func (f linkFunc) LinkAlerts(ctx context.Context, ws uuid.UUID) error { return f(ctx, ws) }

func setup(t *testing.T, timeout time.Duration) *env {
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
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "c" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), Name: "C"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	sp, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewService(q, sp)
	meta, err := sec.Create(ctx, ws.ID, secrets.CreateInput{Name: "tok", Type: secrets.TypeAPIToken, Value: []byte(apiToken)})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, q: q, script: &fake.Script{}, ws: ws.ID, secret: meta.ID}
	reg := connectors.NewRegistry()
	fake.Register(reg, e.script)
	alertwebhook.Register(reg)
	e.sec = sec
	e.svc = connectors.New(connectors.Options{DB: pool, Registry: reg, Secrets: sec, SecretWriter: sec, Timeout: timeout,
		Alerts:   linkFunc(func(context.Context, uuid.UUID) error { e.linked.Add(1); return nil }),
		PostSync: connectors.PostSyncFunc(func(context.Context, uuid.UUID, uuid.UUID) error { e.synced.Add(1); return nil })})
	return e
}

func (e *env) create(t *testing.T, name string, interval int) dbgen.Connector {
	t.Helper()
	sid := e.secret
	c, err := e.svc.Create(context.Background(), e.ws, connectors.CreateInput{Type: fake.Type, Name: name, SecretID: &sid, IntervalSeconds: interval, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func vlan(n int) *int { return &n }

func TestCreateValidation(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c := e.create(t, "a", 60)
	if c.IntervalSeconds != 60 || !c.Enabled || c.NextSyncAt.Time.After(time.Now().Add(time.Second)) {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if _, err := e.svc.Create(ctx, e.ws, connectors.CreateInput{Type: fake.Type, Name: "a", Enabled: true}); !errors.Is(err, connectors.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.ws, connectors.CreateInput{Type: "nope", Name: "b"}); !errors.Is(err, connectors.ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.ws, connectors.CreateInput{Type: fake.Type, Name: "b", IntervalSeconds: 5}); !errors.Is(err, connectors.ErrInvalidConfig) {
		t.Errorf("short interval: %v", err)
	}
	other := uuid.New()
	if _, err := e.svc.Create(ctx, e.ws, connectors.CreateInput{Type: fake.Type, Name: "b", SecretID: &other}); !errors.Is(err, connectors.ErrInvalidConfig) {
		t.Errorf("foreign secret: %v", err)
	}
}

func TestSyncStoresAndReplacesSet(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c := e.create(t, "s", 60)
	e.script.Set(connectors.Result{
		Networks: []connectors.Network{{ExternalKey: "lan", CIDR: netip.MustParsePrefix("192.0.2.5/24"), VLAN: vlan(10), Interface: "lan0"}},
		Observations: []connectors.HostObservation{
			{ExternalKey: "m1", Kind: connectors.KindLease, Fields: connectors.Fields{MACs: []string{"02:00:00:00:00:01"}, IPs: []string{"192.0.2.10"}}},
			{ExternalKey: "m2", Kind: connectors.KindVM, Fields: connectors.Fields{Hostname: "vm2"}},
		},
		Alerts: []connectors.Alert{{ExternalKey: "a1", Host: "x", Severity: connectors.SeverityWarning, Summary: "m"}},
	})
	counts, err := e.svc.Run(ctx, c, connectors.TriggerManual)
	if err != nil || counts.Networks != 1 || counts.Observations != 2 || counts.Alerts != 1 {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
	if e.synced.Load() != 1 {
		t.Errorf("post-sync hook calls=%d", e.synced.Load())
	}
	nets, _ := e.q.ListNetworks(ctx, e.ws)
	if len(nets) != 1 || nets[0].Cidr.String() != "192.0.2.0/24" || nets[0].Vlan.Int32 != 10 {
		t.Fatalf("networks=%+v", nets)
	}
	obs, _ := e.q.ListObservations(ctx, e.ws)
	if len(obs) != 2 {
		t.Fatalf("observations=%d", len(obs))
	}
	first := obs[0].FirstSeen.Time

	// Second sync reports only m1 (changed) and age-marks m2 as old.
	if _, err := e.pool.Exec(ctx, "UPDATE host_observations SET last_seen = now() - interval '31 days', first_seen = now() - interval '40 days' WHERE external_key = 'm2'"); err != nil {
		t.Fatal(err)
	}
	e.script.Set(connectors.Result{Observations: []connectors.HostObservation{
		{ExternalKey: "m1", Kind: connectors.KindStatic, Fields: connectors.Fields{MACs: []string{"02:00:00:00:00:01"}, Hostname: "h1"}},
	}})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	obs, _ = e.q.ListObservations(ctx, e.ws)
	if len(obs) != 1 || obs[0].ExternalKey != "m1" || obs[0].Kind != connectors.KindStatic || !strings.Contains(string(obs[0].Fields), "h1") {
		t.Fatalf("after replace: %+v", obs)
	}
	if !obs[0].FirstSeen.Time.Equal(first) {
		t.Error("first_seen must be preserved")
	}
	nets, _ = e.q.ListNetworks(ctx, e.ws)
	if len(nets) != 1 {
		t.Errorf("network not reported again must age out only after 30 days, got %d", len(nets))
	}

	got, _ := e.svc.Get(ctx, e.ws, c.ID)
	if got.LastStatus != "ok" || got.BackoffCount != 0 || !got.LastSuccessAt.Valid || got.LastError != "" || !strings.Contains(string(got.LastCounts), `"observations": 1`) { // jsonb normalizes spacing
		t.Errorf("connector status: %+v", got)
	}
	if d := got.NextSyncAt.Time.Sub(got.LastSyncAt.Time); d < 59*time.Second || d > 61*time.Second {
		t.Errorf("next sync in %s, want ~60s", d)
	}
	syncs, _ := e.q.ListConnectorSyncs(ctx, dbgen.ListConnectorSyncsParams{ConnectorID: c.ID, WorkspaceID: e.ws, Limit: 10})
	if len(syncs) != 2 || syncs[0].Status != "ok" {
		t.Errorf("syncs=%+v", syncs)
	}
}

func TestFailureBackoffAndSecretMasking(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c := e.create(t, "f", 60)
	e.script.Set(connectors.Result{Observations: []connectors.HostObservation{{ExternalKey: "keep", Kind: connectors.KindNode}}})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerManual); err != nil {
		t.Fatal(err)
	}
	e.script.Fail(errors.New("auth failed with " + apiToken))
	for i, wantDelay := range []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second} {
		cur, _ := e.svc.Get(ctx, e.ws, c.ID)
		_, err := e.svc.Run(ctx, cur, connectors.TriggerSchedule)
		if err == nil || strings.Contains(err.Error(), apiToken) {
			t.Fatalf("run %d: err=%v", i, err)
		}
		got, _ := e.svc.Get(ctx, e.ws, c.ID)
		if got.LastStatus != "error" || int(got.BackoffCount) != i+1 || strings.Contains(got.LastError, apiToken) || !strings.Contains(got.LastError, "***") {
			t.Fatalf("run %d: status %+v", i, got)
		}
		if d := got.NextSyncAt.Time.Sub(got.LastSyncAt.Time); d < wantDelay-time.Second || d > wantDelay+time.Second {
			t.Errorf("run %d: delay %s want %s", i, d, wantDelay)
		}
		if !got.LastSuccessAt.Valid {
			t.Error("last_success_at must survive failures")
		}
	}
	obs, _ := e.q.ListObservations(ctx, e.ws)
	if len(obs) != 1 {
		t.Errorf("failed sync must keep previous observations, have %d", len(obs))
	}
	if e.synced.Load() != 1 {
		t.Errorf("post-sync hook must only run after success, calls=%d", e.synced.Load())
	}
	e.script.Set(connectors.Result{})
	cur, _ := e.svc.Get(ctx, e.ws, c.ID)
	if _, err := e.svc.Run(ctx, cur, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.Get(ctx, e.ws, c.ID); got.BackoffCount != 0 || got.LastStatus != "ok" {
		t.Errorf("success must reset backoff: %+v", got)
	}
	// Invalid result is a failed sync and writes nothing.
	e.script.Set(connectors.Result{Observations: []connectors.HostObservation{{ExternalKey: "x", Kind: "bogus"}}})
	cur, _ = e.svc.Get(ctx, e.ws, c.ID)
	if _, err := e.svc.Run(ctx, cur, connectors.TriggerSchedule); err == nil {
		t.Error("invalid result accepted")
	}
}

func TestClaimDueIsExclusive(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	due := e.create(t, "due", 60)
	off := e.create(t, "off", 60)
	if _, err := e.pool.Exec(ctx, "UPDATE connectors SET enabled = false WHERE id = $1", off.ID); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.svc.ClaimDue(ctx, 100)
			if err != nil {
				t.Error(err)
				return
			}
			for _, c := range got {
				switch c.ID {
				case off.ID:
					t.Error("disabled connector claimed")
				case due.ID:
					claims.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("claimed %d times, want 1", claims.Load())
	}
	// Manual sync request makes it due again.
	if err := e.svc.RequestSync(ctx, e.ws, due.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.ClaimDue(ctx, 100)
	found := false
	for _, c := range got {
		found = found || c.ID == due.ID
	}
	if !found {
		t.Error("requested sync not claimed")
	}
	if err := e.svc.RequestSync(ctx, e.ws, uuid.New()); !errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestRunDueTimeoutAndTest(t *testing.T) {
	e := setup(t, 300*time.Millisecond)
	ctx := context.Background()
	c := e.create(t, "slow", 60)
	if err := e.svc.Test(ctx, e.ws, c.ID); err != nil {
		t.Fatal(err)
	}
	e.script.Fail(errors.New("down " + apiToken))
	if err := e.svc.Test(ctx, e.ws, c.ID); err == nil || strings.Contains(err.Error(), apiToken) {
		t.Errorf("test error not masked: %v", err)
	}
	e.script.Set(connectors.Result{})
	release := e.script.Block()
	defer release()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.svc.RunDue(ctx, 2, log)
	got, _ := e.svc.Get(ctx, e.ws, c.ID)
	if got.LastStatus != "error" || !strings.Contains(got.LastError, "timed out") || got.BackoffCount != 1 {
		t.Errorf("timeout not recorded: %+v", got)
	}
	if e.script.Calls() != 1 {
		t.Errorf("calls=%d", e.script.Calls())
	}
	// Leased/backed off: a second tick must not pick it up again.
	e.svc.RunDue(ctx, 2, log)
	if e.script.Calls() != 1 {
		t.Errorf("connector synced again before it was due, calls=%d", e.script.Calls())
	}
}

func TestUpdateDeleteAndSyncs(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c := e.create(t, "u", 60)
	e.script.Set(connectors.Result{})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerManual); err != nil {
		t.Fatal(err)
	}
	// Push the next sync into the future, then a connection change makes it due.
	if _, err := e.pool.Exec(ctx, `UPDATE connectors SET next_sync_at = now() + interval '1 hour' WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	name, secs := "renamed", 120
	u, err := e.svc.Update(ctx, e.ws, c.ID, connectors.UpdateInput{Name: &name, IntervalSeconds: &secs, Config: map[string]any{"note": "x"}})
	if err != nil || u.Name != "renamed" || u.IntervalSeconds != 120 || !u.SecretID.Valid {
		t.Fatalf("update: %+v %v", u, err)
	}
	if got, _ := e.svc.Get(ctx, e.ws, c.ID); got.NextSyncAt.Time.After(time.Now().Add(time.Minute)) {
		t.Error("config change must make the connector due")
	}
	if u, err = e.svc.Update(ctx, e.ws, c.ID, connectors.UpdateInput{ClearSecret: true}); err != nil || u.SecretID.Valid {
		t.Fatalf("clear secret: %+v %v", u, err)
	}
	other := uuid.New()
	for name, in := range map[string]connectors.UpdateInput{
		"foreign secret": {SecretID: &other},
		"short interval": {IntervalSeconds: new(int)},
		"unknown option": {Config: map[string]any{"bogus": 1}},
		"empty name":     {Name: new(string)},
	} {
		if _, err := e.svc.Update(ctx, e.ws, c.ID, in); !errors.Is(err, connectors.ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e.create(t, "dup", 60)
	dup := "dup"
	if _, err := e.svc.Update(ctx, e.ws, c.ID, connectors.UpdateInput{Name: &dup}); !errors.Is(err, connectors.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := e.svc.Update(ctx, e.ws, uuid.New(), connectors.UpdateInput{Name: &name}); !errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("unknown connector: %v", err)
	}

	syncs, err := e.svc.ListSyncs(ctx, e.ws, c.ID, 10)
	if err != nil || len(syncs) != 1 || syncs[0].Trigger != connectors.TriggerManual {
		t.Fatalf("syncs %+v %v", syncs, err)
	}
	if _, err := e.svc.ListSyncs(ctx, e.ws, uuid.New(), 10); !errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("syncs of unknown connector: %v", err)
	}
	refs, err := e.svc.SecretRefs(ctx, e.ws)
	if err != nil || refs[e.secret].Name != "tok" || refs[e.secret].Type != secrets.TypeAPIToken {
		t.Errorf("secret refs %+v %v", refs, err)
	}

	before := e.synced.Load()
	if err := e.svc.Delete(ctx, e.ws, c.ID); err != nil {
		t.Fatal(err)
	}
	if e.synced.Load() != before+1 {
		t.Error("delete must re-merge the fleet")
	}
	if err := e.svc.Delete(ctx, e.ws, c.ID); !errors.Is(err, connectors.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestTestConfig(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	sid := e.secret
	in := connectors.TestInput{Type: fake.Type, SecretID: &sid, Config: map[string]any{"note": "n"}}
	e.script.Set(connectors.Result{})
	if err := e.svc.TestConfig(ctx, e.ws, in); err != nil {
		t.Errorf("probe: %v", err)
	}
	if list, _ := e.svc.List(ctx, e.ws); len(list) != 0 {
		t.Error("an unsaved test must store nothing")
	}
	e.script.Fail(errors.New("auth failed for " + apiToken))
	if err := e.svc.TestConfig(ctx, e.ws, in); err == nil || strings.Contains(err.Error(), apiToken) {
		t.Errorf("probe failure must be reported with the secret masked: %v", err)
	}
	other := uuid.New()
	for name, bad := range map[string]connectors.TestInput{
		"unknown type":   {Type: "nope"},
		"foreign secret": {Type: fake.Type, SecretID: &other},
		"bad option":     {Type: fake.Type, Config: map[string]any{"bogus": true}},
	} {
		err := e.svc.TestConfig(ctx, e.ws, bad)
		if !errors.Is(err, connectors.ErrInvalidConfig) && !errors.Is(err, connectors.ErrUnknownType) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
