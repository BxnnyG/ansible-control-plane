package semaphore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/schedules"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const (
	testPassword = "pw-do-not-leak-0123"
	scanJSON     = `{"playbooks":[{"path":"site.yml","hosts":["all"]}],"roles":[]}`
)

type applyEnv struct {
	ap      *Applier
	q       *dbgen.Queries
	ws      dbgen.Workspace
	project dbgen.Project
	snap    *Snapshot
}

func applySetup(t *testing.T) *applyEnv {
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
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "w" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""), Name: "W"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "ansible", Url: "https://git.example.test/o/r.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{
		ProjectID: prj.ID, Sha: "0123456789abcdef0123456789abcdef01234567", Scan: []byte(scanJSON), Warnings: []byte("[]"),
	}); err != nil {
		t.Fatal(err)
	}
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i*5 + 3)
	}
	sp, err := secrets.NewLocalProvider(mk)
	if err != nil {
		t.Fatal(err)
	}
	inv := inventory.New(inventory.Options{DB: pool})
	rs := runs.New(runs.Options{DB: pool, Graphs: inv, Sealer: sp})
	sshKey := genKey(t, "")
	col := func(login, pass, key string) string {
		b := payload(login, pass, key)
		if key == "" {
			b, _ = json.Marshal(map[string]string{"login": login, "password": pass})
		}
		return string(b)
	}
	snap := &Snapshot{
		Keys: []Key{
			{ID: 1, Name: " deploy ", Type: "ssh", Secret: col("ops", "", sshKey)},
			{ID: 2, Name: "become", Type: "login_password", Secret: col("root", testPassword, "")},
			{ID: 3, Name: "vault", Type: "login_password", Secret: col("", testPassword, "")},
		},
		Repositories: []Repository{{ID: 1, Name: "ansible", GitURL: "https://git.example.test/o/r"}},
		Inventories: []Inventory{
			{ID: 1, Name: "Lab Hosts ", Type: "static", Inventory: "[web]\nweb1.example.test ansible_host=192.0.2.10\n", SSHKeyID: p(1), BecomeKeyID: p(2)},
			{ID: 2, Name: "Other", Type: "static", Inventory: "db1.example.test\n"},
		},
		Environments: []Environment{{ID: 1, Name: "env", JSON: `{"foo":"bar"}`, Env: `{"A":"b"}`}},
		Templates: []Template{
			{ID: 1, Name: " site ", Playbook: "site.yml", InventoryID: p(1), RepositoryID: p(1), EnvironmentID: p(1), App: "ansible", Arguments: `["--limit","web1.example.test","--check"]`},
			{ID: 2, Name: "mapped", Playbook: "site.yml", InventoryID: p(2), RepositoryID: p(1), App: "ansible"},
		},
		TemplateVault: []TemplateVault{{TemplateID: 1, VaultKeyID: p(3), Name: "default"}},
		Schedules:     []Schedule{{ID: 1, TemplateID: 1, Name: "nightly", CronFormat: "0 3 * * *", Active: true}},
	}
	return &applyEnv{
		q: q, ws: ws, project: prj, snap: snap,
		ap: &Applier{
			Q: q, Secrets: secrets.NewService(q, sp), Inventories: inv, Runs: rs,
			Schedules: schedules.New(schedules.Options{DB: pool, Runs: rs}),
			Workspace: ws.ID, Slug: ws.Slug, EncKey: NewSecret(nil),
		},
	}
}

// plan builds a fresh report from the current mappings in the database.
func (e *applyEnv) plan(t *testing.T, opt Options, existing ...ACPInventory) *Report {
	t.Helper()
	maps, err := e.q.ListMigrationMappings(context.Background(), dbgen.ListMigrationMappingsParams{Source: MappingSource, WorkspaceID: e.ws.ID})
	if err != nil {
		t.Fatal(err)
	}
	tgt := Target{
		Workspace: e.ws.Slug, Mappings: map[string]string{}, Inventories: existing,
		Projects: []ACPProject{{ID: e.project.ID.String(), Slug: "ansible", URL: e.project.Url, DefaultRef: "main"}},
	}
	for _, m := range maps {
		tgt.Mappings[m.Kind+":"+m.SourceID] = m.AcpID.String()
	}
	e.ap.Mappings = tgt.Mappings
	snap := *e.snap
	return Map(&snap, DescribeKeys(snap.Keys, e.ap.EncKey), tgt, opt)
}

func TestApplyIdempotentAndCompare(t *testing.T) {
	e := applySetup(t)
	ctx := context.Background()
	// An existing Git-style inventory that template 2 is re-pointed to.
	if _, err := e.ap.Inventories.Create(ctx, e.ws.ID, inventory.CreateInput{Name: "Prod", Mode: inventory.ModeDB}); err != nil {
		t.Fatal(err)
	}
	opt := Options{InventoryMap: map[int64]string{2: "prod"}}
	rep := e.plan(t, opt, ACPInventory{Slug: "prod", Mode: "db"})
	res, err := e.ap.Apply(ctx, e.snap, rep)
	if err != nil {
		t.Fatal(err)
	}
	// 3 secrets, 1 vault identity, 2 archives (also the mapped one), 2 templates, 1 schedule.
	want := map[string]int{ResCreated: 9}
	if res.Counts[ResCreated] != want[ResCreated] || res.Failed() {
		t.Fatalf("first apply: %+v items=%+v", res.Counts, res.Items)
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), testPassword) || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("apply report leaks secret material")
	}
	scheds, _ := e.q.ListSchedules(ctx, e.ws.ID)
	if len(scheds) != 1 || scheds[0].Enabled {
		t.Fatalf("schedules must be created disabled: %+v", scheds)
	}
	tpls, _ := e.ap.Runs.ListTemplates(ctx, e.ws.ID)
	if len(tpls) != 2 {
		t.Fatalf("templates: %d", len(tpls))
	}
	for _, tp := range tpls {
		if tp.Name == "site" && (tp.SSHSecretID == nil || tp.BecomeSecretID == nil || len(tp.VaultIdentityIDs) != 1 || tp.ExtraVars["ansible_user"] != "ops" || tp.DefaultLimit != "web1.example.test" || !tp.CheckMode) {
			t.Fatalf("template site: %+v", tp)
		}
	}
	invs, _ := e.q.ListInventories(ctx, e.ws.ID)
	if len(invs) != 3 {
		t.Fatalf("inventories (prod + 2 archives): %d", len(invs))
	}
	maps, _ := e.q.ListMigrationMappings(ctx, dbgen.ListMigrationMappingsParams{Source: MappingSource, WorkspaceID: e.ws.ID})
	if len(maps) != 9 {
		t.Fatalf("mappings: %d", len(maps))
	}

	// Re-run: nothing new, everything reported as existing.
	rep = e.plan(t, opt, ACPInventory{Slug: "prod", Mode: "db"})
	res, err = e.ap.Apply(ctx, e.snap, rep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts[ResCreated] != 0 || res.Counts[ResExists] != 9 || res.Failed() {
		t.Fatalf("second apply: %+v items=%+v", res.Counts, res.Items)
	}

	// Compare: counts match; a check run is queued per template (the one on the empty Git-style inventory is rejected and listed).
	rep = e.plan(t, opt, ACPInventory{Slug: "prod", Mode: "db"})
	cmp, err := e.ap.Compare(ctx, e.snap, rep, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Match || len(cmp.CheckRuns) != 2 || cmp.CheckRuns[0].RunID == "" || cmp.CheckRuns[1].Error == "" {
		t.Fatalf("compare: %+v", cmp)
	}

	// Deleting an imported template makes the comparison fail and lists it.
	for _, tp := range tpls {
		if tp.Name == "mapped" {
			if _, err := e.ap.Runs.DeleteTemplate(ctx, e.ws.ID, tp.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	cmp, err = e.ap.Compare(ctx, e.snap, rep, false)
	if err != nil {
		t.Fatal(err)
	}
	var row CompareRow
	for _, r := range cmp.Rows {
		if r.Kind == KindTemplate {
			row = r
		}
	}
	if cmp.Match || row.Source != 2 || row.Present != 1 || len(row.Missing) != 1 || !strings.Contains(row.Missing[0], "mapped") {
		t.Fatalf("compare after delete: %+v", cmp)
	}
}

func TestApplyConflictSkipsDependants(t *testing.T) {
	e := applySetup(t)
	ctx := context.Background()
	// A secret with the target name exists and is not mapped.
	if _, err := e.ap.Secrets.Create(ctx, e.ws.ID, secrets.CreateInput{Name: "semaphore: become", Type: "password", Value: []byte("other")}); err != nil {
		t.Fatal(err)
	}
	rep := e.plan(t, Options{})
	res, err := e.ap.Apply(ctx, e.snap, rep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts[ResConflict] != 1 || !res.Failed() {
		t.Fatalf("expected one conflict: %+v", res.Counts)
	}
	// Template 1 needs the become secret: not created, and neither is its schedule.
	for _, it := range res.Items {
		if it.Kind == KindTemplate && it.SourceID == "1" && it.Result == ResCreated {
			t.Fatal("template with a conflicting secret must not be created")
		}
		if it.Kind == KindSchedule && it.Result == ResCreated {
			t.Fatal("schedule of a skipped template must not be created")
		}
	}
	// The foreign secret is untouched and nothing mapped to it.
	maps, _ := e.q.ListMigrationMappings(ctx, dbgen.ListMigrationMappingsParams{Source: MappingSource, WorkspaceID: e.ws.ID})
	for _, m := range maps {
		if m.Kind == KindSecret && m.SourceID == "2" {
			t.Fatal("conflicting secret must not be mapped")
		}
	}
	cmp, err := e.ap.Compare(ctx, e.snap, rep, false)
	if err != nil || cmp.Match {
		t.Fatalf("compare must mismatch: %+v %v", cmp, err)
	}
}
