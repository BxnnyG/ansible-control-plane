package scan

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/vault"
)

// buildProject copies the fixture to a temp dir and adds the pieces that
// must not be committed: vault files, .git content, symlinks.
func buildProject(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "project")
	outside = filepath.Join(base, "outside")
	src := "testdata/project"
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o750)
		}
		data, err := os.ReadFile(p) //nolint:gosec // test fixture path
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600) //nolint:gosec // test fixture path
	})
	if err != nil {
		t.Fatal(err)
	}
	write := func(base, rel string, data []byte) {
		t.Helper()
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	v12, err := vault.Encrypt([]byte("db_password: SECRET-MARKER\n"), "pw", "prod")
	if err != nil {
		t.Fatal(err)
	}
	v11, err := vault.Encrypt([]byte("x: SECRET-MARKER\n"), "pw", "")
	if err != nil {
		t.Fatal(err)
	}
	write(root, "inventories/prod/group_vars/vault.yml", v12)
	write(root, "secrets.yml", v11)
	write(root, ".git/config", []byte("- hosts: all\n"))
	write(outside, "evil.yml", []byte("- hosts: all\n"))
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "evil.yml"), filepath.Join(root, "evil-link.yml")); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func scanFixture(t *testing.T) *Result {
	t.Helper()
	root, _ := buildProject(t)
	res, err := Scan(context.Background(), root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestScanPlaybooks(t *testing.T) {
	res := scanFixture(t)
	want := []Playbook{
		{Path: "playbooks/db.yml", Plays: 1, Hosts: []string{"db"}, Roles: []string{}, Imports: []string{}},
		{Path: "site.yml", Name: "Site", Plays: 2, Hosts: []string{"webservers"}, Roles: []string{"base", "webapp"}, Imports: []string{"playbooks/db.yml"}},
	}
	if !reflect.DeepEqual(res.Playbooks, want) {
		t.Errorf("playbooks = %+v", res.Playbooks)
	}
}

func TestScanRolesAndSurveys(t *testing.T) {
	res := scanFixture(t)
	if len(res.Roles) != 2 {
		t.Fatalf("roles = %d", len(res.Roles))
	}
	base, web := res.Roles[0], res.Roles[1]
	if base.Name != "base" || len(base.EntryPoints) != 0 {
		t.Errorf("base = %+v", base)
	}
	if web.Path != "roles/webapp" || !reflect.DeepEqual(web.EntryPoints, []string{"extra", "main", "upgrade"}) {
		t.Fatalf("webapp = %+v", web.EntryPoints)
	}
	main := web.Surveys[1]
	if main.Title != "Deploy the web app" || main.Description != "Installs and configures the app. Second line." {
		t.Errorf("main header = %q / %q", main.Title, main.Description)
	}
	byName := map[string]Field{}
	for _, f := range main.Fields {
		byName[f.Name] = f
	}
	tests := []struct {
		name string
		want Field
	}{
		{"app_port", Field{Name: "app_port", Type: TypeInt, Default: 8080, Description: "Listen port"}},
		{"app_env", Field{Name: "app_env", Type: TypeStr, Required: true, Choices: []any{"dev", "prod"}}},
		{"app_ratio", Field{Name: "app_ratio", Type: TypeFloat}},
		{"app_debug", Field{Name: "app_debug", Type: TypeBool, Default: false}},
		{"app_conf", Field{Name: "app_conf", Type: TypePath}},
		{"app_token", Field{Name: "app_token", Type: TypeStr, Secret: true}},
		{"app_hosts", Field{Name: "app_hosts", Type: TypeList, Elements: TypeStr}},
		{"app_extra", Field{Name: "app_extra", Type: TypeRaw}},
	}
	for _, tt := range tests {
		if got := byName[tt.name]; !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s = %+v, want %+v", tt.name, got, tt.want)
		}
	}
	db := byName["app_db"]
	if db.Type != TypeDict || len(db.Fields) != 2 || !db.Fields[0].Required || db.Fields[1].Default != 5432 {
		t.Errorf("app_db = %+v", db)
	}
	users := byName["app_users"]
	if users.Elements != TypeDict || len(users.Fields) != 1 || users.Fields[0].Name != "name" {
		t.Errorf("app_users = %+v", users)
	}
	// argument_specs.yml wins over meta/main.yml for the same entry point;
	// entry points only in meta/main.yml are still found.
	if len(web.Surveys[0].Fields) != 1 || web.Surveys[0].EntryPoint != "extra" {
		t.Errorf("extra = %+v", web.Surveys[0])
	}
}

func TestSurveySchema(t *testing.T) {
	res := scanFixture(t)
	schema := res.Roles[1].Surveys[1].Schema()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Error("default of no_log option leaked into schema")
	}
	props := schema["properties"].(map[string]any)
	tests := []struct {
		field, key string
		want       any
	}{
		{"app_port", "type", "integer"},
		{"app_ratio", "type", "number"},
		{"app_debug", "type", "boolean"},
		{"app_conf", "format", "path"},
		{"app_token", "writeOnly", true},
		{"app_hosts", "type", "array"},
		{"app_db", "type", "object"},
	}
	for _, tt := range tests {
		if got := props[tt.field].(map[string]any)[tt.key]; got != tt.want {
			t.Errorf("%s.%s = %v, want %v", tt.field, tt.key, got, tt.want)
		}
	}
	if !reflect.DeepEqual(schema["required"], []string{"app_env"}) {
		t.Errorf("required = %v", schema["required"])
	}
	items := props["app_users"].(map[string]any)["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("app_users items = %v", items)
	}
	if _, ok := props["app_extra"].(map[string]any)["type"]; ok {
		t.Error("raw field must not carry a JSON type")
	}
}

func TestScanRequirements(t *testing.T) {
	res := scanFixture(t)
	var got []string
	for _, r := range res.Requirements {
		got = append(got, strings.Join([]string{r.File, r.Kind, r.Name, r.Version, r.Src}, "|"))
	}
	want := []string{
		"collections/requirements.yml|collection|ansible.utils||",
		"requirements.yml|collection|ansible.posix||",
		"requirements.yml|collection|community.general|>=8.0.0|",
		"requirements.yml|role|geerlingguy.nginx|3.2.0|",
		"requirements.yml|role|git+https://git.example.test/org/role.git|main|git+https://git.example.test/org/role.git",
		"roles/requirements.yml|role|acme.thing|1.0.0|acme.thing",
		"roles/requirements.yml|role|justname||",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requirements:\n got %q\nwant %q", got, want)
	}
}

func TestScanAnsibleCfg(t *testing.T) {
	res := scanFixture(t)
	if res.AnsibleCfg == nil {
		t.Fatal("no ansible.cfg")
	}
	want := map[string]string{
		"defaults.inventory":          "inventories/prod, ../outside",
		"defaults.roles_path":         "roles",
		"defaults.remote_user":        "deploy",
		"privilege_escalation.become": "True",
	}
	if !reflect.DeepEqual(res.AnsibleCfg.Settings, want) {
		t.Errorf("settings = %v", res.AnsibleCfg.Settings)
	}
}

func TestScanInventoriesAndVars(t *testing.T) {
	res := scanFixture(t)
	wantInv := []Inventory{
		{Path: "inventories/prod/hosts", Format: "ini", Source: "ansible.cfg"},
		{Path: "inventories/prod/hosts.yml", Format: "yaml", Source: "ansible.cfg"},
	}
	if !reflect.DeepEqual(res.Inventories, wantInv) {
		t.Errorf("inventories = %+v", res.Inventories)
	}
	wantVars := []VarsFile{
		{Path: "inventories/prod/group_vars/all.yml", Scope: "group", Name: "all"},
		{Path: "inventories/prod/group_vars/vault.yml", Scope: "group", Name: "vault", Vaulted: true},
		{Path: "inventories/prod/host_vars/web-01.example.test.yml", Scope: "host", Name: "web-01.example.test"},
	}
	if !reflect.DeepEqual(res.VarsFiles, wantVars) {
		t.Errorf("vars = %+v", res.VarsFiles)
	}
}

func TestScanVault(t *testing.T) {
	res := scanFixture(t)
	wantFiles := []VaultFile{
		{Path: "inventories/prod/group_vars/vault.yml", Version: "1.2", VaultID: "prod"},
		{Path: "secrets.yml", Version: "1.1"},
	}
	if !reflect.DeepEqual(res.VaultFiles, wantFiles) {
		t.Errorf("vault files = %+v", res.VaultFiles)
	}
	wantInline := []InlineVault{
		{File: "inventories/prod/host_vars/web-01.example.test.yml", Paths: []string{"api_key", "nested.list[0]"}},
	}
	if !reflect.DeepEqual(res.InlineVault, wantInline) {
		t.Errorf("inline = %+v", res.InlineVault)
	}
}

func TestScanSafetyAndWarnings(t *testing.T) {
	res := scanFixture(t)
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"SECRET-MARKER", "tok3n", "evil.yml\"", "/secret/pw", "private_key_file"} {
		if strings.Contains(string(data), bad) {
			t.Errorf("result contains %q", bad)
		}
	}
	for _, pb := range res.Playbooks {
		if strings.HasPrefix(pb.Path, ".git") || strings.HasPrefix(pb.Path, "linked") || pb.Path == "evil-link.yml" || strings.HasPrefix(pb.Path, "templates") {
			t.Errorf("unexpected playbook %s", pb.Path)
		}
	}
	var broken, link bool
	for _, w := range res.Warnings {
		if w.Path == "playbooks/broken.yml" && strings.HasPrefix(w.Message, "yaml parse error") {
			broken = true
		}
		if w.Path == "evil-link.yml" || w.Path == "linked" {
			link = true
		}
	}
	if !broken || !link {
		t.Errorf("warnings = %+v", res.Warnings)
	}
}

func TestScanDeterministicJSON(t *testing.T) {
	root, _ := buildProject(t)
	var prev []byte
	for i := 0; i < 3; i++ {
		res, err := Scan(context.Background(), root, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(res)
		if prev != nil && string(prev) != string(data) {
			t.Fatal("scan output not stable")
		}
		prev = data
	}
	if strings.Contains(string(prev), ":null") {
		t.Errorf("null in output: %s", prev)
	}
}

func TestScanLimits(t *testing.T) {
	root, _ := buildProject(t)
	big := append([]byte("- hosts: all\n  vars:\n    x: \""), []byte(strings.Repeat("a", 4096))...)
	if err := os.WriteFile(filepath.Join(root, "big.yml"), append(big, []byte("\"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		lim      Limits
		wantWarn string
		wantPath string
	}{
		{"file size", Limits{MaxFileSize: 2048}, "file too large, skipped", "big.yml"},
		{"file count", Limits{MaxFiles: 3}, "file limit of 3 reached, scan truncated", "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Scan(context.Background(), root, tt.lim)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, w := range res.Warnings {
				if w.Path == tt.wantPath && w.Message == tt.wantWarn {
					found = true
				}
			}
			if !found {
				t.Errorf("warnings = %+v", res.Warnings)
			}
		})
	}
}

func TestScanErrors(t *testing.T) {
	if _, err := Scan(context.Background(), filepath.Join(t.TempDir(), "missing"), Limits{}); err == nil {
		t.Error("missing root: want error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root, _ := buildProject(t)
	if _, err := Scan(ctx, root, Limits{}); err == nil {
		t.Error("cancelled context: want error")
	}
}

func TestScanVarsFileNames(t *testing.T) {
	root := t.TempDir()
	for rel, data := range map[string]string{
		"host_vars/web.example.test":       "a: 1\n",
		"host_vars/db1":                    "a: 1\n",
		"host_vars/db2.example.test/x.yml": "a: 1\n",
		"host_vars/notes.md":               "text\n",
		"group_vars/all/readme.txt":        "text\n",
		"group_vars/web.yaml":              "a: 1\n",
	} {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Scan(context.Background(), root, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []VarsFile{
		{Path: "group_vars/web.yaml", Scope: "group", Name: "web"},
		{Path: "host_vars/db1", Scope: "host", Name: "db1"},
		{Path: "host_vars/db2.example.test/x.yml", Scope: "host", Name: "db2.example.test"},
		{Path: "host_vars/web.example.test", Scope: "host", Name: "web.example.test"},
	}
	if !reflect.DeepEqual(res.VarsFiles, want) {
		t.Errorf("vars = %+v", res.VarsFiles)
	}
}
