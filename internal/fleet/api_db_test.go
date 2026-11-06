package fleet_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func TestReadAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.q.UpsertNetwork(ctx, dbgen.UpsertNetworkParams{WorkspaceID: e.ws, ConnectorID: e.conns["opnsense"].ID, ExternalKey: "lan",
		Cidr: netip.MustParsePrefix("192.0.2.0/24"), Interface: "lan0", FirstSeen: pgtype.Timestamptz{Time: e.now, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	e.observe(t, "opnsense", "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.5"}}, e.now)
	e.observe(t, "proxmox", "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}, Hostname: "web1"}, e.now)
	e.observe(t, "opnsense", "c", connectors.Fields{Hostname: "far1", IPs: []string{"198.51.100.7"}}, e.now)
	e.merge(t)

	nets, err := e.store.Networks(ctx, e.ws, nil, nil)
	if err != nil || len(nets) != 1 || nets[0].CIDR != "192.0.2.0/24" || nets[0].ConnectorName != "opnsense-1" {
		t.Fatalf("networks %+v %v", nets, err)
	}
	if nets[0].HostCount != 2 {
		t.Errorf("host count %d, want 2", nets[0].HostCount)
	}
	if hid, _ := e.store.Networks(ctx, e.ws, nil, func([]string) bool { return false }); len(hid) != 1 || hid[0].HostCount != 0 {
		t.Errorf("host count must respect visibility: %+v", hid)
	}
	if none, _ := e.store.Networks(ctx, e.ws, func(uuid.UUID, netip.Prefix) bool { return false }, nil); len(none) != 0 {
		t.Error("allow filter ignored")
	}

	all, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{}, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("hosts %d %v", len(all), err)
	}
	inNet, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{NetworkID: &nets[0].ID}, nil)
	if err != nil || len(inNet) != 2 {
		t.Fatalf("network filter: %d %v", len(inNet), err)
	}
	if _, err := e.store.Hosts(ctx, e.ws, fleet.HostFilter{NetworkID: new(uuid.UUID)}, nil); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown network: %v", err)
	}
	if got, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Q: "WEB"}, nil); len(got) != 1 || got[0].Name != "web1" {
		t.Errorf("q filter: %+v", got)
	}
	if got, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{Conflicts: true}, nil); len(got) != 2 {
		t.Errorf("conflict filter: %d", len(got))
	}
	scoped := func(ips []string) bool { return slices.Contains(ips, "198.51.100.7") }
	if got, _ := e.store.Hosts(ctx, e.ws, fleet.HostFilter{}, scoped); len(got) != 1 || got[0].Name != "far1" {
		t.Errorf("visibility filter: %+v", got)
	}

	var web fleet.HostView
	for _, h := range all {
		if h.Name == "web1" {
			web = h
		}
	}
	d, err := e.store.Host(ctx, e.ws, web.ID, nil)
	if err != nil || len(d.Observations) != 1 || len(d.Keys) == 0 || len(d.Conflicts) != 1 || len(d.Fields.Sources["hostname"]) != 1 {
		t.Fatalf("detail %+v %v", d, err)
	}
	if _, err := e.store.Host(ctx, e.ws, web.ID, scoped); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("invisible host: %v", err)
	}
	if _, err := e.store.Host(ctx, e.ws, uuid.New(), nil); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown host: %v", err)
	}

	open, err := e.store.Conflicts(ctx, e.ws, fleet.StateOpen, nil)
	if err != nil || len(open) != 1 {
		t.Fatalf("conflicts %+v %v", open, err)
	}
	if len(open[0].Hosts) != len(open[0].HostIDs) || (open[0].Hosts[0].Name != "web1" && open[0].Hosts[1].Name != "web1") {
		t.Errorf("conflict host names: %+v", open[0].Hosts)
	}
	if hidden, _ := e.store.Conflicts(ctx, e.ws, "", scoped); len(hidden) != 0 {
		t.Error("conflict with invisible hosts must be hidden")
	}
	user, err := e.q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{OidcIssuer: pgtype.Text{String: "https://idp.example.test", Valid: true},
		OidcSubject: pgtype.Text{String: uuid.NewString(), Valid: true}, Email: "ack@example.test", DisplayName: "Ack"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.Acknowledge(ctx, e.ws, open[0].ID, user.ID, scoped); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("ack of invisible conflict: %v", err)
	}
	c, err := e.store.Acknowledge(ctx, e.ws, open[0].ID, user.ID, nil)
	if err != nil || c.State != fleet.StateAcknowledged || c.AcknowledgedBy == nil || *c.AcknowledgedBy != user.ID {
		t.Fatalf("ack %+v %v", c, err)
	}
	if got, _ := e.store.Conflicts(ctx, e.ws, fleet.StateAcknowledged, nil); len(got) != 1 {
		t.Errorf("acknowledged filter: %d", len(got))
	}
	if got, _ := e.store.Conflicts(ctx, e.ws, fleet.StateResolved, nil); len(got) != 0 {
		t.Errorf("resolved filter: %d", len(got))
	}
	if _, err := e.store.Acknowledge(ctx, e.ws, uuid.New(), user.ID, nil); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("unknown conflict: %v", err)
	}
}
