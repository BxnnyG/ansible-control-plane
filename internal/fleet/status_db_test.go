package fleet_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
)

// statusFixture creates one inventory with a template and returns their ids.
func (e *env) statusFixture(t *testing.T, hosts map[string]string) (inv, tpl uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var proj uuid.UUID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, name, slug, url) VALUES ($1, 'p', 'p', 'https://example.test/p.git') RETURNING id`, e.ws).Scan(&proj))
	must(e.pool.QueryRow(ctx, `INSERT INTO inventories (workspace_id, name, slug, mode) VALUES ($1, 'main', 'main', 'db') RETURNING id`, e.ws).Scan(&inv))
	must(e.pool.QueryRow(ctx, `INSERT INTO templates (workspace_id, project_id, inventory_id, name, slug, playbook) VALUES ($1, $2, $3, 'site', 'site', 'site.yml') RETURNING id`,
		e.ws, proj, inv).Scan(&tpl))
	for name, addr := range hosts {
		vars := `{}`
		if addr != "" {
			vars = `{"ansible_host":"` + addr + `"}`
		}
		_, err := e.pool.Exec(ctx, `INSERT INTO inventory_hosts (inventory_id, name, vars) VALUES ($1, $2, $3::jsonb)`, inv, name, vars)
		must(err)
	}
	return inv, tpl
}

// run records a finished run with one host result.
func (e *env) run(t *testing.T, tpl uuid.UUID, check bool, finished time.Time, host string, changed, failed, unreachable int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	params := `{"check":false}`
	if check {
		params = `{"check":true}`
	}
	status := "succeeded"
	if failed > 0 || unreachable > 0 {
		status = "failed"
	}
	err := e.pool.QueryRow(ctx, `INSERT INTO runs (workspace_id, template_id, template_name, status, trigger, params, finished_at)
		VALUES ($1, $2, 'site', $3, 'manual', $4::jsonb, $5) RETURNING id`, e.ws, tpl, status, params, finished).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO run_host_results (run_id, host, ok, changed, failed, unreachable) VALUES ($1, $2, 1, $3, $4, $5)`,
		id, host, changed, failed, unreachable)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) observeKind(t *testing.T, typ, key, kind string, f connectors.Fields, seen time.Time) {
	t.Helper()
	e.observe(t, typ, key, f, seen)
	if _, err := e.pool.Exec(context.Background(), `UPDATE host_observations SET kind = $3, last_seen = $4 WHERE connector_id = $1 AND external_key = $2`,
		e.conns[typ].ID, key, kind, seen); err != nil {
		t.Fatal(err)
	}
}

func TestHostStatusAndNetworkCounts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.observeKind(t, "opnsense", "a", connectors.KindLease, connectors.Fields{Hostname: "web1", IPs: []string{"192.0.2.10"}}, e.now.Add(-time.Minute))
	e.observeKind(t, "opnsense", "b", connectors.KindLease, connectors.Fields{Hostname: "db1", IPs: []string{"192.0.2.20"}}, e.now.Add(-time.Hour))
	e.observeKind(t, "proxmox", "c", connectors.KindVM, connectors.Fields{Hostname: "app1", IPs: []string{"192.0.2.30"}, Status: "running"}, e.now)
	e.observeKind(t, "opnsense", "d", connectors.KindStatic, connectors.Fields{Hostname: "printer", IPs: []string{"192.0.2.40"}}, e.now)
	e.observeKind(t, "opnsense", "e", connectors.KindLease, connectors.Fields{Hostname: "lone", IPs: []string{"192.0.2.50"}}, e.now)
	if err := e.q.UpsertNetwork(ctx, dbgen.UpsertNetworkParams{WorkspaceID: e.ws, ConnectorID: e.conns["opnsense"].ID, ExternalKey: "lan",
		Cidr: netip.MustParsePrefix("192.0.2.0/24"), Source: "test", FirstSeen: pgtype.Timestamptz{Time: e.now, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.merge(t)
	// web1 by name, db1 by ansible_host address, app1 by name; printer and lone are unmanaged.
	_, tpl := e.statusFixture(t, map[string]string{"web1": "", "database": "192.0.2.20", "app1": "", "never-seen": "203.0.113.9"})
	e.run(t, tpl, false, e.now.Add(-3*time.Hour), "web1", 0, 0, 0)
	e.run(t, tpl, false, e.now.Add(-2*time.Hour), "web1", 2, 0, 0)
	chk1 := e.run(t, tpl, true, e.now.Add(-90*time.Minute), "web1", 1, 0, 0)
	e.run(t, tpl, false, e.now.Add(-time.Hour), "database", 0, 0, 1)
	e.run(t, tpl, false, e.now.Add(-30*time.Minute), "app1", 0, 1, 0)
	e.run(t, tpl, true, e.now.Add(-20*time.Minute), "app1", 0, 0, 0)
	e.alert(t, "disk", "web1", "", "warning")
	e.merge(t)

	hosts, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]fleet.HostView{}
	for _, h := range hosts {
		by[h.Name] = h
	}
	web := by["web1"]
	if web.Online != fleet.StatusOnline || !web.Managed || len(web.Ansible) != 1 || web.Ansible[0].Host != "web1" || web.Ansible[0].Inventory != "main" {
		t.Errorf("web1 status = %+v", web)
	}
	if web.LastRun == nil || web.LastRun.Result != fleet.ResultChanged || !web.LastRun.Check || web.Reachable != fleet.ReachYes {
		t.Errorf("web1 last run = %+v reachable %s", web.LastRun, web.Reachable)
	}
	if web.Drift == nil || web.Drift.RunID != chk1 || web.Drift.Changed != 1 {
		t.Errorf("web1 drift = %+v", web.Drift)
	}
	if web.OpenAlerts != 1 {
		t.Errorf("web1 alerts = %d", web.OpenAlerts)
	}
	db := by["db1"]
	if db.Online != fleet.StatusOffline || !db.Managed || db.Reachable != fleet.ReachNo || db.LastRun == nil || db.LastRun.Result != fleet.ResultUnreachable {
		t.Errorf("db1 status = %+v", db)
	}
	app := by["app1"]
	if app.Online != fleet.StatusOnline || app.LastRun == nil || app.LastRun.Result != fleet.ResultOK || app.Drift != nil {
		t.Errorf("app1 status = %+v (newest check run was clean, so no drift)", app)
	}
	if p := by["printer"]; p.Online != fleet.StatusUnknown || p.Managed || p.Reachable != fleet.ReachUnknown || p.LastRun != nil || len(p.Ansible) != 0 {
		t.Errorf("printer status = %+v", p)
	}

	no := false
	un, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Managed: &no}, nil)
	if err != nil || len(un) != 2 {
		t.Fatalf("unmanaged = %d (%v), want 2", len(un), err)
	}
	yes := true
	if m, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Managed: &yes}, nil); err != nil || len(m) != 3 {
		t.Fatalf("managed = %d (%v), want 3", len(m), err)
	}

	d, err := e.store.Host(ctx, e.ws, web.ID, nil)
	if err != nil || d.Drift == nil || d.Online != fleet.StatusOnline || !d.Managed {
		t.Fatalf("detail status = %+v (%v)", d.HostView, err)
	}

	nets, err := e.store.Networks(ctx, e.ws, nil, nil)
	if err != nil || len(nets) != 1 {
		t.Fatalf("networks = %d (%v)", len(nets), err)
	}
	c := nets[0].Counts
	// online: web1, app1, lone; ok: web1 (changed), app1; failed: db1 (unreachable); never managed: printer, lone.
	if nets[0].HostCount != 5 || c.Online != 3 || c.AnsibleOK != 2 || c.Failed != 1 || c.NeverManaged != 2 || c.OpenAlerts != 1 {
		t.Errorf("counts = %+v hosts %d", c, nets[0].HostCount)
	}
}

func TestAddToInventory(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.observeKind(t, "opnsense", "a", connectors.KindLease, connectors.Fields{Hostname: "printer", IPs: []string{"2001:db8::5", "192.0.2.40"}}, e.now)
	e.observeKind(t, "opnsense", "b", connectors.KindLease, connectors.Fields{Hostname: "web1", IPs: []string{"192.0.2.10"}}, e.now)
	e.observeKind(t, "opnsense", "c", connectors.KindLease, connectors.Fields{Hostname: "far", IPs: []string{"198.51.100.7"}}, e.now)
	e.merge(t)
	inv, _ := e.statusFixture(t, map[string]string{"web1": "", "taken": ""})
	ids := e.hostIDs(t)
	add := fleet.Adder{Fleet: e.store, Inventories: inventory.New(inventory.Options{DB: e.pool})}

	// Linked hosts and unknown groups/names are refused.
	if _, err := add.AddToInventory(ctx, e.ws, ids["web1"], fleet.AddInput{InventoryID: inv, Group: "web"}, nil); !errors.Is(err, fleet.ErrManaged) {
		t.Errorf("managed host: %v", err)
	}
	if _, err := add.AddToInventory(ctx, e.ws, ids["printer"], fleet.AddInput{InventoryID: inv}, nil); !errors.Is(err, fleet.ErrInvalid) {
		t.Errorf("no group: %v", err)
	}
	if _, err := add.AddToInventory(ctx, e.ws, ids["printer"], fleet.AddInput{InventoryID: inv, Group: "web", Name: "taken"}, nil); !errors.Is(err, fleet.ErrNameTaken) {
		t.Errorf("name taken: %v", err)
	}
	only := func(ip string) fleet.Visible {
		return func(ips []string) bool { return slices.Contains(ips, ip) }
	}
	if _, err := add.AddToInventory(ctx, e.ws, ids["far"], fleet.AddInput{InventoryID: inv, Group: "web"}, only("192.0.2.10")); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("invisible host: %v", err)
	}

	res, err := add.AddToInventory(ctx, e.ws, ids["printer"], fleet.AddInput{InventoryID: inv, Group: "web"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Host != "printer" || res.AnsibleHost != "192.0.2.40" || !res.Result.GroupCreated || len(res.Result.HostsCreated) != 1 {
		t.Errorf("result: %+v", res)
	}
	var vars string
	if err := e.pool.QueryRow(ctx, `SELECT vars::text FROM inventory_hosts WHERE inventory_id = $1 AND name = 'printer'`, inv).Scan(&vars); err != nil || !strings.Contains(vars, "192.0.2.40") {
		t.Errorf("stored vars %q (%v)", vars, err)
	}
	// The host is now managed and drops out of the unmanaged list.
	no := false
	list, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Managed: &no}, nil)
	if err != nil || len(list) != 1 || list[0].Name != "far" {
		t.Errorf("unmanaged after add: %v (%v)", list, err)
	}
	// Custom name without matching the fleet name still writes the address.
	res, err = add.AddToInventory(ctx, e.ws, ids["far"], fleet.AddInput{InventoryID: inv, Group: "web", Name: "far-away"}, nil)
	if err != nil || res.Host != "far-away" || res.AnsibleHost != "198.51.100.7" || res.Result.GroupCreated {
		t.Errorf("custom name: %+v (%v)", res, err)
	}
}

func TestPrimaryAddress(t *testing.T) {
	tests := []struct {
		ips  []string
		want string
	}{
		{nil, ""}, {[]string{"2001:db8::1"}, "2001:db8::1"}, {[]string{"2001:db8::1", "192.0.2.7"}, "192.0.2.7"},
		{[]string{"nonsense", "::ffff:192.0.2.9"}, "192.0.2.9"},
	}
	for _, tt := range tests {
		if got := fleet.PrimaryAddress(tt.ips); got != tt.want {
			t.Errorf("PrimaryAddress(%v) = %q, want %q", tt.ips, got, tt.want)
		}
	}
}
