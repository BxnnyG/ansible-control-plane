package remediation_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/remediation"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const scanJSON = `{"playbooks":[{"path":"fix.yml"}],"roles":[]}`

// fakeAuthz allows only the listed inventory host names, as a scoped caller would.
type fakeAuthz struct {
	allowed map[string]bool
	denied  int
}

func (f *fakeAuthz) AuthorizeHosts(_ context.Context, _ auth.Principal, _ uuid.UUID, _ policy.Permission, _ uuid.UUID, hosts []string) error {
	for _, h := range hosts {
		if !f.allowed[h] {
			f.denied++
			return policy.ErrForbidden
		}
	}
	return nil
}

type env struct {
	svc     *remediation.Service
	runs    *runs.Service
	fleet   *fleet.Store
	q       *dbgen.Queries
	az      *fakeAuthz
	ws      uuid.UUID
	user    dbgen.User
	actor   runs.Actor
	tplID   uuid.UUID
	conn    dbgen.Connector
	otherTp uuid.UUID
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "r" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), Name: "R"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	user, err := q.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
		OidcIssuer: pgtype.Text{String: "https://idp.example.test", Valid: true}, OidcSubject: pgtype.Text{String: uuid.NewString(), Valid: true},
		Email: "u@example.test", DisplayName: "U",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{
		ProjectID: prj.ID, Sha: "0123456789abcdef0123456789abcdef01234567", Scan: []byte(scanJSON), Warnings: []byte("[]"),
	}); err != nil {
		t.Fatal(err)
	}
	inv := inventory.New(inventory.Options{DB: pool})
	mkInv := func(name string, hosts map[string]string) uuid.UUID {
		iv, err := inv.Create(ctx, ws.ID, inventory.CreateInput{Name: name, Mode: inventory.ModeDB})
		if err != nil {
			t.Fatal(err)
		}
		groups := map[string]uuid.UUID{}
		for _, g := range hosts {
			if _, ok := groups[g]; ok {
				continue
			}
			grp, _, err := inv.CreateGroup(ctx, ws.ID, iv.ID, inventory.GroupInput{Name: g}, nil)
			if err != nil {
				t.Fatal(err)
			}
			groups[g] = grp.ID
		}
		for h, g := range hosts {
			if _, _, err := inv.CreateHost(ctx, ws.ID, iv.ID, inventory.HostInput{Name: h, GroupIDs: []uuid.UUID{groups[g]}}, nil); err != nil {
				t.Fatal(err)
			}
		}
		return iv.ID
	}
	mainInv := mkInv("main", map[string]string{"web1.example.test": "web", "web2.example.test": "web", "db1.example.test": "db"})
	other := mkInv("other", map[string]string{"app1.example.test": "app"})

	az := &fakeAuthz{allowed: map[string]bool{"web1.example.test": true, "web2.example.test": true, "db1.example.test": true, "app1.example.test": true}}
	rs := runs.New(runs.Options{DB: pool, Graphs: inv, Authz: az, Sealer: testProvider(t)})
	mkTpl := func(name string, invID uuid.UUID, limit, check bool) uuid.UUID {
		tpl, err := rs.CreateTemplate(ctx, ws.ID, runs.TemplateInput{Name: &name, ProjectID: &prj.ID, InventoryID: &invID,
			Playbook: strp("fix.yml"), CreatedBy: user.ID, AllowLimitOverride: &limit, AllowCheckOverride: &check})
		if err != nil {
			t.Fatal(err)
		}
		return tpl.ID
	}
	e := &env{runs: rs, q: q, az: az, ws: ws.ID, user: user, tplID: mkTpl("fix", mainInv, true, true), otherTp: mkTpl("rigid", other, false, false)}
	e.fleet = fleet.NewStore(pool, nil)
	e.svc = remediation.New(remediation.Options{DB: pool, Fleet: e.fleet, Runs: rs, Graphs: inv})
	e.actor = runs.Actor{UserID: user.ID, Principal: &auth.Principal{User: user}}
	e.conn, err = q.CreateConnector(ctx, dbgen.CreateConnectorParams{WorkspaceID: ws.ID, Type: "checkmk", Name: "mon", Config: []byte(`{}`), IntervalSeconds: 300, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Fleet: four merged hosts in two networks; web1/web2/db1/app1 are inventory-linked by FQDN.
	now := time.Now()
	for key, f := range map[string]connectors.Fields{
		"web1": {Hostname: "web1", FQDN: "web1.example.test", IPs: []string{"192.0.2.10"}},
		"web2": {Hostname: "web2", FQDN: "web2.example.test", IPs: []string{"192.0.2.11"}},
		"db1":  {Hostname: "db1", FQDN: "db1.example.test", IPs: []string{"198.51.100.7"}},
		"app1": {Hostname: "app1", FQDN: "app1.example.test", IPs: []string{"192.0.2.20"}},
		"free": {Hostname: "free", IPs: []string{"192.0.2.30"}},
	} {
		fb, _ := json.Marshal(f)
		if err := q.UpsertObservation(ctx, dbgen.UpsertObservationParams{WorkspaceID: ws.ID, ConnectorID: e.conn.ID, ExternalKey: key,
			Kind: connectors.KindStatic, Fields: fb, FirstSeen: pgtype.Timestamptz{Time: now, Valid: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.fleet.Merge(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	return e
}

func strp(s string) *string { return &s }

func testProvider(t *testing.T) secrets.Provider {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*5 + 3)
	}
	p, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) alert(t *testing.T, key, host, service, sev string) uuid.UUID {
	t.Helper()
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	if err := e.q.UpsertAlert(context.Background(), dbgen.UpsertAlertParams{WorkspaceID: e.ws, ConnectorID: e.conn.ID, ExternalKey: key,
		HostName: host, Service: service, Severity: sev, Summary: key + " is failing", Labels: []byte(`{}`), StartedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := e.fleet.LinkAlerts(context.Background(), e.ws); err != nil {
		t.Fatal(err)
	}
	list, err := e.fleet.Alerts(context.Background(), e.ws, fleet.AlertFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.ExternalKey == key {
			return a.ID
		}
	}
	t.Fatalf("alert %s not stored", key)
	return uuid.Nil
}

func (e *env) rule(t *testing.T, mutate func(*remediation.Input)) remediation.Rule {
	t.Helper()
	in := remediation.Input{Name: "restart-" + uuid.NewString()[:6], ServiceRegex: "^Service ", MinSeverity: "warning", TemplateID: e.tplID, Target: remediation.TargetHost}
	if mutate != nil {
		mutate(&in)
	}
	r, err := e.svc.Create(context.Background(), e.ws, in, e.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRuleCRUDAndValidation(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	r := e.rule(t, func(in *remediation.Input) {
		in.Name = "disk"
		in.ConnectorType = strp("checkmk")
		in.CheckFirst = true
	})
	if r.Template != "fix" || !r.Enabled || r.ConnectorType == nil || *r.ConnectorType != "checkmk" || r.Group != "" {
		t.Fatalf("created rule: %+v", r)
	}
	disabled := false
	got, err := e.svc.Update(ctx, e.ws, r.ID, remediation.Input{Name: "disk2", Enabled: &disabled, MinSeverity: "critical", TemplateID: e.tplID,
		Target: remediation.TargetGroup, Group: "web"}, e.user.ID)
	if err != nil || got.Name != "disk2" || got.Enabled || got.ConnectorType != nil || got.Group != "web" || got.MinSeverity != "critical" {
		t.Fatalf("update: %+v %v", got, err)
	}
	list, err := e.svc.List(ctx, e.ws)
	if err != nil || len(list) != 1 || list[0].Template != "fix" {
		t.Fatalf("list: %+v %v", list, err)
	}

	bad := func(name string, mutate func(*remediation.Input), want error) {
		t.Helper()
		in := remediation.Input{Name: "x" + name, MinSeverity: "info", TemplateID: e.tplID, Target: remediation.TargetHost}
		mutate(&in)
		if _, err := e.svc.Create(ctx, e.ws, in, e.user.ID); !errors.Is(err, want) {
			t.Errorf("%s: err %v, want %v", name, err, want)
		}
	}
	bad("dup", func(in *remediation.Input) { in.Name = "disk2" }, remediation.ErrConflict)
	bad("regex", func(in *remediation.Input) { in.ServiceRegex = "(" }, remediation.ErrInvalid)
	bad("severity", func(in *remediation.Input) { in.MinSeverity = "fatal" }, remediation.ErrInvalid)
	bad("target", func(in *remediation.Input) { in.Target = "galaxy" }, remediation.ErrInvalid)
	bad("group missing", func(in *remediation.Input) { in.Target = remediation.TargetGroup }, remediation.ErrInvalid)
	bad("group on host", func(in *remediation.Input) { in.Group = "web" }, remediation.ErrInvalid)
	bad("group pattern", func(in *remediation.Input) { in.Target = remediation.TargetGroup; in.Group = "web*" }, remediation.ErrInvalid)
	bad("template of nowhere", func(in *remediation.Input) { in.TemplateID = uuid.New() }, remediation.ErrInvalid)
	bad("template without limit override", func(in *remediation.Input) { in.TemplateID = e.otherTp }, remediation.ErrInvalid)

	if err := e.svc.Delete(ctx, e.ws, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, e.ws, r.ID); !errors.Is(err, remediation.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := e.svc.Delete(ctx, e.ws, r.ID); !errors.Is(err, remediation.ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func hostsOf(r remediation.Result) []string { return r.Run.Params.Hosts }

func TestRemediateTargetsCheckFirstAndProvenance(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a := e.alert(t, "svc-web1", "web1.example.test", "Service nginx", "critical")

	// target host
	host := e.rule(t, nil)
	res, err := e.svc.Remediate(ctx, e.ws, a, host.ID, false, e.actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Phase != remediation.PhaseApply || res.Run.Params.Limit != "web1.example.test" || res.Run.Params.Check || res.Run.Status != runs.StatusQueued ||
		!slices.Equal(hostsOf(res), []string{"web1.example.test"}) {
		t.Fatalf("host fix: %+v", res)
	}
	var prov map[string]any
	if err := json.Unmarshal(res.Run.Remediation, &prov); err != nil || prov["rule_id"] != host.ID.String() || prov["alert_id"] != a.String() || prov["phase"] != "apply" {
		t.Fatalf("provenance: %s %v", res.Run.Remediation, err)
	}
	stored, err := e.runs.GetRun(ctx, e.ws, res.Run.ID)
	if err != nil || len(stored.Remediation) == 0 {
		t.Fatalf("stored provenance: %v %s", err, stored.Remediation)
	}

	// target group, with a check run first; the real run follows on request
	grp := e.rule(t, func(in *remediation.Input) {
		in.Target = remediation.TargetGroup
		in.Group = "web"
		in.CheckFirst = true
	})
	res, err = e.svc.Remediate(ctx, e.ws, a, grp.ID, false, e.actor, nil)
	if err != nil || res.Phase != remediation.PhaseCheck || !res.Run.Params.Check || res.Run.Params.Limit != "web" ||
		!slices.Equal(hostsOf(res), []string{"web1.example.test", "web2.example.test"}) {
		t.Fatalf("group check: %+v %v", res, err)
	}
	res, err = e.svc.Remediate(ctx, e.ws, a, grp.ID, true, e.actor, nil)
	if err != nil || res.Phase != remediation.PhaseApply || res.Run.Params.Check {
		t.Fatalf("group apply: %+v %v", res, err)
	}

	// target network: every linked host of the template's inventory in 192.0.2.0/24 would need the network
	// to be known; without one the fix is refused.
	net := e.rule(t, func(in *remediation.Input) { in.Target = remediation.TargetNetwork })
	if _, err := e.svc.Remediate(ctx, e.ws, a, net.ID, false, e.actor, nil); !errors.Is(err, remediation.ErrInvalid) {
		t.Errorf("network without known networks: %v", err)
	}
	if err := e.q.UpsertNetwork(ctx, dbgen.UpsertNetworkParams{WorkspaceID: e.ws, ConnectorID: e.conn.ID, ExternalKey: "lan",
		Cidr: netip24("192.0.2.0/24"), FirstSeen: pgtype.Timestamptz{Time: time.Now(), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Remediate(ctx, e.ws, a, net.ID, false, e.actor, nil)
	if err != nil || !slices.Equal(hostsOf(res), []string{"web1.example.test", "web2.example.test"}) {
		t.Fatalf("network fix: %+v %v", res, err)
	}
}

func TestRemediateRefusals(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rule := e.rule(t, nil)
	crit := e.alert(t, "svc-web1", "web1.example.test", "Service nginx", "critical")
	info := e.alert(t, "svc-info", "web1.example.test", "Service cron", "info")
	other := e.alert(t, "mem", "web1.example.test", "Memory", "critical")
	orphan := e.alert(t, "svc-ghost", "ghost.example.test", "Service nginx", "critical")
	unmanaged := e.alert(t, "svc-free", "free", "Service nginx", "critical")

	for name, tt := range map[string]struct {
		alert uuid.UUID
		want  error
	}{"severity below minimum": {info, remediation.ErrNotApplicable}, "service regex": {other, remediation.ErrNotApplicable},
		"unknown alert": {uuid.New(), remediation.ErrAlertNotFound}, "unlinked alert": {orphan, remediation.ErrInvalid},
		"host without inventory link": {unmanaged, remediation.ErrInvalid}} {
		if _, err := e.svc.Remediate(ctx, e.ws, tt.alert, rule.ID, false, e.actor, nil); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", name, err, tt.want)
		}
	}
	// A visibility filter hides alerts of other hosts.
	none := func([]string) bool { return false }
	if _, err := e.svc.Remediate(ctx, e.ws, crit, rule.ID, false, e.actor, none); !errors.Is(err, remediation.ErrAlertNotFound) {
		t.Errorf("hidden alert: %v", err)
	}
	// A disabled rule cannot be used.
	off := false
	if _, err := e.svc.Update(ctx, e.ws, rule.ID, remediation.Input{Name: rule.Name, Enabled: &off, MinSeverity: "info", TemplateID: e.tplID, Target: remediation.TargetHost}, e.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Remediate(ctx, e.ws, crit, rule.ID, false, e.actor, nil); !errors.Is(err, remediation.ErrDisabled) {
		t.Errorf("disabled rule: %v", err)
	}
	// Nothing was queued by any refusal.
	if n := countRuns(t, e); n != 0 {
		t.Errorf("%d runs queued by refused fixes", n)
	}
}

func TestRemediateScopeDenial(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	delete(e.az.allowed, "web2.example.test") // the caller's scope covers web1 but not web2
	a := e.alert(t, "svc-web1", "web1.example.test", "Service nginx", "critical")

	if _, err := e.svc.Remediate(ctx, e.ws, a, e.rule(t, nil).ID, false, e.actor, nil); err != nil {
		t.Fatalf("host target inside the scope: %v", err)
	}
	grp := e.rule(t, func(in *remediation.Input) { in.Target = remediation.TargetGroup; in.Group = "web" })
	before := countRuns(t, e)
	if _, err := e.svc.Remediate(ctx, e.ws, a, grp.ID, false, e.actor, nil); !errors.Is(err, policy.ErrForbidden) {
		t.Fatalf("group target reaching web2: %v", err)
	}
	if e.az.denied == 0 || countRuns(t, e) != before {
		t.Errorf("denied %d, runs %d -> %d", e.az.denied, before, countRuns(t, e))
	}
}

func countRuns(t *testing.T, e *env) int {
	t.Helper()
	page, err := e.runs.ListRuns(context.Background(), e.ws, runs.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Items)
}

func TestApplicable(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	on := e.rule(t, nil)
	off := false
	e.rule(t, func(in *remediation.Input) { in.Enabled = &off })
	e.rule(t, func(in *remediation.Input) { in.ConnectorType = strp("alert_webhook") })
	hit := e.alert(t, "svc-web1", "web1.example.test", "Service nginx", "critical")
	miss := e.alert(t, "mem", "web1.example.test", "Memory", "critical")
	orphan := e.alert(t, "svc-ghost", "ghost.example.test", "Service nginx", "critical")
	alerts, err := e.fleet.Alerts(ctx, e.ws, fleet.AlertFilter{State: fleet.AlertOpen}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.Applicable(ctx, e.ws, alerts)
	if err != nil {
		t.Fatal(err)
	}
	if len(m[hit]) != 1 || m[hit][0].ID != on.ID || len(m[miss]) != 0 || len(m[orphan]) != 0 {
		t.Errorf("applicable: %+v", m)
	}
	pv, err := e.svc.Preview(ctx, e.ws, remediation.Criteria{ServiceRegex: "nginx"})
	if err != nil || len(pv) != 2 {
		t.Errorf("preview: %d %v", len(pv), err)
	}
	if _, err := e.svc.Preview(ctx, e.ws, remediation.Criteria{ServiceRegex: "("}); !errors.Is(err, remediation.ErrInvalid) {
		t.Errorf("preview with bad regex: %v", err)
	}
}

func TestBulkRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	hosts, err := e.fleet.Hosts(ctx, e.ws, fleet.HostFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := map[string]uuid.UUID{}
	for _, h := range hosts {
		id[h.Name] = h.ID
	}

	res, err := e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["web1"], id["db1"], id["free"], id["web1"], uuid.New()}, TemplateID: e.tplID, CheckMode: true}, e.actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Hosts, []string{"db1.example.test", "web1.example.test"}) || !res.Run.Params.Check ||
		res.Run.Params.Limit != "db1.example.test,web1.example.test" || len(res.Skipped) != 2 {
		t.Fatalf("bulk: %+v", res)
	}
	for _, s := range res.Skipped {
		if s.Reason == "" {
			t.Errorf("skipped without reason: %+v", s)
		}
	}
	var prov map[string]any
	if err := json.Unmarshal(res.Run.Remediation, &prov); err != nil || prov["source"] != "bulk" {
		t.Errorf("provenance: %s", res.Run.Remediation)
	}

	// A host that lives only in another inventory fails the whole request with the list.
	_, err = e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["web1"], id["app1"]}, TemplateID: e.tplID}, e.actor, nil)
	var nie *remediation.NotInInventoryError
	if !errors.As(err, &nie) || !errors.Is(err, remediation.ErrInvalid) || !slices.Equal(nie.Hosts, []string{"app1"}) {
		t.Fatalf("other inventory: %v", err)
	}
	// Only unmanaged hosts: nothing to run.
	if _, err := e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["free"]}, TemplateID: e.tplID}, e.actor, nil); !errors.Is(err, remediation.ErrInvalid) {
		t.Errorf("only unmanaged: %v", err)
	}
	// Limits the template forbids, empty and oversized selections.
	if _, err := e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["app1"]}, TemplateID: e.otherTp}, e.actor, nil); !errors.Is(err, remediation.ErrInvalid) {
		t.Errorf("template without limit override: %v", err)
	}
	if _, err := e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{TemplateID: e.tplID}, e.actor, nil); !errors.Is(err, remediation.ErrInvalid) {
		t.Errorf("empty selection: %v", err)
	}
	// Visibility: hosts outside the caller's fleet scope count as not found.
	vis := func(ips []string) bool { return slices.Contains(ips, "192.0.2.10") }
	res, err = e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["web1"], id["db1"]}, TemplateID: e.tplID}, e.actor, vis)
	if err != nil || !slices.Equal(res.Hosts, []string{"web1.example.test"}) || len(res.Skipped) != 1 {
		t.Errorf("visibility: %+v %v", res, err)
	}
	// Scope denial on the effective host set queues nothing.
	delete(e.az.allowed, "db1.example.test")
	before := countRuns(t, e)
	if _, err := e.svc.BulkRun(ctx, e.ws, remediation.BulkInput{HostIDs: []uuid.UUID{id["web1"], id["db1"]}, TemplateID: e.tplID}, e.actor, nil); !errors.Is(err, policy.ErrForbidden) {
		t.Errorf("scope denial: %v", err)
	}
	if countRuns(t, e) != before {
		t.Error("a denied bulk run was queued")
	}
}

func netip24(s string) netip.Prefix { return netip.MustParsePrefix(s) }
