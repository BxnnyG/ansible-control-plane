package fleet

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

func TestOnlineOf(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	tests := []struct {
		name string
		f    Fields
		want string
	}{
		{"no live source", Fields{}, StatusUnknown},
		{"fresh lease", Fields{NetSeen: ago(5 * time.Minute)}, StatusOnline},
		{"lease at the edge", Fields{NetSeen: ago(OnlineWindow)}, StatusOnline},
		{"stale lease", Fields{NetSeen: ago(OnlineWindow + time.Second)}, StatusOffline},
		{"running vm", Fields{Power: "running"}, StatusOnline},
		{"active device", Fields{Power: "active"}, StatusOnline},
		{"stopped vm", Fields{Power: "stopped"}, StatusOffline},
		{"stale lease but running vm", Fields{NetSeen: ago(time.Hour), Power: "running"}, StatusOnline},
		{"fresh lease but stopped vm", Fields{NetSeen: ago(time.Minute), Power: "stopped"}, StatusOnline},
		{"stale lease and stopped vm", Fields{NetSeen: ago(time.Hour), Power: "stopped"}, StatusOffline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnlineOf(tt.f, now); got != tt.want {
				t.Fatalf("OnlineOf = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestResultOf(t *testing.T) {
	tests := []struct {
		changed, failed, unreachable int
		want                         string
	}{
		{0, 0, 0, ResultOK}, {3, 0, 0, ResultChanged}, {3, 1, 0, ResultFailed}, {0, 1, 1, ResultUnreachable}, {0, 0, 1, ResultUnreachable},
	}
	for _, tt := range tests {
		if got := ResultOf(tt.changed, tt.failed, tt.unreachable); got != tt.want {
			t.Errorf("ResultOf(%d,%d,%d) = %s, want %s", tt.changed, tt.failed, tt.unreachable, got, tt.want)
		}
	}
}

func TestNetworkCountsCount(t *testing.T) {
	run := func(res string) *RunRef { return &RunRef{Result: res} }
	hosts := []HostView{
		{Online: StatusOnline, LastRun: run(ResultOK)},
		{Online: StatusOnline, LastRun: run(ResultChanged), OpenAlerts: 2},
		{Online: StatusOffline, LastRun: run(ResultFailed)},
		{Online: StatusUnknown, LastRun: run(ResultUnreachable)},
		{Online: StatusOnline},
		{Online: StatusOffline, Managed: true},
	}
	var c NetworkCounts
	for _, h := range hosts {
		c.count(h)
	}
	want := NetworkCounts{Online: 3, AnsibleOK: 2, Failed: 2, NeverManaged: 2, OpenAlerts: 2}
	if c != want {
		t.Fatalf("counts = %+v, want %+v", c, want)
	}
}

func TestMergeLiveState(t *testing.T) {
	older, newer := t0.Add(-time.Hour), t0.Add(-time.Minute)
	mk := func(c uuid.UUID, key, kind, status string, seen time.Time) Observation {
		o := obAt(c, key, connectors.Fields{Hostname: "web1", Status: status}, seen)
		o.Kind = kind
		return o
	}
	res := Merge([]Observation{
		mk(cOPN, "l1", connectors.KindLease, "", older),
		mk(cOPN, "a1", connectors.KindARP, "", newer),
		mk(cPVE, "v1", connectors.KindVM, "Running", older),
		mk(cMon, "d1", connectors.KindDevice, "stopped", older),
	}, DefaultPriority(), t0)
	if len(res.Hosts) != 1 {
		t.Fatalf("hosts = %d, want 1", len(res.Hosts))
	}
	f := res.Hosts[0].Fields
	if f.NetSeen == nil || !f.NetSeen.Equal(newer) {
		t.Errorf("NetSeen = %v, want %v", f.NetSeen, newer)
	}
	if f.Power != "running" { // proxmox outranks the default-priority device
		t.Errorf("Power = %q, want running", f.Power)
	}
}
