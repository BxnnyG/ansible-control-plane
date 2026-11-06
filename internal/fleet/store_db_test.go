package fleet_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

const (
	m1 = "02:00:00:00:00:01"
	m2 = "02:00:00:00:00:02"
)

type env struct {
	pool  *pgxpool.Pool
	q     *dbgen.Queries
	store *fleet.Store
	ws    uuid.UUID
	now   time.Time
	conns map[string]dbgen.Connector
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
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "f" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), Name: "F"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	e := &env{pool: pool, q: q, ws: ws.ID, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), conns: map[string]dbgen.Connector{}}
	e.store = fleet.NewStore(pool, func() time.Time { return e.now })
	for _, typ := range []string{"opnsense", "proxmox"} {
		c, err := q.CreateConnector(ctx, dbgen.CreateConnectorParams{WorkspaceID: ws.ID, Type: typ, Name: typ + "-1", Config: []byte(`{}`), IntervalSeconds: 300, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		e.conns[typ] = c
	}
	return e
}

func (e *env) observe(t *testing.T, typ, key string, f connectors.Fields, seen time.Time) {
	t.Helper()
	fb, _ := json.Marshal(f)
	err := e.q.UpsertObservation(context.Background(), dbgen.UpsertObservationParams{WorkspaceID: e.ws, ConnectorID: e.conns[typ].ID,
		ExternalKey: key, Kind: connectors.KindStatic, Fields: fb, FirstSeen: pgtype.Timestamptz{Time: seen, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) forget(t *testing.T, typ, key string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM host_observations WHERE connector_id = $1 AND external_key = $2`, e.conns[typ].ID, key); err != nil {
		t.Fatal(err)
	}
}

func (e *env) merge(t *testing.T) fleet.Summary {
	t.Helper()
	s, err := e.store.Merge(context.Background(), e.ws)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) hostIDs(t *testing.T) map[string]uuid.UUID {
	t.Helper()
	hosts, err := e.q.ListHosts(context.Background(), e.ws)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uuid.UUID{}
	for _, h := range hosts {
		out[h.Name] = h.ID
	}
	return out
}

func (e *env) conflicts(t *testing.T, state string) []dbgen.HostConflict {
	t.Helper()
	all, err := e.q.ListHostConflicts(context.Background(), e.ws)
	if err != nil {
		t.Fatal(err)
	}
	var out []dbgen.HostConflict
	for _, c := range all {
		if c.State == state {
			out = append(out, c)
		}
	}
	return out
}

func TestMergeStableIDsAcrossRemerge(t *testing.T) {
	e := setup(t)
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.10"}}, e.now)
	e.observe(t, "proxmox", "b", connectors.Fields{Hostname: "web1", IPs: []string{"192.0.2.10"}}, e.now)
	e.observe(t, "opnsense", "c", connectors.Fields{MACs: []string{m2}, Hostname: "db1"}, e.now)
	if s := e.merge(t); s.Hosts != 2 || s.Created != 2 {
		t.Fatalf("first merge = %+v", s)
	}
	first := e.hostIDs(t)
	if len(first) != 2 || first["web1"] == uuid.Nil || first["db1"] == uuid.Nil {
		t.Fatalf("hosts = %v", first)
	}

	// Re-merge without changes keeps ids.
	if s := e.merge(t); s.Created != 0 || s.Deleted != 0 {
		t.Fatalf("second merge = %+v", s)
	}
	if got := e.hostIDs(t); got["web1"] != first["web1"] || got["db1"] != first["db1"] {
		t.Fatalf("ids changed: %v vs %v", got, first)
	}

	// The IP changes and a name is renamed: the MAC keeps the identity.
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.99"}}, e.now)
	e.forget(t, "proxmox", "b")
	e.observe(t, "proxmox", "b2", connectors.Fields{MACs: []string{m1}, Hostname: "web1-renamed"}, e.now)
	e.merge(t)
	got := e.hostIDs(t)
	if got["web1-renamed"] != first["web1"] || got["db1"] != first["db1"] {
		t.Fatalf("ids after change: %v vs %v", got, first)
	}

	// Host keys follow the merge.
	keys, err := e.q.ListHostKeys(context.Background(), e.ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.HostID == first["web1"] && k.Kind == "ip" && k.Value == "192.0.2.10" {
			t.Errorf("stale key kept: %+v", k)
		}
	}

	// A host whose observations are gone is deleted.
	e.forget(t, "opnsense", "c")
	if s := e.merge(t); s.Deleted != 1 {
		t.Fatalf("delete merge = %+v", s)
	}
	if got := e.hostIDs(t); len(got) != 1 || got["web1-renamed"] != first["web1"] {
		t.Fatalf("hosts after delete = %v", got)
	}
}

func TestMergeIDReuseOldestOnJoin(t *testing.T) {
	e := setup(t)
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, Hostname: "old"}, e.now.Add(-2*time.Hour))
	e.merge(t)
	older := e.hostIDs(t)["old"]
	e.observe(t, "opnsense", "b", connectors.Fields{MACs: []string{m2}, Hostname: "new"}, e.now)
	e.merge(t)
	if len(e.hostIDs(t)) != 2 {
		t.Fatal("want two hosts")
	}
	// A dual-MAC observation joins both hosts; the older id survives.
	e.observe(t, "proxmox", "vm", connectors.Fields{MACs: []string{m1, m2}, Hostname: "joined"}, e.now)
	if s := e.merge(t); s.Hosts != 1 || s.Deleted != 1 {
		t.Fatalf("join = %+v", s)
	}
	if got := e.hostIDs(t); len(got) != 1 {
		t.Fatalf("hosts = %v", got)
	} else {
		for _, id := range got {
			if id != older {
				t.Errorf("id = %s, want oldest %s", id, older)
			}
		}
	}
}

func TestConflictLifecycle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.5"}}, e.now)
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}}, e.now)
	if s := e.merge(t); s.ConflictsOpen != 1 || s.ConflictsOpened != 1 {
		t.Fatalf("merge = %+v", s)
	}
	open := e.conflicts(t, "open")
	if len(open) != 1 || open[0].Type != fleet.ConflictIPMAC || len(open[0].HostIds) != 2 {
		t.Fatalf("open = %+v", open)
	}
	if strings.Contains(string(open[0].Details), "token") {
		t.Errorf("details = %s", open[0].Details)
	}

	// Acknowledge, then re-merge: the state survives and nothing new opens.
	n, err := e.q.AcknowledgeHostConflict(ctx, dbgen.AcknowledgeHostConflictParams{ID: open[0].ID, WorkspaceID: e.ws,
		AcknowledgedAt: pgtype.Timestamptz{Time: e.now, Valid: true}})
	if err != nil || n != 1 {
		t.Fatalf("ack = %d %v", n, err)
	}
	e.now = e.now.Add(time.Minute)
	if s := e.merge(t); s.ConflictsOpened != 0 || s.ConflictsResolved != 0 {
		t.Fatalf("remerge = %+v", s)
	}
	open = e.conflicts(t, "open")
	if len(open) != 1 || !open[0].AcknowledgedAt.Valid || !open[0].LastSeen.Time.Equal(e.now) {
		t.Fatalf("after remerge = %+v", open)
	}
	id := open[0].ID

	// The condition disappears: resolved with a timestamp.
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.6"}}, e.now)
	e.now = e.now.Add(time.Minute)
	if s := e.merge(t); s.ConflictsOpen != 0 || s.ConflictsResolved != 1 {
		t.Fatalf("resolve = %+v", s)
	}
	if len(e.conflicts(t, "open")) != 0 {
		t.Fatal("conflict still open")
	}
	res := e.conflicts(t, "resolved")
	if len(res) != 1 || res[0].ID != id || !res[0].ResolvedAt.Valid || !res[0].AcknowledgedAt.Valid {
		t.Fatalf("resolved = %+v", res)
	}

	// It comes back: same row reopens, acknowledgement is cleared.
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}}, e.now)
	e.now = e.now.Add(time.Minute)
	if s := e.merge(t); s.ConflictsOpened != 1 {
		t.Fatalf("reopen = %+v", s)
	}
	open = e.conflicts(t, "open")
	if len(open) != 1 || open[0].ID != id || open[0].AcknowledgedAt.Valid || open[0].ResolvedAt.Valid {
		t.Fatalf("reopened = %+v", open)
	}
}

func TestDisappearedAndPriorityOverride(t *testing.T) {
	e := setup(t)
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, Hostname: "opn-name"}, e.now.Add(-30*time.Hour))
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m1}, Hostname: "pve-name"}, e.now.Add(-30*time.Hour))
	e.merge(t)
	open := e.conflicts(t, "open")
	if len(open) != 1 || open[0].Type != fleet.ConflictDisappeared {
		t.Fatalf("open = %+v", open)
	}
	if got := e.hostIDs(t); len(got) != 1 || got["pve-name"] == uuid.Nil {
		t.Fatalf("default priority hosts = %v", got)
	}

	// Raising the OPNsense connector's priority flips the winner.
	if _, err := e.pool.Exec(context.Background(), `UPDATE connectors SET config = '{"priority": 900}' WHERE id = $1`, e.conns["opnsense"].ID); err != nil {
		t.Fatal(err)
	}
	e.merge(t)
	if got := e.hostIDs(t); len(got) != 1 || got["opn-name"] == uuid.Nil {
		t.Fatalf("override hosts = %v", got)
	}

	// The host reappears: the disappeared conflict resolves.
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m1}, Hostname: "pve-name"}, e.now)
	e.merge(t)
	if len(e.conflicts(t, "open")) != 0 || len(e.conflicts(t, "resolved")) != 1 {
		t.Fatal("disappeared conflict not resolved")
	}
}
