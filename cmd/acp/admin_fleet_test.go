package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func TestParseFleetArgs(t *testing.T) {
	a, err := parseFleetArgs([]string{"conflicts", "--workspace", "w", "--state", "all"}, io.Discard)
	if err != nil || a.Sub != "conflicts" || a.State != "all" || a.Workspace != "w" {
		t.Errorf("parsed %+v %v", a, err)
	}
	if a, err := parseFleetArgs([]string{"hosts", "--workspace", "w"}, io.Discard); err != nil || a.Sub != "hosts" {
		t.Errorf("hosts %+v %v", a, err)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"merge"}, {"conflicts", "--workspace", "w", "--state", "x"}, {"hosts", "--workspace", "w", "extra"}} {
		if _, err := parseFleetArgs(bad, io.Discard); err == nil {
			t.Errorf("args %v accepted", bad)
		}
	}
}

func TestFormatFleet(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	fb, _ := json.Marshal(fleet.Fields{IPs: []string{"192.0.2.1"}, Kind: "vm", Sources: map[string][]fleet.Source{"ips": {{ConnectorName: "pve"}, {ConnectorName: "opn"}}}})
	seen := pgtype.Timestamptz{Time: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), Valid: true}
	got := formatHost(dbgen.Host{ID: id, Name: "web1", Fields: fb, LastSeen: seen}, []dbgen.HostKey{{Kind: "mac", Value: "02:00:00:00:00:01"}})
	want := id.String() + "\tweb1\t02:00:00:00:00:01\t192.0.2.1\tvm\tlast=2026-11-06T12:00:00Z\tsources=opn,pve"
	if got != want {
		t.Errorf("host = %q", got)
	}
	c := formatConflict(dbgen.HostConflict{ID: id, Type: "ip_conflict", State: "open", Fingerprint: "ip_conflict|ip:192.0.2.1", HostIds: []uuid.UUID{id}})
	if !strings.Contains(c, "\tip_conflict\topen\tack=-\tip_conflict|ip:192.0.2.1\thosts="+id.String()) {
		t.Errorf("conflict = %q", c)
	}
}
