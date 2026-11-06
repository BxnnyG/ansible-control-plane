package remediation

import (
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

var (
	invA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	invB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

func fh(name string, ips []string, links ...fleet.AnsibleLink) fleet.HostView {
	return fleet.HostView{ID: uuid.New(), Name: name, IPs: ips, Ansible: links, Managed: len(links) > 0}
}

func link(inv uuid.UUID, host string) fleet.AnsibleLink {
	return fleet.AnsibleLink{InventoryID: inv, HostID: uuid.New(), Host: host}
}

func graphOf(groups map[string][]string) func() (policy.Graph, error) {
	return func() (policy.Graph, error) {
		g := policy.Graph{Groups: map[uuid.UUID]string{}, Hosts: map[uuid.UUID]string{}}
		ids := map[string]uuid.UUID{}
		for grp, hosts := range groups {
			gid := uuid.New()
			g.Groups[gid] = grp
			for _, h := range hosts {
				if _, ok := ids[h]; !ok {
					ids[h] = uuid.New()
					g.Hosts[ids[h]] = h
				}
				g.Members = append(g.Members, policy.GraphMember{Group: gid, Host: ids[h]})
			}
		}
		return g, nil
	}
}

func TestResolve(t *testing.T) {
	web1 := fh("web1", []string{"192.0.2.10"}, link(invA, "web1"))
	web2 := fh("web2", []string{"192.0.2.11"}, link(invA, "web2"), link(invB, "web2-b"))
	db1 := fh("db1", []string{"198.51.100.5"}, link(invA, "db1"))
	loose := fh("loose", []string{"192.0.2.99"})
	other := fh("other", []string{"192.0.2.50"}, link(invB, "other"))
	all := []fleet.HostView{web1, web2, db1, loose, other}
	nets := []Net{
		{ID: uuid.New(), Prefix: netip.MustParsePrefix("192.0.2.0/24")},
		{ID: uuid.New(), Prefix: netip.MustParsePrefix("198.51.100.0/24")},
		{ID: uuid.New(), Prefix: netip.MustParsePrefix("192.0.0.0/16")},
	}
	graph := graphOf(map[string][]string{"webservers": {"web1", "web2"}, "dbs": {"db1"}})

	for _, tt := range []struct {
		name, target, group string
		host                fleet.HostView
		wantLimit           string
		wantHosts           []string
		wantErr             bool
	}{
		{"host", TargetHost, "", web1, "web1", []string{"web1"}, false},
		{"host uses only the template inventory", TargetHost, "", web2, "web2", []string{"web2"}, false},
		{"host without link", TargetHost, "", loose, "", nil, true},
		{"host linked to another inventory only", TargetHost, "", other, "", nil, true},
		{"group containing the host", TargetGroup, "webservers", web1, "webservers", []string{"web1", "web2"}, false},
		{"group not containing the host", TargetGroup, "dbs", web1, "", nil, true},
		{"unknown group", TargetGroup, "ghosts", web1, "", nil, true},
		{"network picks the most specific prefix and skips unlinked hosts", TargetNetwork, "", web1, "web1,web2", []string{"web1", "web2"}, false},
		{"other network", TargetNetwork, "", db1, "db1", []string{"db1"}, false},
		{"unknown target", "galaxy", "", web1, "", nil, true},
	} {
		limit, hosts, err := Resolve(tt.target, tt.group, invA, tt.host, all, nets, graph)
		if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrInvalid)) {
			t.Errorf("%s: err %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if limit != tt.wantLimit || !slices.Equal(hosts, tt.wantHosts) {
			t.Errorf("%s: limit %q hosts %v, want %q %v", tt.name, limit, hosts, tt.wantLimit, tt.wantHosts)
		}
	}
}

func TestNetworkOf(t *testing.T) {
	wide := Net{ID: uuid.New(), Prefix: netip.MustParsePrefix("10.0.0.0/8")}
	narrow := Net{ID: uuid.New(), Prefix: netip.MustParsePrefix("10.1.0.0/16")}
	if n, ok := NetworkOf([]Net{wide, narrow}, []string{"10.1.2.3"}); !ok || n.ID != narrow.ID {
		t.Errorf("most specific: %v %v", n, ok)
	}
	// The primary (first IPv4) address wins over later ones.
	if n, ok := NetworkOf([]Net{wide, narrow}, []string{"fd00::1", "10.9.9.9", "10.1.2.3"}); !ok || n.ID != wide.ID {
		t.Errorf("primary address: %v %v", n, ok)
	}
	if _, ok := NetworkOf([]Net{wide}, []string{"192.0.2.1", "bogus"}); ok {
		t.Error("address outside every network matched")
	}
}
