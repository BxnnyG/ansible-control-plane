package fleet

import (
	"testing"

	"github.com/google/uuid"
)

func TestMatchHost(t *testing.T) {
	id := func() uuid.UUID { return uuid.New() }
	web, web2, db, dup1, dup2 := id(), id(), id(), id(), id()
	hosts := []linkHost{
		newLinkHost(web, "web1", Fields{Hostname: "web1", FQDN: "web1.example.test", IPs: []string{"192.0.2.10"}}),
		newLinkHost(web2, "web2.example.test", Fields{IPs: []string{"192.0.2.11", "2001:db8::11"}}),
		newLinkHost(db, "db-main", Fields{Hostname: "DB1", IPs: []string{"192.0.2.20"}}),
		newLinkHost(dup1, "app.a.example.test", Fields{IPs: []string{"192.0.2.31"}}),
		newLinkHost(dup2, "app.b.example.test", Fields{IPs: []string{"192.0.2.32"}}),
	}
	tests := []struct {
		name, host, ip string
		want           uuid.UUID
	}{
		{"exact name", "web1", "", web},
		{"case-insensitive hostname", "db1", "", db},
		{"fqdn alert, fqdn host", "WEB1.example.test", "", web},
		{"short alert vs fqdn host name", "web2", "", web2},
		{"fqdn alert vs short host", "web1.other.test", "", web},
		{"address only", "", "192.0.2.20", db},
		{"ipv6 address", "", "2001:DB8::11", web2},
		{"unknown name falls back to address", "nope", "192.0.2.11", web2},
		{"name wins over conflicting address", "web1", "192.0.2.20", web},
		{"ambiguous short name", "app", "", uuid.Nil},
		{"ambiguous short name narrowed by ip", "app", "192.0.2.32", dup2},
		{"ambiguous short name, foreign ip", "app", "192.0.2.99", uuid.Nil},
		{"no match", "nope", "198.51.100.1", uuid.Nil},
		{"empty", "", "", uuid.Nil},
		{"invalid ip", "", "not-an-ip", uuid.Nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := matchHost(hosts, tc.host, tc.ip)
			if ok != (tc.want != uuid.Nil) || got != tc.want {
				t.Errorf("got (%v,%v), want %v", got, ok, tc.want)
			}
		})
	}
	// An address shared by two hosts is ambiguous.
	shared := []linkHost{newLinkHost(id(), "a", Fields{IPs: []string{"192.0.2.50"}}), newLinkHost(id(), "b", Fields{IPs: []string{"192.0.2.50"}})}
	if _, ok := matchHost(shared, "", "192.0.2.50"); ok {
		t.Error("shared address must not link")
	}
}

func TestSeverityRank(t *testing.T) {
	for s, r := range severityRank {
		if rankSeverity(r) != s {
			t.Errorf("round trip %s", s)
		}
	}
	if rankSeverity(0) != "" {
		t.Error("rank 0 means no alerts")
	}
}
