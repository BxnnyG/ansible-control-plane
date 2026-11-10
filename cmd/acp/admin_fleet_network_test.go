package main

import (
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func TestParseFleetNetworkArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		action  string
		network string
	}{
		{"list", []string{"network", "list", "--workspace", "w"}, false, "list", ""},
		{"track", []string{"network", "track", "--workspace", "w", "--network", "lan"}, false, "track", "lan"},
		{"untrack", []string{"network", "untrack", "--workspace", "w", "--network", "192.0.2.0/24"}, false, "untrack", "192.0.2.0/24"},
		{"missing action", []string{"network", "--workspace", "w"}, true, "", ""},
		{"unknown action", []string{"network", "drop", "--workspace", "w"}, true, "", ""},
		{"track without network", []string{"network", "track", "--workspace", "w"}, true, "", ""},
		{"list with network", []string{"network", "list", "--workspace", "w", "--network", "x"}, true, "", ""},
		{"no workspace", []string{"network", "list"}, true, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := parseFleetArgs(tc.args, io.Discard)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil && (a.Sub != "network" || a.Action != tc.action || a.Network != tc.network) {
				t.Errorf("parsed %+v", a)
			}
		})
	}
	if a, err := parseFleetArgs([]string{"unmanaged", "--workspace", "w", "--include-untracked"}, io.Discard); err != nil || !a.IncludeUntracked {
		t.Errorf("include-untracked: %+v %v", a, err)
	}
}

func TestMatchNetwork(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nets := []fleet.NetworkView{
		{ID: a, ExternalKey: "lan", CIDR: "192.0.2.0/24", Description: "Servers"},
		{ID: b, ExternalKey: "wifi", CIDR: "198.51.100.0/24", Description: "Guests"},
		{ID: c, ExternalKey: "lan", CIDR: "203.0.113.0/24"},
	}
	tests := []struct {
		ref     string
		want    uuid.UUID
		wantErr string
	}{
		{b.String(), b, ""},
		{uuid.NewString(), uuid.Nil, "not found"},
		{"WIFI", b, ""},
		{"servers", a, ""},
		{"203.0.113.0/24", c, ""},
		{"lan", uuid.Nil, "ambiguous"},
		{"nope", uuid.Nil, "not found"},
	}
	for _, tc := range tests {
		got, err := matchNetwork(nets, tc.ref)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", tc.ref, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.ID != tc.want {
			t.Errorf("%s: %v %v", tc.ref, got.ID, err)
		}
	}
}

func TestFormatNetwork(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	got := formatNetwork(fleet.NetworkView{ID: id, CIDR: "192.0.2.0/24", ExternalKey: "lan", ConnectorName: "fw", HostCount: 4})
	want := id.String() + "\tuntracked\t192.0.2.0/24\tlan\t-\tfw\thosts=4"
	if got != want {
		t.Errorf("format = %q", got)
	}
}
