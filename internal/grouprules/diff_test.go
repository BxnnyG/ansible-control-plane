package grouprules

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
)

func TestParseMatcherValidation(t *testing.T) {
	tests := []struct {
		name, kind, params string
		wantErr            bool
		wantNorm           string
	}{
		{"subnet v4", KindSubnet, `{"cidr":"192.0.2.77/24"}`, false, `{"cidr":"192.0.2.0/24"}`},
		{"subnet v6", KindSubnet, `{"cidr":"2001:db8::1/64"}`, false, `{"cidr":"2001:db8::/64"}`},
		{"subnet bad", KindSubnet, `{"cidr":"nonsense"}`, true, ""},
		{"subnet unknown field", KindSubnet, `{"cidr":"192.0.2.0/24","x":1}`, true, ""},
		{"subnet missing", KindSubnet, `{}`, true, ""},
		{"tag", KindProxmoxTag, `{"tag":" Web "}`, false, `{"tag":"web"}`},
		{"tag with prefix", KindProxmoxTag, `{"tag":"proxmox:db"}`, false, `{"tag":"db"}`},
		{"tag empty", KindProxmoxTag, `{"tag":""}`, true, ""},
		{"tag whitespace", KindProxmoxTag, `{"tag":"a b"}`, true, ""},
		{"unknown kind", "netbox_role", `{}`, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, norm, err := ParseMatcher(tt.kind, json.RawMessage(tt.params))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}
			if err == nil && string(norm) != tt.wantNorm {
				t.Errorf("norm = %s, want %s", norm, tt.wantNorm)
			}
		})
	}
}

func TestMatchers(t *testing.T) {
	h := func(ips []string, tags ...string) Host { return Host{IPs: ips, Tags: tags} }
	tests := []struct {
		name, kind, params string
		host               Host
		want               bool
	}{
		{"in subnet", KindSubnet, `{"cidr":"192.0.2.0/24"}`, h([]string{"198.51.100.1", "192.0.2.9"}), true},
		{"outside subnet", KindSubnet, `{"cidr":"192.0.2.0/24"}`, h([]string{"198.51.100.1"}), false},
		{"v6 in subnet", KindSubnet, `{"cidr":"2001:db8::/32"}`, h([]string{"2001:db8:1::5"}), true},
		{"v4-mapped in v4 subnet", KindSubnet, `{"cidr":"192.0.2.0/24"}`, h([]string{"::ffff:192.0.2.9"}), true},
		{"v4 not in v6 subnet", KindSubnet, `{"cidr":"2001:db8::/32"}`, h([]string{"192.0.2.9"}), false},
		{"garbage ip ignored", KindSubnet, `{"cidr":"192.0.2.0/24"}`, h([]string{"x"}), false},
		{"no ips", KindSubnet, `{"cidr":"192.0.2.0/24"}`, h(nil), false},
		{"tag match", KindProxmoxTag, `{"tag":"web"}`, h(nil, "node:pve1", "proxmox:web"), true},
		{"tag case-insensitive", KindProxmoxTag, `{"tag":"web"}`, h(nil, "Proxmox:WEB"), true},
		{"tag other namespace", KindProxmoxTag, `{"tag":"web"}`, h(nil, "node:web"), false},
		{"tag absent", KindProxmoxTag, `{"tag":"web"}`, h(nil), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, err := ParseMatcher(tt.kind, json.RawMessage(tt.params))
			if err != nil {
				t.Fatal(err)
			}
			if got := m.Match(tt.host); got != tt.want {
				t.Errorf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInventoryName(t *testing.T) {
	tests := []struct {
		name     string
		host     Host
		wantName string
		wantAddr string
		wantOK   bool
	}{
		{"fqdn wins, no ansible_host", Host{FQDN: "Web1.Lan.Example", Hostname: "web1", IPs: []string{"192.0.2.5"}}, "web1.lan.example", "", true},
		{"hostname gets primary ip", Host{Hostname: "web1", IPs: []string{"2001:db8::5", "192.0.2.5"}}, "web1", "192.0.2.5", true},
		{"dotless fqdn is a hostname", Host{FQDN: "web1", IPs: []string{"192.0.2.5"}}, "web1", "192.0.2.5", true},
		{"hostname without ip", Host{Hostname: "web1"}, "web1", "", true},
		{"ip only", Host{IPs: []string{"192.0.2.5"}}, "192.0.2.5", "", true},
		{"ipv6 only", Host{IPs: []string{"2001:db8::5"}}, "2001:db8::5", "", true},
		{"invalid name falls back to ip", Host{Hostname: "bad name", IPs: []string{"192.0.2.5"}}, "192.0.2.5", "", true},
		{"nothing", Host{Name: "aa:bb"}, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, a, ok := InventoryName(tt.host)
			if n != tt.wantName || a != tt.wantAddr || ok != tt.wantOK {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", n, a, ok, tt.wantName, tt.wantAddr, tt.wantOK)
			}
		})
	}
}

func fh(fqdn, hostname string, ips ...string) Host {
	return Host{ID: uuid.New(), Name: hostname, Hostname: hostname, FQDN: fqdn, IPs: ips}
}

func addNames(p Plan) []string {
	out := []string{}
	for _, a := range p.Add {
		out = append(out, a.Name)
	}
	return out
}

func TestDiff(t *testing.T) {
	all := Matcher(matchAll{})
	tests := []struct {
		name       string
		mode       string
		hosts      []Host
		state      inventory.GroupState
		tracked    []string
		wantAdd    []string
		wantRemove []string
		wantUntrk  []string
		wantKeep   int
		wantCreate bool
		wantSkip   int
	}{
		{
			name: "empty inventory creates group and hosts", mode: ModeAddOnly,
			hosts:   []Host{fh("b.lan", "b", "192.0.2.2"), fh("a.lan", "a", "192.0.2.1")},
			wantAdd: []string{"a.lan", "b.lan"}, wantCreate: true,
		},
		{
			name: "existing member is kept", mode: ModeAddOnly,
			hosts:    []Host{fh("a.lan", "a", "192.0.2.1")},
			state:    inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "a.lan"}}, Members: []string{"a.lan"}},
			wantKeep: 1, wantAdd: []string{},
		},
		{
			name: "existing host outside group joins without creation", mode: ModeAddOnly,
			hosts:   []Host{fh("a.lan", "a", "192.0.2.1")},
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "a.lan"}}},
			wantAdd: []string{"a.lan"},
		},
		{
			name: "links inventory host by short name case-insensitively", mode: ModeAddOnly,
			hosts:   []Host{fh("a.lan", "A", "192.0.2.1")},
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "a"}}},
			wantAdd: []string{"a"},
		},
		{
			name: "links inventory host by ansible_host", mode: ModeAddOnly,
			hosts:   []Host{fh("", "", "192.0.2.1")},
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "legacy", AnsibleHost: "192.0.2.1"}}},
			wantAdd: []string{"legacy"},
		},
		{
			name: "same inventory host from two fleet hosts is skipped", mode: ModeAddOnly,
			hosts:   []Host{fh("a.lan", "a", "192.0.2.1"), fh("a.lan", "a2", "192.0.2.2")},
			wantAdd: []string{"a.lan"}, wantCreate: true, wantSkip: 1,
		},
		{
			name: "host without name or address is skipped", mode: ModeAddOnly,
			hosts:   []Host{{ID: uuid.New(), Name: "aa:bb"}},
			wantAdd: []string{}, wantCreate: true, wantSkip: 1,
		},
		{
			name: "add-only never removes", mode: ModeAddOnly,
			hosts:   nil,
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "old"}}, Members: []string{"old"}},
			tracked: []string{"old"}, wantAdd: []string{},
		},
		{
			name: "sync removes only tracked", mode: ModeSync,
			hosts: []Host{fh("a.lan", "a", "192.0.2.1")},
			state: inventory.GroupState{GroupExists: true,
				Hosts:   []inventory.HostRef{{Name: "a.lan"}, {Name: "old"}, {Name: "manual"}},
				Members: []string{"a.lan", "manual", "old"}},
			tracked: []string{"a.lan", "old"}, wantKeep: 1, wantAdd: []string{}, wantRemove: []string{"old"},
		},
		{
			name: "sync keeps untracked manual member that no longer matches", mode: ModeSync,
			hosts:   nil,
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "manual"}}, Members: []string{"manual"}},
			wantAdd: []string{},
		},
		{
			name: "stale tracking is dropped", mode: ModeSync,
			hosts:   nil,
			state:   inventory.GroupState{GroupExists: true, Hosts: []inventory.HostRef{{Name: "gone"}}},
			tracked: []string{"gone"}, wantAdd: []string{}, wantUntrk: []string{"gone"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Diff(DiffInput{Matcher: all, Mode: tt.mode, Hosts: tt.hosts, State: tt.state, Tracked: tt.tracked})
			if got := addNames(p); !reflect.DeepEqual(got, tt.wantAdd) {
				t.Errorf("add = %v, want %v", got, tt.wantAdd)
			}
			if tt.wantRemove == nil {
				tt.wantRemove = []string{}
			}
			if tt.wantUntrk == nil {
				tt.wantUntrk = []string{}
			}
			if !reflect.DeepEqual(p.Remove, tt.wantRemove) {
				t.Errorf("remove = %v, want %v", p.Remove, tt.wantRemove)
			}
			if !reflect.DeepEqual(p.Untrack, tt.wantUntrk) {
				t.Errorf("untrack = %v, want %v", p.Untrack, tt.wantUntrk)
			}
			if p.Keep != tt.wantKeep || p.GroupCreate != tt.wantCreate || len(p.Skipped) != tt.wantSkip {
				t.Errorf("keep=%d create=%v skipped=%d, want %d %v %d", p.Keep, p.GroupCreate, len(p.Skipped), tt.wantKeep, tt.wantCreate, tt.wantSkip)
			}
		})
	}
}

func TestDiffCreatedHostCarriesAnsibleHostOnlyWhenNeeded(t *testing.T) {
	p := Diff(DiffInput{Matcher: matchAll{}, Mode: ModeAddOnly, Hosts: []Host{fh("", "short", "192.0.2.7"), fh("f.lan", "f", "192.0.2.8")}})
	got := map[string]AddItem{}
	for _, a := range p.Add {
		got[a.Name] = a
	}
	if a := got["short"]; !a.CreateHost || a.AnsibleHost != "192.0.2.7" {
		t.Errorf("short = %+v", a)
	}
	if a := got["f.lan"]; !a.CreateHost || a.AnsibleHost != "" {
		t.Errorf("f.lan = %+v", a)
	}
}

func TestDiffIsDeterministicAndPure(t *testing.T) {
	hosts := []Host{fh("c.lan", "c"), fh("a.lan", "a"), fh("b.lan", "b")}
	in := DiffInput{Matcher: matchAll{}, Mode: ModeSync, Hosts: hosts, State: inventory.GroupState{Members: []string{"z", "y"}}, Tracked: []string{"z", "y"}}
	first := Diff(in)
	if hosts[0].FQDN != "c.lan" {
		t.Error("input slice was reordered")
	}
	if second := Diff(in); !reflect.DeepEqual(first, second) {
		t.Error("diff is not deterministic")
	}
}

type matchAll struct{}

func (matchAll) Match(Host) bool { return true }
