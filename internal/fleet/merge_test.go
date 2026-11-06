package fleet

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

var (
	t0      = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cOPN    = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	cPVE    = uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	cNB     = uuid.MustParse("00000000-0000-0000-0000-0000000000a3")
	cMon    = uuid.MustParse("00000000-0000-0000-0000-0000000000a4")
	connTyp = map[uuid.UUID][2]string{cOPN: {"opn", "opnsense"}, cPVE: {"pve", "proxmox"}, cNB: {"nb", "netbox"}, cMon: {"mon", "zabbix"}}
)

func ob(c uuid.UUID, key string, f connectors.Fields) Observation {
	return obAt(c, key, f, t0)
}

func obAt(c uuid.UUID, key string, f connectors.Fields, seen time.Time) Observation {
	return Observation{ConnectorID: c, ConnectorName: connTyp[c][0], ConnectorType: connTyp[c][1], ExternalKey: key, Kind: connectors.KindStatic,
		Fields: f, FirstSeen: seen.Add(-time.Hour), LastSeen: seen}
}

func hostNames(r Result) string {
	var out []string
	for _, h := range r.Hosts {
		out = append(out, h.Name)
	}
	return strings.Join(out, ",")
}

func conflictTypes(r Result) string {
	var out []string
	for _, c := range r.Conflicts {
		out = append(out, c.Fingerprint)
	}
	return strings.Join(out, " ; ")
}

const (
	m1 = "02:00:00:00:00:01"
	m2 = "02:00:00:00:00:02"
	m3 = "02:00:00:00:00:03"
)

func TestMergeMatching(t *testing.T) {
	tests := []struct {
		name      string
		in        []Observation
		wantHosts int
		conflicts string
	}{
		{"same MAC merges despite different names and IPs", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "a", IPs: []string{"192.0.2.10"}}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{m1}, Hostname: "b", IPs: []string{"192.0.2.11"}}),
		}, 1, ""},
		{"MAC is case-insensitive", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{"02:00:00:00:0A:01"}}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{"02:00:00:00:0a:01"}}),
		}, 1, ""},
		{"second MAC links observations", []Observation{
			ob(cPVE, "vm", connectors.Fields{MACs: []string{m1, m2}, Hostname: "vm"}),
			ob(cOPN, "lease", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.20"}}),
		}, 1, ""},
		{"name match without MAC, trailing dot and case", []Observation{
			ob(cOPN, "a", connectors.Fields{Hostname: "Web1.Example.Test."}),
			ob(cPVE, "b", connectors.Fields{FQDN: "web1.example.test"}),
		}, 1, ""},
		{"MAC-less joins the only MAC-bearing host by name", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "web1"}),
			ob(cNB, "b", connectors.Fields{Hostname: "web1"}),
		}, 1, ""},
		{"same name different MACs stays separate and conflicts", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "web1"}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{m2}, Hostname: "web1"}),
		}, 2, "duplicate_hostname|mac:" + m1 + "|mac:" + m2 + "|name:web1"},
		{"MAC-less with two MAC candidates by name stays separate", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "web1"}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{m2}, Hostname: "web1"}),
			ob(cNB, "c", connectors.Fields{Hostname: "web1"}),
		}, 3, "duplicate_hostname|mac:" + m1 + "|mac:" + m2 + "|name:web1"},
		{"short name matches unique FQDN", []Observation{
			ob(cOPN, "a", connectors.Fields{Hostname: "web1", IPs: []string{"192.0.2.1"}}),
			ob(cNB, "b", connectors.Fields{FQDN: "web1.example.test"}),
		}, 1, ""},
		{"short name with ambiguous FQDNs does not match", []Observation{
			ob(cOPN, "a", connectors.Fields{Hostname: "web1"}),
			ob(cNB, "b", connectors.Fields{FQDN: "web1.a.example.test"}),
			ob(cMon, "c", connectors.Fields{FQDN: "web1.b.example.test"}),
		}, 3, ""},
		{"own short name does not bridge FQDNs of different domains", []Observation{
			ob(cNB, "b", connectors.Fields{Hostname: "web1", FQDN: "web1.a.example.test"}),
			ob(cMon, "c", connectors.Fields{Hostname: "web1", FQDN: "web1.b.example.test"}),
		}, 2, ""},
		{"IP match without MAC and name", []Observation{
			ob(cPVE, "vm", connectors.Fields{Hostname: "vm", IPs: []string{"192.0.2.5"}}),
			ob(cOPN, "arp", connectors.Fields{IPs: []string{"192.0.2.5"}}),
		}, 1, ""},
		{"IPv6 notation is normalized", []Observation{
			ob(cPVE, "vm", connectors.Fields{IPs: []string{"2001:DB8::1"}}),
			ob(cOPN, "arp", connectors.Fields{IPs: []string{"2001:db8:0:0:0:0:0:1"}}),
		}, 1, ""},
		{"shared IP with different MACs conflicts instead of merging", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.5"}}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}}),
		}, 2, "ip_conflict|ip:192.0.2.5|mac:" + m1 + "|mac:" + m2},
		{"MAC-less IP match with two MAC candidates stays separate", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, IPs: []string{"192.0.2.5"}}),
			ob(cPVE, "b", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.5"}}),
			ob(cNB, "c", connectors.Fields{IPs: []string{"192.0.2.5"}}),
		}, 3, "ip_conflict|ip:192.0.2.5|mac:" + m1 + "|mac:" + m2},
		{"loopback and link-local never match", []Observation{
			ob(cPVE, "a", connectors.Fields{Hostname: "a", IPs: []string{"127.0.0.1", "fe80::1"}}),
			ob(cOPN, "b", connectors.Fields{Hostname: "b", IPs: []string{"127.0.0.1", "fe80::1"}}),
		}, 2, ""},
		{"documentation ranges 198.51.100.0/24 merge by IP", []Observation{
			ob(cPVE, "a", connectors.Fields{IPs: []string{"198.51.100.7"}}),
			ob(cOPN, "b", connectors.Fields{IPs: []string{"198.51.100.7"}, Hostname: "x"}),
		}, 1, ""},
		{"name match then differing MAC via IP is a conflict", []Observation{
			ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "web1"}),
			ob(cNB, "b", connectors.Fields{Hostname: "web1", IPs: []string{"192.0.2.9"}}),
			ob(cPVE, "c", connectors.Fields{MACs: []string{m2}, IPs: []string{"192.0.2.9"}}),
		}, 2, "ip_conflict|ip:192.0.2.9|mac:" + m1 + "|mac:" + m2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Merge(tc.in, DefaultPriority(), t0)
			if len(r.Hosts) != tc.wantHosts {
				t.Fatalf("hosts = %d (%s), want %d", len(r.Hosts), hostNames(r), tc.wantHosts)
			}
			if got := conflictTypes(r); got != tc.conflicts {
				t.Errorf("conflicts = %q, want %q", got, tc.conflicts)
			}
		})
	}
}

func TestMergePriorityAndProvenance(t *testing.T) {
	in := []Observation{
		ob(cOPN, "a", connectors.Fields{MACs: []string{m1}, Hostname: "opn-name", IPs: []string{"192.0.2.10"}, Interface: "lan", Status: "bound", Tags: []string{"t1"}}),
		ob(cPVE, "b", connectors.Fields{MACs: []string{m1, m2}, Hostname: "pve-name", Notes: "vm", IPs: []string{"192.0.2.11"}, Tags: []string{"t2"}}),
		ob(cMon, "c", connectors.Fields{MACs: []string{m1}, Hostname: "mon-name", FQDN: "mon.example.test", Status: "ok"}),
	}
	r := Merge(in, DefaultPriority(), t0)
	if len(r.Hosts) != 1 {
		t.Fatalf("hosts = %d", len(r.Hosts))
	}
	h := r.Hosts[0]
	f := h.Fields
	if f.Hostname != "pve-name" || f.Sources["hostname"][0].ConnectorType != "proxmox" {
		t.Errorf("hostname = %q %v", f.Hostname, f.Sources["hostname"])
	}
	if f.FQDN != "mon.example.test" || f.Sources["fqdn"][0].ConnectorType != "zabbix" {
		t.Errorf("fqdn = %q %v", f.FQDN, f.Sources["fqdn"])
	}
	if f.Status != "bound" || f.Sources["status"][0].ConnectorType != "opnsense" {
		t.Errorf("status = %q (opnsense outranks monitoring)", f.Status)
	}
	if f.Interface != "lan" || f.Notes != "vm" {
		t.Errorf("interface/notes = %q/%q", f.Interface, f.Notes)
	}
	if !reflect.DeepEqual(f.MACs, []string{m1, m2}) || !reflect.DeepEqual(f.IPs, []string{"192.0.2.11", "192.0.2.10"}) || !reflect.DeepEqual(f.Tags, []string{"t2", "t1"}) {
		t.Errorf("lists = %v %v %v", f.MACs, f.IPs, f.Tags)
	}
	if len(f.Sources["ips"]) != 2 || f.Sources["ips"][0].ConnectorType != "proxmox" {
		t.Errorf("ip sources = %v", f.Sources["ips"])
	}
	if h.Name != "pve-name" || h.Observations != 3 {
		t.Errorf("name/obs = %q/%d", h.Name, h.Observations)
	}
	if k, _ := h.Strongest(); k.String() != "mac:"+m1 {
		t.Errorf("strongest = %v", k)
	}

	// A NetBox observation outranks Proxmox by default.
	nb := append(in, ob(cNB, "n", connectors.Fields{MACs: []string{m1}, Hostname: "nb-name"}))
	if got := Merge(nb, DefaultPriority(), t0).Hosts[0].Fields.Hostname; got != "nb-name" {
		t.Errorf("netbox hostname = %q", got)
	}
	// A per-connector override flips the order.
	p := DefaultPriority()
	p.ByConnector = map[uuid.UUID]int{cOPN: 1000}
	r = Merge(in, p, t0)
	if r.Hosts[0].Fields.Hostname != "opn-name" || r.Hosts[0].Fields.Sources["hostname"][0].ConnectorName != "opn" {
		t.Errorf("override hostname = %q", r.Hosts[0].Fields.Hostname)
	}
}

func TestMergeDisappeared(t *testing.T) {
	old := t0.Add(-25 * time.Hour)
	fresh := t0.Add(-23 * time.Hour)
	in := []Observation{
		obAt(cOPN, "gone", connectors.Fields{MACs: []string{m1}, Hostname: "gone"}, old),
		obAt(cOPN, "ok", connectors.Fields{MACs: []string{m2}, Hostname: "ok"}, fresh),
		// Old observation, but another connector saw the host recently.
		obAt(cOPN, "x1", connectors.Fields{MACs: []string{m3}, Hostname: "mixed"}, old),
		obAt(cPVE, "x2", connectors.Fields{MACs: []string{m3}}, fresh),
	}
	r := Merge(in, DefaultPriority(), t0)
	if len(r.Conflicts) != 1 || r.Conflicts[0].Type != ConflictDisappeared || r.Conflicts[0].Fingerprint != "host_disappeared|mac:"+m1 {
		t.Fatalf("conflicts = %s", conflictTypes(r))
	}
	if h := r.Hosts[r.Conflicts[0].Hosts[0]]; h.Name != "gone" {
		t.Errorf("conflict host = %q", h.Name)
	}
}

func TestMergeStableOrderAndInputIndependence(t *testing.T) {
	in := []Observation{
		ob(cOPN, "1", connectors.Fields{MACs: []string{m2}, Hostname: "b", IPs: []string{"192.0.2.2"}}),
		ob(cPVE, "2", connectors.Fields{MACs: []string{m1}, Hostname: "a", IPs: []string{"192.0.2.1"}}),
		ob(cNB, "3", connectors.Fields{Hostname: "named-only"}),
		ob(cMon, "4", connectors.Fields{IPs: []string{"198.51.100.1"}}),
		ob(cOPN, "5", connectors.Fields{Hostname: "named-only", IPs: []string{"192.0.2.3"}}),
		ob(cPVE, "6", connectors.Fields{MACs: []string{m3}, Hostname: "a", IPs: []string{"192.0.2.1"}}),
	}
	want := Merge(in, DefaultPriority(), t0)
	var order []string
	for _, h := range want.Hosts {
		k, _ := h.Strongest()
		order = append(order, k.String())
	}
	// MAC hosts first (sorted), then name, then IP.
	exp := []string{"mac:" + m1, "mac:" + m2, "mac:" + m3, "name:named-only", "ip:198.51.100.1"}
	if !reflect.DeepEqual(order, exp) {
		t.Fatalf("order = %v, want %v", order, exp)
	}
	rng := rand.New(rand.NewSource(1))
	for i := range 20 {
		shuffled := append([]Observation(nil), in...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := Merge(shuffled, DefaultPriority(), t0); !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d changed the result:\n%v\n%v", i, fmt.Sprint(got.Hosts), fmt.Sprint(want.Hosts))
		}
	}
}

func TestMergeEmpty(t *testing.T) {
	r := Merge(nil, DefaultPriority(), t0)
	if len(r.Hosts) != 0 || len(r.Conflicts) != 0 {
		t.Errorf("empty = %+v", r)
	}
}
