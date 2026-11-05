package runs

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// testGraph: web{web1,web2,app(group){app1}}, db{db1}, ungrouped host lone1.
func testGraph() policy.Graph {
	h := map[string]uuid.UUID{}
	g := policy.Graph{Groups: map[uuid.UUID]string{}, Hosts: map[uuid.UUID]string{}}
	for _, n := range []string{"web1.example.test", "web2.example.test", "app1.example.test", "db1.example.test", "lone1.example.test"} {
		id := uuid.New()
		h[n] = id
		g.Hosts[id] = n
	}
	grp := map[string]uuid.UUID{}
	for _, n := range []string{"web", "app", "db"} {
		id := uuid.New()
		grp[n] = id
		g.Groups[id] = n
	}
	g.Links = []policy.GraphLink{{Parent: grp["web"], Child: grp["app"]}}
	mem := func(gr, ho string) { g.Members = append(g.Members, policy.GraphMember{Group: grp[gr], Host: h[ho]}) }
	mem("web", "web1.example.test")
	mem("web", "web2.example.test")
	mem("app", "app1.example.test")
	mem("db", "db1.example.test")
	return g
}

func TestResolveLimit(t *testing.T) {
	g := testGraph()
	w1, w2, a1, d1, l1 := "web1.example.test", "web2.example.test", "app1.example.test", "db1.example.test", "lone1.example.test"
	tests := []struct {
		name, pattern string
		want          []string
	}{
		{"empty is all", "", []string{a1, d1, l1, w1, w2}},
		{"all", "all", []string{a1, d1, l1, w1, w2}},
		{"star", "*", []string{a1, d1, l1, w1, w2}},
		{"host name", w1, []string{w1}},
		{"group includes descendants", "web", []string{a1, w1, w2}},
		{"child group", "app", []string{a1}},
		{"wildcard hosts", "web*", []string{a1, w1, w2}}, // matches group "web" and host names web*
		{"wildcard suffix", "*.example.test", []string{a1, d1, l1, w1, w2}},
		{"question mark", "web?.example.test", []string{w1, w2}},
		{"char class", "web[12].example.test", []string{w1, w2}},
		{"colon union", "db:" + l1, []string{d1, l1}},
		{"comma union", "db," + l1, []string{d1, l1}},
		{"intersection", "web:&app", []string{a1}},
		{"exclusion", "web:!app", []string{w1, w2}},
		{"exclusion host", "all:!" + d1, []string{a1, l1, w1, w2}},
		{"only exclusion starts from all", "!db", []string{a1, l1, w1, w2}},
		{"only intersection starts from all", "&db", []string{d1}},
		{"combined", "web:db:&*1*:!" + a1, []string{d1, w1}},
		{"ungrouped", "ungrouped", []string{l1}},
		{"unknown name", "nope", []string{}},
		{"order independent", "!db:web", []string{a1, w1, w2}},
		{"spaces trimmed", " db : " + l1 + " ", []string{d1, l1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveLimit(g, tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ResolveLimit(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestResolveLimitRejects(t *testing.T) {
	for _, p := range []string{"~web.*", "web[0]", "web[1:2]", "&", "!", "web:!", "we b"} {
		if _, err := ResolveLimit(testGraph(), p); !errors.Is(err, ErrInvalid) {
			t.Errorf("ResolveLimit(%q) err = %v, want ErrInvalid", p, err)
		}
	}
	if err := ValidatePattern("web:&app:!db"); err != nil {
		t.Errorf("valid pattern rejected: %v", err)
	}
}
