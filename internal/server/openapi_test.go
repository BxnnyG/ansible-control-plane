package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/BxnnyG/ansible-control-plane/api"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// apiRouteTable registers every API route group on a bare router (services
// are nil: registration never calls them) and returns "METHOD pattern" ->
// declared permission. Routes outside the policy package are "public" unless
// listed in sessionOnly.
func apiRouteTable(t *testing.T) map[string]string {
	t.Helper()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default"}, user: uuid.New(), role: policy.RoleViewer}
	az := policy.New(st, nil, func(next http.Handler) http.Handler { return next }, quiet)
	r := chi.NewRouter()
	lim := newHookLimiter(60, time.Minute)
	noop := func(http.ResponseWriter, *http.Request) {}
	// Routes of auth.Service.Routes (registered conditionally on config).
	r.Get("/api/auth/config", noop)
	r.Get("/api/auth/oidc/login", noop)
	r.Get("/api/auth/oidc/callback", noop)
	r.Post("/api/auth/breakglass/login", noop)
	r.Post("/api/auth/logout", noop)
	r.Get("/api/v1/me", noop)
	apiRoutes(r, az, st, nil, quiet)
	secretRoutes(r, az, st, nil, quiet)
	projectRoutes(r, az, st, nil, quiet)
	webhookRoutes(r, az, st, nil, "", lim, quiet)
	runRoutes(r, az, st, nil, &runStream{}, quiet)
	scheduleRoutes(r, az, st, nil, lim, "", quiet)
	inventoryRoutes(r, az, st, nil, quiet)
	connectorRoutes(r, az, st, nil, lim, "", quiet)
	fleetRoutes(r, az, st, nil, stubAdder{}, quiet)
	groupRuleRoutes(r, az, st, nil, quiet)
	remediationRoutes(r, az, st, nil, nil, quiet)
	vaultRoutes(r, az, st, nil, nil, quiet)
	notificationRoutes(r, az, st, nil, quiet)

	sessionOnly := map[string]bool{"POST /api/auth/logout": true, "GET /api/v1/me": true}
	out := map[string]string{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if perm, ok := az.Declared(method, route); ok {
			out[key] = perm
		} else if sessionOnly[key] {
			out[key] = "authenticated"
		} else {
			out[key] = "public"
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var specMethods = map[string]string{"get": "GET", "put": "PUT", "post": "POST", "delete": "DELETE", "patch": "PATCH", "head": "HEAD", "options": "OPTIONS"}

// specOperations returns "METHOD path" -> x-acp-permission of the OpenAPI document.
func specOperations(t *testing.T) (map[string]string, map[string]any) {
	t.Helper()
	var doc struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(api.Spec, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi version %q, want 3.1.x", doc.OpenAPI)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(api.Spec, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for path, item := range doc.Paths {
		for key, op := range item {
			method, ok := specMethods[key]
			if !ok {
				continue // parameters, summary, ...
			}
			fields, _ := op.(map[string]any)
			perm, _ := fields["x-acp-permission"].(string)
			out[method+" "+path] = perm
		}
	}
	return out, raw
}

// TestOpenAPIMatchesRoutes fails when a registered route has no operation in
// api/openapi.yaml (or the reverse), or when x-acp-permission differs from the
// permission the route declares.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	routes := apiRouteTable(t)
	spec, _ := specOperations(t)
	var problems []string
	for key, perm := range routes {
		sp, ok := spec[key]
		switch {
		case !ok:
			problems = append(problems, "route without spec operation: "+key)
		case sp != perm:
			problems = append(problems, "permission mismatch for "+key+": route "+perm+", spec "+sp)
		}
	}
	for key := range spec {
		if _, ok := routes[key]; !ok {
			problems = append(problems, "spec operation without route: "+key)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// TestOpenAPIReferences checks that every $ref points at an existing component
// and every operation id is unique and present.
func TestOpenAPIReferences(t *testing.T) {
	_, raw := specOperations(t)
	ids := map[string]string{}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Errorf("%s: non-local $ref %q", path, ref)
				} else {
					var cur any = raw
					for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
						m, _ := cur.(map[string]any)
						cur = m[part]
					}
					if cur == nil {
						t.Errorf("%s: unresolved $ref %s", path, ref)
					}
				}
			}
			for k, c := range x {
				walk(c, path+"/"+k)
			}
		case []any:
			for i, c := range x {
				walk(c, path+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(raw, "#")
	paths, _ := raw["paths"].(map[string]any)
	for p, item := range paths {
		m, _ := item.(map[string]any)
		for key, op := range m {
			if _, ok := specMethods[key]; !ok {
				continue
			}
			o, _ := op.(map[string]any)
			id, _ := o["operationId"].(string)
			if id == "" {
				t.Errorf("%s %s: missing operationId", key, p)
			} else if prev, dup := ids[id]; dup {
				t.Errorf("operationId %q used by %s and %s %s", id, prev, key, p)
			}
			ids[id] = key + " " + p
		}
	}
}

type stubAdder struct{ FleetAdder }
