package fleet_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func (e *env) alert(t *testing.T, key, host, ip, sev string) {
	t.Helper()
	err := e.q.UpsertAlert(context.Background(), dbgen.UpsertAlertParams{WorkspaceID: e.ws, ConnectorID: e.conns["proxmox"].ID, ExternalKey: key,
		HostName: host, HostIp: ip, Severity: sev, Summary: key, Labels: []byte(`{}`),
		StartedAt: pgtype.Timestamptz{Time: e.now, Valid: true}, LastSeenAt: pgtype.Timestamptz{Time: e.now, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) linked(t *testing.T) map[string]*uuid.UUID {
	t.Helper()
	list, err := e.store.Alerts(context.Background(), e.ws, fleet.AlertFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*uuid.UUID{}
	for _, a := range list {
		out[a.ExternalKey] = a.HostID
	}
	return out
}

func TestAlertsLinkedAfterMergeAndReadAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.observe(t, "opnsense", "a", connectors.Fields{Hostname: "web1", FQDN: "web1.example.test", IPs: []string{"192.0.2.10"}}, e.now)
	e.observe(t, "opnsense", "b", connectors.Fields{Hostname: "db1", IPs: []string{"198.51.100.7"}}, e.now)
	e.observe(t, "opnsense", "c", connectors.Fields{Hostname: "app", FQDN: "app.a.example.test", IPs: []string{"192.0.2.31"}}, e.now)
	e.observe(t, "opnsense", "d", connectors.Fields{Hostname: "app", FQDN: "app.b.example.test", IPs: []string{"192.0.2.32"}}, e.now)

	// Alerts exist before the hosts are merged: they stay unlinked until the merge.
	e.alert(t, "crit", "WEB1.example.test", "", "critical")
	e.alert(t, "warn", "web1", "", "warning")
	e.alert(t, "byip", "unknown-name", "198.51.100.7", "unknown")
	e.alert(t, "ambiguous", "app", "", "info")
	e.alert(t, "orphan", "ghost", "203.0.113.9", "critical")
	for k, v := range e.linked(t) {
		if v != nil {
			t.Fatalf("%s linked before merge", k)
		}
	}
	e.merge(t)
	ids := e.hostIDs(t)
	got := e.linked(t)
	web, db := ids["web1"], ids["db1"]
	if got["crit"] == nil || *got["crit"] != web || got["warn"] == nil || *got["warn"] != web || got["byip"] == nil || *got["byip"] != db ||
		got["ambiguous"] != nil || got["orphan"] != nil {
		t.Fatalf("links: %v (hosts %v)", got, ids)
	}

	// Host views carry the open count and the worst severity.
	hosts, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		switch h.ID {
		case web:
			if h.OpenAlerts != 2 || h.WorstSeverity != "critical" {
				t.Errorf("web1: %d %s", h.OpenAlerts, h.WorstSeverity)
			}
		case db:
			if h.OpenAlerts != 1 || h.WorstSeverity != "unknown" {
				t.Errorf("db1: %d %s", h.OpenAlerts, h.WorstSeverity)
			}
		default:
			if h.OpenAlerts != 0 || h.WorstSeverity != "" {
				t.Errorf("%s: %d %s", h.Name, h.OpenAlerts, h.WorstSeverity)
			}
		}
	}
	if d, err := e.store.Host(ctx, e.ws, web, nil); err != nil || d.OpenAlerts != 2 {
		t.Errorf("detail: %+v %v", d.OpenAlerts, err)
	}
	if only, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Alerts: true}, nil); len(only) != 2 {
		t.Errorf("alerts filter: %d", len(only))
	}

	// Resolved alerts do not count; filters by state, host and connector.
	if _, err := e.q.ResolveAlert(ctx, dbgen.ResolveAlertParams{ConnectorID: e.conns["proxmox"].ID, ExternalKey: "crit", ResolvedAt: pgtype.Timestamptz{Time: e.now, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.store.Host(ctx, e.ws, web, nil); d.OpenAlerts != 1 || d.WorstSeverity != "warning" {
		t.Errorf("after resolve: %d %s", d.OpenAlerts, d.WorstSeverity)
	}
	open, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{State: fleet.AlertOpen}, nil)
	res, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{State: fleet.AlertResolved}, nil)
	byHost, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{HostID: &web}, nil)
	otherConn := e.conns["opnsense"].ID
	byConn, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{ConnectorID: &otherConn}, nil)
	if len(open) != 4 || len(res) != 1 || res[0].ExternalKey != "crit" || len(byHost) != 2 || len(byConn) != 0 {
		t.Errorf("filters: open=%d resolved=%d host=%d conn=%d", len(open), len(res), len(byHost), len(byConn))
	}
	if open[0].Severity != "critical" || open[0].ExternalKey != "orphan" {
		t.Errorf("open alerts must be sorted worst first: %+v", open[0])
	}

	// Scope: only alerts of visible hosts; unlinked ones are hidden.
	inWeb := func(ips []string) bool { return len(ips) > 0 && ips[0] == "192.0.2.10" }
	scoped, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{}, inWeb)
	if len(scoped) != 2 {
		t.Errorf("scoped alerts: %d", len(scoped))
	}
	if _, err := e.store.Host(ctx, e.ws, db, inWeb); err == nil {
		t.Error("host outside scope must be hidden")
	}

	// A host that disappears unlinks its alerts; relinking happens on the next merge.
	e.forget(t, "opnsense", "b")
	e.merge(t)
	if got = e.linked(t); got["byip"] != nil {
		t.Error("alert must be unlinked when the host is gone")
	}
	e.observe(t, "opnsense", "b", connectors.Fields{Hostname: "db1-new", IPs: []string{"198.51.100.7"}}, e.now)
	e.merge(t)
	if got = e.linked(t); got["byip"] == nil {
		t.Error("alert must relink when a host with the address reappears")
	}
}
