package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

const connSecretValue = "connector-secret-value-xyz"

type fakeConnectors struct {
	conn     dbgen.Connector
	secretID uuid.UUID
	err      error
	testErr  error
	created  []connectors.CreateInput
	updated  []connectors.UpdateInput
	tested   []connectors.TestInput
	syncs    int
	deleted  int

	generated string
	hookID    uuid.UUID
	hookErr   error
	hookAuth  string
	hookBody  []byte
}

func (f *fakeConnectors) Specs() []connectors.Spec {
	return []connectors.Spec{{Type: "opnsense", Label: "OPNsense", BaseURL: true, Help: "help"}}
}

func (f *fakeConnectors) row() dbgen.Connector {
	c := f.conn
	if c.ID == uuid.Nil {
		c = dbgen.Connector{ID: uuid.New(), Type: "opnsense", Name: "fw", BaseUrl: "https://fw.example.test", Config: []byte(`{"tls_skip_verify":true}`),
			SecretID: pgtype.UUID{Bytes: f.secretID, Valid: true}, IntervalSeconds: 300, Enabled: true, LastStatus: "ok",
			LastCounts: []byte(`{"networks":2,"observations":5,"alerts":0,"warnings":["optional endpoint failed"]}`),
			NextSyncAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	}
	return c
}

func (f *fakeConnectors) List(context.Context, uuid.UUID) ([]dbgen.Connector, error) {
	return []dbgen.Connector{f.row()}, f.err
}
func (f *fakeConnectors) Get(context.Context, uuid.UUID, uuid.UUID) (dbgen.Connector, error) {
	return f.row(), f.err
}
func (f *fakeConnectors) CreateGenerating(_ context.Context, _ uuid.UUID, in connectors.CreateInput) (dbgen.Connector, string, error) {
	f.created = append(f.created, in)
	return f.row(), f.generated, f.err
}
func (f *fakeConnectors) HandleAlertWebhook(_ context.Context, id uuid.UUID, authz string, body []byte) (uuid.UUID, int, error) {
	f.hookAuth, f.hookBody = authz, body
	if id != f.hookID {
		return uuid.Nil, 0, connectors.ErrWebhookRejected
	}
	return uuid.New(), 2, f.hookErr
}
func (f *fakeConnectors) Update(_ context.Context, _, _ uuid.UUID, in connectors.UpdateInput) (dbgen.Connector, error) {
	f.updated = append(f.updated, in)
	return f.row(), f.err
}
func (f *fakeConnectors) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	f.deleted++
	return f.err
}
func (f *fakeConnectors) Test(context.Context, uuid.UUID, uuid.UUID) error { return f.testErr }
func (f *fakeConnectors) TestConfig(_ context.Context, _ uuid.UUID, in connectors.TestInput) error {
	f.tested = append(f.tested, in)
	if f.err != nil {
		return f.err
	}
	return f.testErr
}
func (f *fakeConnectors) RequestSync(context.Context, uuid.UUID, uuid.UUID) error {
	f.syncs++
	return f.err
}
func (f *fakeConnectors) ListSyncs(context.Context, uuid.UUID, uuid.UUID, int) ([]dbgen.ConnectorSync, error) {
	return []dbgen.ConnectorSync{{ID: uuid.New(), Trigger: "manual", Status: "ok", Counts: []byte(`{"networks":1}`)}}, f.err
}
func (f *fakeConnectors) SecretRefs(context.Context, uuid.UUID) (map[uuid.UUID]connectors.SecretRef, error) {
	return map[uuid.UUID]connectors.SecretRef{f.secretID: {ID: f.secretID, Name: "fw-key", Type: "api_key_pair"}}, nil
}

type fakeFleet struct {
	hosts     []fleet.HostView
	conflicts []fleet.ConflictView
	netPrefix map[uuid.UUID]netip.Prefix
	acked     []uuid.UUID
	alerts    []fleet.AlertView
	lastAlert fleet.AlertFilter
	lastHosts fleet.HostFilter
	tracking  trackingCalls
}

// trackingCalls records the network tracking calls of the fleet fake.
type trackingCalls struct {
	setIDs     []uuid.UUID
	setTracked bool
	only       []uuid.UUID
	onlyCalled bool
	newDefault bool
	unknown    bool // SetTracked finds no network
}

func (f *fakeFleet) SetTracked(_ context.Context, _ uuid.UUID, ids []uuid.UUID, tracked bool) (int, error) {
	f.tracking.setIDs, f.tracking.setTracked = ids, tracked
	if f.tracking.unknown {
		return 0, nil
	}
	return len(ids), nil
}

func (f *fakeFleet) TrackOnly(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (int, error) {
	f.tracking.only, f.tracking.onlyCalled = ids, true
	return 2, nil
}

func (f *fakeFleet) NewNetworksTracked(context.Context, uuid.UUID) (bool, error) {
	return f.tracking.newDefault, nil
}

func (f *fakeFleet) SetNewNetworksTracked(_ context.Context, _ uuid.UUID, v bool) error {
	f.tracking.newDefault = v
	return nil
}

func (f *fakeFleet) Alerts(_ context.Context, _ uuid.UUID, fl fleet.AlertFilter, v fleet.Visible) ([]fleet.AlertView, error) {
	f.lastAlert = fl
	out := []fleet.AlertView{}
	for _, a := range f.alerts {
		if v != nil {
			ok := false
			for _, h := range f.hosts {
				if a.HostID != nil && h.ID == *a.HostID && v(h.IPs) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, a)
	}
	return out, nil
}

func (f *fakeFleet) Networks(_ context.Context, _ uuid.UUID, allow func(uuid.UUID, netip.Prefix) bool, v fleet.Visible) ([]fleet.NetworkView, error) {
	out := []fleet.NetworkView{}
	for id, p := range f.netPrefix {
		if allow == nil || allow(id, p) {
			nv := fleet.NetworkView{ID: id, CIDR: p.String()}
			for _, h := range f.hosts {
				for _, ip := range h.IPs {
					if a, err := netip.ParseAddr(ip); err == nil && p.Contains(a) && (v == nil || v(h.IPs)) {
						nv.HostCount++
						break
					}
				}
			}
			out = append(out, nv)
		}
	}
	return out, nil
}

func (f *fakeFleet) Hosts(_ context.Context, _ uuid.UUID, fl fleet.HostFilter, v fleet.Visible) ([]fleet.HostView, error) {
	f.lastHosts = fl
	out := []fleet.HostView{}
	for _, h := range f.hosts {
		if (v == nil || v(h.IPs)) && (!fl.Conflicts || h.OpenConflicts > 0) && (fl.Q == "" || strings.Contains(h.Name, fl.Q)) {
			out = append(out, h)
		}
	}
	return out, nil
}

func (f *fakeFleet) Host(_ context.Context, _, id uuid.UUID, v fleet.Visible) (fleet.HostDetail, error) {
	for _, h := range f.hosts {
		if h.ID == id && (v == nil || v(h.IPs)) {
			return fleet.HostDetail{HostView: h, Keys: []fleet.Key{}, Observations: []fleet.ObservationView{}, Conflicts: f.conflicts}, nil
		}
	}
	return fleet.HostDetail{}, fleet.ErrNotFound
}

func (f *fakeFleet) visibleConflict(c fleet.ConflictView, v fleet.Visible) bool {
	if v == nil {
		return true
	}
	for _, id := range c.HostIDs {
		ok := false
		for _, h := range f.hosts {
			ok = ok || (h.ID == id && v(h.IPs))
		}
		if !ok {
			return false
		}
	}
	return true
}

func (f *fakeFleet) Conflicts(_ context.Context, _ uuid.UUID, state string, _ bool, v fleet.Visible) ([]fleet.ConflictView, error) {
	out := []fleet.ConflictView{}
	for _, c := range f.conflicts {
		if (state == "" || c.State == state) && f.visibleConflict(c, v) {
			c.Hosts = []fleet.ConflictHost{}
			for _, id := range c.HostIDs {
				for _, h := range f.hosts {
					if h.ID == id {
						c.Hosts = append(c.Hosts, fleet.ConflictHost{ID: id, Name: h.Name})
					}
				}
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeFleet) Acknowledge(_ context.Context, _, id, _ uuid.UUID, v fleet.Visible) (fleet.ConflictView, error) {
	for _, c := range f.conflicts {
		if c.ID == id && c.State == "open" && f.visibleConflict(c, v) {
			f.acked = append(f.acked, id)
			c.State = "acknowledged"
			return c, nil
		}
	}
	return fleet.ConflictView{}, fleet.ErrNotFound
}

type fakeNetSource struct{ prefixes map[uuid.UUID]netip.Prefix }

func (f fakeNetSource) NetworkPrefixes(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, id := range ids {
		if p, ok := f.prefixes[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f fakeNetSource) FleetHosts(context.Context, uuid.UUID) ([]policy.FleetHost, error) {
	return nil, nil
}

type connEnv struct {
	h      http.Handler
	conns  *fakeConnectors
	fleet  *fakeFleet
	adder  *fakeAdder
	audits *[]string
}

func newConnEnv(t *testing.T, role string, scopes ...dbgen.MembershipScope) *connEnv {
	t.Helper()
	uid := uuid.New()
	audits := []string{}
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes, audits: &audits}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	netA, netB := uuid.MustParse("00000000-0000-0000-0000-00000000000a"), uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	prefixes := map[uuid.UUID]netip.Prefix{netA: netip.MustParsePrefix("192.0.2.0/24"), netB: netip.MustParsePrefix("198.51.100.0/24")}
	fc := &fakeConnectors{secretID: uuid.New(), hookID: uuid.New()}
	hA := fleet.HostView{ID: uuid.New(), Name: "host-a", IPs: []string{"192.0.2.10"}, OpenConflicts: 1}
	hB := fleet.HostView{ID: uuid.New(), Name: "host-b", IPs: []string{"198.51.100.10"}}
	hA2 := fleet.HostView{ID: uuid.New(), Name: "host-a2", IPs: []string{"192.0.2.11"}}
	ff := &fakeFleet{hosts: []fleet.HostView{hA, hB, hA2}, netPrefix: prefixes, conflicts: []fleet.ConflictView{
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000c1"), Type: "ip_conflict", State: "open", HostIDs: []uuid.UUID{hA.ID, hA2.ID}},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000c2"), Type: "ip_conflict", State: "open", HostIDs: []uuid.UUID{hA.ID, hB.ID}},
		{ID: uuid.MustParse("00000000-0000-0000-0000-0000000000c3"), Type: "host_disappeared", State: "resolved", HostIDs: []uuid.UUID{hB.ID}},
	}}
	fa := &fakeAdder{}
	az := policy.New(st, policy.InventoryResolver{Networks: policy.NetworkSource(fakeNetSource{prefixes})}, authn, quiet)
	h := Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(az, st), WithConnectors(fc), WithFleet(ff), WithFleetAdd(fa))
	return &connEnv{h: h, conns: fc, fleet: ff, adder: fa, audits: &audits}
}

func (e *connEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/workspaces/default"+path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func netScope(id string) dbgen.MembershipScope {
	return dbgen.MembershipScope{Kind: policy.KindNetwork, RefID: id}
}

func TestConnectorsAPIAuthorization(t *testing.T) {
	cid := uuid.NewString()
	const create = `{"type":"opnsense","name":"fw","base_url":"https://fw.example.test"}`
	type call struct{ method, path, body string }
	reads := []call{{"GET", "/connector-types", ""}, {"GET", "/connectors", ""}, {"GET", "/connectors/" + cid, ""}, {"GET", "/connectors/" + cid + "/syncs", ""}}
	writes := []call{{"POST", "/connectors", create}, {"PATCH", "/connectors/" + cid, `{"enabled":false}`}, {"DELETE", "/connectors/" + cid, ""},
		{"POST", "/connectors/" + cid + "/test", ""}, {"POST", "/connectors/test", `{"type":"opnsense"}`}, {"POST", "/connectors/" + cid + "/sync", ""}}
	for _, role := range []string{policy.RoleViewer, policy.RoleOperator, policy.RoleEditor, policy.RoleAdmin, policy.RoleOwner} {
		admin := role == policy.RoleAdmin || role == policy.RoleOwner
		for _, c := range append(slices.Clone(reads), writes...) {
			rec := newConnEnv(t, role).do(c.method, c.path, c.body)
			denied := rec.Code == http.StatusForbidden
			if denied == admin {
				t.Errorf("%s %s %s: status %d (admin=%v)", role, c.method, c.path, rec.Code, admin)
			}
		}
	}
	// A network-scoped admin never reaches the connector endpoints.
	for _, c := range append(slices.Clone(reads), writes...) {
		if rec := newConnEnv(t, policy.RoleAdmin, netScope(uuid.NewString())).do(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("scoped admin %s %s: %d", c.method, c.path, rec.Code)
		}
	}
}

func TestConnectorsAPIBehaviour(t *testing.T) {
	e := newConnEnv(t, policy.RoleAdmin)
	cid := uuid.NewString()

	rec := e.do("GET", "/connector-types", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"help":"help"`) || !strings.Contains(rec.Body.String(), `"secret_types":[]`) {
		t.Errorf("types: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do("GET", "/connectors", "")
	var list []connectorView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	v := list[0]
	if v.Status != "ok" || v.Secret == nil || v.Secret.Name != "fw-key" || v.Secret.Type != "api_key_pair" || len(v.Warnings) != 1 || v.Counts.Networks != 2 {
		t.Errorf("view: %+v", v)
	}
	if strings.Contains(rec.Body.String(), connSecretValue) || strings.Contains(rec.Body.String(), `"value"`) {
		t.Error("secret value leaked")
	}

	// Create passes the typed input through and audits.
	sid := uuid.New()
	rec = e.do("POST", "/connectors", `{"type":"opnsense","name":"fw","base_url":"https://fw.example.test","secret_id":"`+sid.String()+`","interval_seconds":60,"enabled":false}`)
	if rec.Code != 201 || len(e.conns.created) != 1 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	in := e.conns.created[0]
	if in.Enabled || in.IntervalSeconds != 60 || in.SecretID == nil || *in.SecretID != sid {
		t.Errorf("create input: %+v", in)
	}
	e.do("POST", "/connectors", `{"type":"opnsense","name":"fw2"}`)
	if !e.conns.created[1].Enabled {
		t.Error("connectors must default to enabled")
	}
	if rec = e.do("POST", "/connectors", `{"type":"opnsense","name":"x","password":"`+connSecretValue+`"}`); rec.Code != 400 {
		t.Errorf("unknown field (no inline secrets): %d", rec.Code)
	}
	if rec = e.do("POST", "/connectors", `{"type":"opnsense","name":"x","interval_seconds":99999999}`); rec.Code != 422 {
		t.Errorf("huge interval: %d", rec.Code)
	}

	// PATCH: absent secret keeps it, null clears it, value sets it.
	e.do("PATCH", "/connectors/"+cid, `{"enabled":false}`)
	e.do("PATCH", "/connectors/"+cid, `{"secret_id":null}`)
	e.do("PATCH", "/connectors/"+cid, `{"secret_id":"`+sid.String()+`","interval_seconds":120}`)
	u := e.conns.updated
	if len(u) != 3 || u[0].SecretID != nil || u[0].ClearSecret || !u[1].ClearSecret || u[2].SecretID == nil || *u[2].SecretID != sid || *u[2].IntervalSeconds != 120 {
		t.Errorf("updates: %+v", u)
	}
	if rec = e.do("PATCH", "/connectors/"+cid, `{}`); rec.Code != 422 {
		t.Errorf("empty patch: %d", rec.Code)
	}
	if rec = e.do("PATCH", "/connectors/"+cid, `{"secret_id":"nope"}`); rec.Code != 400 {
		t.Errorf("bad secret id: %d", rec.Code)
	}

	// Stored test: a failing probe is a 200 with ok=false and a masked message.
	e.conns.testErr = errors.New("connection refused")
	rec = e.do("POST", "/connectors/"+cid+"/test", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("failed probe: %d %s", rec.Code, rec.Body.String())
	}
	e.conns.testErr = nil
	if rec = e.do("POST", "/connectors/test", `{"type":"opnsense","base_url":"https://fw.example.test","config":{"tls_skip_verify":true}}`); rec.Code != 200 ||
		!strings.Contains(rec.Body.String(), `"ok":true`) || len(e.conns.tested) != 1 || e.conns.tested[0].BaseURL == "" {
		t.Errorf("unsaved test: %d %s", rec.Code, rec.Body.String())
	}
	e.conns.err = connectors.ErrInvalidConfig
	if rec = e.do("POST", "/connectors/test", `{"type":"opnsense"}`); rec.Code != 422 {
		t.Errorf("invalid unsaved config: %d", rec.Code)
	}
	e.conns.err = nil

	if rec = e.do("POST", "/connectors/"+cid+"/sync", ""); rec.Code != 202 || e.conns.syncs != 1 {
		t.Errorf("sync: %d", rec.Code)
	}
	e.conns.conn = e.conns.row()
	e.conns.conn.Enabled = false
	if rec = e.do("POST", "/connectors/"+cid+"/sync", ""); rec.Code != 409 || e.conns.syncs != 1 {
		t.Errorf("sync of disabled connector: %d", rec.Code)
	}
	e.conns.conn = dbgen.Connector{}

	if rec = e.do("GET", "/connectors/"+cid+"/syncs", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"trigger":"manual"`) {
		t.Errorf("syncs: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do("GET", "/connectors/"+cid+"/syncs?limit=0", ""); rec.Code != 400 {
		t.Errorf("bad limit: %d", rec.Code)
	}
	if rec = e.do("DELETE", "/connectors/"+cid, ""); rec.Code != 204 || e.conns.deleted != 1 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec = e.do("GET", "/connectors/nope", ""); rec.Code != 404 {
		t.Errorf("bad id: %d", rec.Code)
	}

	want := []string{"connector.create", "connector.create", "connector.update", "connector.update", "connector.update", "connector.test", "connector.test",
		"connector.sync", "connector.delete"}
	if !slices.Equal(*e.audits, want) {
		t.Errorf("audits %v, want %v", *e.audits, want)
	}
}

func TestConnectorsAPIErrorMapping(t *testing.T) {
	for err, want := range map[error]int{connectors.ErrNotFound: 404, connectors.ErrConflict: 409, connectors.ErrInvalidConfig: 422,
		connectors.ErrUnknownType: 422, context.DeadlineExceeded: 500} {
		e := newConnEnv(t, policy.RoleAdmin)
		e.conns.err = err
		rec := e.do("POST", "/connectors", `{"type":"opnsense","name":"fw"}`)
		if rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
		if want == 500 && strings.Contains(rec.Body.String(), "deadline") {
			t.Error("internal error detail leaked")
		}
		if len(*e.audits) != 0 {
			t.Errorf("%v: failed create audited %v", err, *e.audits)
		}
	}
}

func TestFleetAPIAuthorizationAndAudit(t *testing.T) {
	c1 := "/conflicts/00000000-0000-0000-0000-0000000000c1/ack"
	tests := []struct {
		role, method, path string
		want               int
		audit              string
	}{
		{policy.RoleViewer, "GET", "/hosts", 200, ""},
		{policy.RoleViewer, "GET", "/networks", 200, ""},
		{policy.RoleViewer, "GET", "/conflicts", 200, ""},
		{policy.RoleViewer, "POST", c1, 403, ""},
		{policy.RoleOperator, "POST", c1, 403, ""},
		{policy.RoleEditor, "POST", c1, 200, "fleet.conflict.ack"},
		{policy.RoleAdmin, "POST", c1, 200, "fleet.conflict.ack"},
		{policy.RoleEditor, "POST", "/conflicts/00000000-0000-0000-0000-0000000000c3/ack", 404, ""},
		{policy.RoleEditor, "POST", "/conflicts/nope/ack", 404, ""},
		{policy.RoleViewer, "GET", "/hosts/nope", 404, ""},
		{policy.RoleViewer, "GET", "/conflicts?state=bogus", 400, ""},
		{policy.RoleViewer, "GET", "/hosts?network=nope", 404, ""},
		{policy.RoleViewer, "GET", "/hosts?conflicts=maybe", 400, ""},
	}
	for _, tc := range tests {
		t.Run(tc.role+" "+tc.method+" "+tc.path, func(t *testing.T) {
			e := newConnEnv(t, tc.role)
			rec := e.do(tc.method, tc.path, "")
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			got := *e.audits
			if tc.audit != "" && (len(got) != 1 || got[0] != tc.audit) {
				t.Errorf("audits %v, want %s", got, tc.audit)
			}
			if tc.audit == "" && tc.want < 400 && len(got) != 0 {
				t.Errorf("unexpected audits %v", got)
			}
		})
	}
}

func TestFleetAPIFiltersAndScopes(t *testing.T) {
	names := func(t *testing.T, rec *httptest.ResponseRecorder) []string {
		t.Helper()
		var hs []fleet.HostView
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &hs) != nil {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var out []string
		for _, h := range hs {
			out = append(out, h.Name)
		}
		slices.Sort(out)
		return out
	}
	netA := "00000000-0000-0000-0000-00000000000a"

	unscoped := newConnEnv(t, policy.RoleViewer)
	if got := names(t, unscoped.do("GET", "/hosts", "")); !slices.Equal(got, []string{"host-a", "host-a2", "host-b"}) {
		t.Errorf("unscoped hosts %v", got)
	}
	if got := names(t, unscoped.do("GET", "/hosts?conflicts=true", "")); !slices.Equal(got, []string{"host-a"}) {
		t.Errorf("conflict filter %v", got)
	}

	scoped := newConnEnv(t, policy.RoleViewer, netScope(netA))
	if got := names(t, scoped.do("GET", "/hosts", "")); !slices.Equal(got, []string{"host-a", "host-a2"}) {
		t.Errorf("scoped hosts %v", got)
	}
	var nets []fleet.NetworkView
	_ = json.Unmarshal(scoped.do("GET", "/networks", "").Body.Bytes(), &nets)
	if len(nets) != 1 || nets[0].ID.String() != netA || nets[0].HostCount != 2 {
		t.Errorf("scoped networks %+v", nets)
	}
	var allNets []fleet.NetworkView
	_ = json.Unmarshal(unscoped.do("GET", "/networks", "").Body.Bytes(), &allNets)
	total := 0
	for _, n := range allNets {
		total += n.HostCount
	}
	if len(allNets) != 2 || total != 3 {
		t.Errorf("unscoped host counts %+v", allNets)
	}
	hostB := scoped.fleet.hosts[1].ID.String()
	if rec := scoped.do("GET", "/hosts/"+hostB, ""); rec.Code != 404 {
		t.Errorf("host outside scope: %d", rec.Code)
	}
	if rec := scoped.do("GET", "/hosts/"+scoped.fleet.hosts[0].ID.String(), ""); rec.Code != 200 {
		t.Errorf("host inside scope: %d", rec.Code)
	} else {
		// The conflict c2 involves host-b outside the scope and must not leak.
		var d fleet.HostDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		if len(d.Conflicts) != 1 || d.Conflicts[0].ID.String() != "00000000-0000-0000-0000-0000000000c1" {
			t.Errorf("detail conflicts %+v", d.Conflicts)
		}
	}
	var cs []fleet.ConflictView
	_ = json.Unmarshal(scoped.do("GET", "/conflicts", "").Body.Bytes(), &cs)
	if len(cs) != 1 || cs[0].Type != "ip_conflict" || len(cs[0].Hosts) != 2 || cs[0].Hosts[0].Name != "host-a" {
		t.Errorf("scoped conflicts %+v", cs)
	}

	// A scoped editor may only acknowledge conflicts fully inside the scope.
	ed := newConnEnv(t, policy.RoleEditor, netScope(netA))
	if rec := ed.do("POST", "/conflicts/00000000-0000-0000-0000-0000000000c2/ack", ""); rec.Code != 404 || len(*ed.audits) != 0 {
		t.Errorf("ack outside scope: %d %v", rec.Code, *ed.audits)
	}
	if rec := ed.do("POST", "/conflicts/00000000-0000-0000-0000-0000000000c1/ack", ""); rec.Code != 200 {
		t.Errorf("ack inside scope: %d", rec.Code)
	}

	// Scopes that are not networks (or unknown networks) see no fleet data.
	inv := newConnEnv(t, policy.RoleViewer, dbgen.MembershipScope{Kind: policy.KindInventory, RefID: uuid.NewString()})
	if got := names(t, inv.do("GET", "/hosts", "")); len(got) != 0 {
		t.Errorf("inventory-scoped viewer sees hosts %v", got)
	}
	ghost := newConnEnv(t, policy.RoleViewer, netScope(uuid.NewString()))
	if got := names(t, ghost.do("GET", "/hosts", "")); len(got) != 0 {
		t.Errorf("unknown-network viewer sees hosts %v", got)
	}
}
