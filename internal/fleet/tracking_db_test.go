package fleet_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func (e *env) network(t *testing.T, key, cidr string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	err := e.q.UpsertNetwork(ctx, dbgen.UpsertNetworkParams{WorkspaceID: e.ws, ConnectorID: e.conns["opnsense"].ID, ExternalKey: key,
		Cidr: netip.MustParsePrefix(cidr), FirstSeen: pgtype.Timestamptz{Time: e.now, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	nets, err := e.store.Networks(ctx, e.ws, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nets {
		if n.ExternalKey == key {
			return n.ID
		}
	}
	t.Fatalf("network %s not found", key)
	return uuid.Nil
}

func names(hs []fleet.HostView) map[string]bool {
	out := map[string]bool{}
	for _, h := range hs {
		out[h.Name] = true
	}
	return out
}

func TestNetworkTrackingFilters(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	srv := e.network(t, "srv", "192.0.2.0/24")
	wifi := e.network(t, "wifi", "198.51.100.0/24")
	overlap := e.network(t, "overlap", "192.0.2.128/25") // inside srv
	e.observe(t, "opnsense", "a", connectors.Fields{Hostname: "server1", IPs: []string{"192.0.2.5"}}, e.now)
	e.observe(t, "opnsense", "b", connectors.Fields{Hostname: "phone1", IPs: []string{"198.51.100.7"}}, e.now)
	e.observe(t, "opnsense", "c", connectors.Fields{Hostname: "nonet1", IPs: []string{"203.0.113.9"}}, e.now)
	e.observe(t, "opnsense", "d", connectors.Fields{Hostname: "multi1", IPs: []string{"192.0.2.6", "198.51.100.8"}}, e.now)
	e.merge(t)
	e.alert(t, "phone-alert", "phone1", "", "critical")
	e.alert(t, "server-alert", "server1", "", "warning")
	e.alert(t, "orphan-alert", "ghost", "203.0.113.50", "info") // unlinked alerts stay visible
	e.merge(t)

	// New networks are tracked by default.
	nets, _ := e.store.Networks(ctx, e.ws, nil, nil)
	for _, n := range nets {
		if !n.Tracked {
			t.Fatalf("network %s must start tracked", n.ExternalKey)
		}
	}
	if def, err := e.store.NewNetworksTracked(ctx, e.ws); err != nil || !def {
		t.Fatalf("default %v %v", def, err)
	}

	tests := []struct {
		name      string
		untrack   []uuid.UUID
		wantHosts []string
		wantAlert []string
	}{
		{"nothing untracked", nil, []string{"server1", "phone1", "nonet1", "multi1"}, []string{"phone-alert", "server-alert", "orphan-alert"}},
		{"wifi untracked", []uuid.UUID{wifi}, []string{"server1", "nonet1", "multi1"}, []string{"server-alert", "orphan-alert"}},
		{"servers untracked", []uuid.UUID{srv, overlap}, []string{"phone1", "nonet1", "multi1"}, []string{"phone-alert", "orphan-alert"}},
		{"only the overlap untracked", []uuid.UUID{overlap}, []string{"server1", "phone1", "nonet1", "multi1"}, []string{"phone-alert", "server-alert", "orphan-alert"}},
		{"all untracked", []uuid.UUID{srv, wifi, overlap}, []string{"nonet1"}, []string{"orphan-alert"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.store.TrackOnly(ctx, e.ws, nil); err != nil { // untrack all, then re-track below
				t.Fatal(err)
			}
			if _, err := e.store.SetTracked(ctx, e.ws, []uuid.UUID{srv, wifi, overlap}, true); err != nil {
				t.Fatal(err)
			}
			if len(tc.untrack) > 0 {
				if n, err := e.store.SetTracked(ctx, e.ws, tc.untrack, false); err != nil || n != len(tc.untrack) {
					t.Fatalf("untrack: %d %v", n, err)
				}
			}
			hosts, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{ExcludeUntracked: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := names(hosts)
			if len(got) != len(tc.wantHosts) {
				t.Errorf("hosts %v, want %v", got, tc.wantHosts)
			}
			for _, h := range tc.wantHosts {
				if !got[h] {
					t.Errorf("host %s missing: %v", h, got)
				}
			}
			unmanaged := false
			un, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{ExcludeUntracked: true, Managed: &unmanaged}, nil)
			if len(un) != len(tc.wantHosts) {
				t.Errorf("unmanaged %d, want %d", len(un), len(tc.wantHosts))
			}
			// Without the opt-in every host stays (group rules, remediation).
			if all, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{}, nil); len(all) != 4 {
				t.Errorf("unfiltered hosts: %d", len(all))
			}
			alerts, err := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{ExcludeUntracked: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(alerts) != len(tc.wantAlert) {
				t.Errorf("alerts %d, want %v", len(alerts), tc.wantAlert)
			}
			for _, w := range tc.wantAlert {
				found := false
				for _, a := range alerts {
					found = found || a.ExternalKey == w
				}
				if !found {
					t.Errorf("alert %s missing", w)
				}
			}
			if all, _ := e.store.Alerts(ctx, e.ws, fleet.AlertFilter{}, nil); len(all) != 3 {
				t.Errorf("unfiltered alerts: %d", len(all))
			}
		})
	}
}

func TestNetworkTrackingBulkDefaultAndSyncStability(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.network(t, "a", "192.0.2.0/24")
	b := e.network(t, "b", "198.51.100.0/24")

	if n, err := e.store.TrackOnly(ctx, e.ws, []uuid.UUID{a}); err != nil || n != 2 {
		t.Fatalf("track only: %d %v", n, err)
	}
	flags := func() map[uuid.UUID]bool {
		nets, _ := e.store.Networks(ctx, e.ws, nil, nil)
		out := map[uuid.UUID]bool{}
		for _, n := range nets {
			out[n.ID] = n.Tracked
		}
		return out
	}
	if f := flags(); !f[a] || f[b] {
		t.Fatalf("after track-only: %v", f)
	}
	// A re-sync of the same network keeps the user's choice and the id.
	if again := e.network(t, "b", "198.51.100.0/24"); again != b || flags()[b] {
		t.Errorf("sync reset the flag or changed the id")
	}
	// Networks of other workspaces and unknown ids are not touched.
	if n, err := e.store.SetTracked(ctx, e.ws, []uuid.UUID{uuid.New()}, true); err != nil || n != 0 {
		t.Errorf("unknown id: %d %v", n, err)
	}

	// New networks follow the workspace default.
	if err := e.store.SetNewNetworksTracked(ctx, e.ws, false); err != nil {
		t.Fatal(err)
	}
	c := e.network(t, "c", "203.0.113.0/24")
	if flags()[c] {
		t.Error("new network must start untracked with default=false")
	}
	if def, _ := e.store.NewNetworksTracked(ctx, e.ws); def {
		t.Error("default not persisted")
	}
	// An empty selection untracks everything.
	if _, err := e.store.TrackOnly(ctx, e.ws, nil); err != nil {
		t.Fatal(err)
	}
	for id, v := range flags() {
		if v {
			t.Errorf("%s still tracked", id)
		}
	}
}

func TestConflictsRespectTracking(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	lan := e.network(t, "lan", "192.0.2.0/24")
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.5"}}, e.now)
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}, Hostname: "web1"}, e.now)
	e.merge(t)

	if got, err := e.store.Conflicts(ctx, e.ws, fleet.StateOpen, true, nil); err != nil || len(got) != 1 {
		t.Fatalf("tracked network: %d conflicts, %v", len(got), err)
	}
	if _, err := e.store.SetTracked(ctx, e.ws, []uuid.UUID{lan}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.store.Conflicts(ctx, e.ws, fleet.StateOpen, true, nil); len(got) != 0 {
		t.Errorf("conflict of untracked hosts must be hidden, got %d", len(got))
	}
	if got, _ := e.store.Conflicts(ctx, e.ws, fleet.StateOpen, false, nil); len(got) != 1 {
		t.Errorf("include untracked must list the conflict, got %d", len(got))
	}
}
