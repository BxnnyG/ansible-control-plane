package semaphore

import (
	"reflect"
	"strings"
	"testing"
)

func p(i int64) *int64 { return &i }

func fixture() (*Snapshot, map[int64]KeyInfo, Target) {
	s := &Snapshot{
		Projects: []Project{{ID: 1, Name: "Infra"}},
		Keys: []Key{
			{ID: 1, Name: "deploy", Type: "ssh"},
			{ID: 2, Name: "become", Type: "login_password"},
			{ID: 3, Name: "nothing", Type: "none"},
			{ID: 4, Name: "broken", Type: "ssh"},
			{ID: 5, Name: "vault", Type: "login_password"},
		},
		Repositories: []Repository{
			{ID: 1, Name: "ansible", GitURL: "git@git.example.org:ops/ansible.git", GitBranch: "main"},
			{ID: 2, Name: "local", GitURL: "/srv/playbooks"},
		},
		Inventories: []Inventory{
			{ID: 1, Name: "Prod Hosts", Type: "static", Inventory: "[web]\nweb1 ansible_host=192.0.2.10\nweb2\n", SSHKeyID: p(1), BecomeKeyID: p(2)},
			{ID: 2, Name: "from file", Type: "file", Inventory: "inventories/prod"},
			{ID: 3, Name: "broken key", Type: "static", Inventory: "h1\n", SSHKeyID: p(4)},
		},
		Environments: []Environment{{ID: 1, Name: "env", JSON: `{"foo":"bar"}`, Env: `{"A":"b"}`, SecretValues: 1}},
		Templates: []Template{
			{ID: 1, Name: "site", Playbook: "site.yml", InventoryID: p(1), RepositoryID: p(1), EnvironmentID: p(1), App: "ansible",
				Arguments:  `["--limit","web1","-vv","--check","--tags=a,b","-e","x=secret"]`,
				SurveyVars: `[{"name":"ver","type":"enum","values":[{"name":"a","value":"1"}]}]`},
			{ID: 2, Name: "local", Playbook: "x.yml", InventoryID: p(1), RepositoryID: p(2)},
			{ID: 3, Name: "file inv", Playbook: "x.yml", InventoryID: p(2), RepositoryID: p(1)},
			{ID: 4, Name: "tf", Playbook: "x", InventoryID: p(1), RepositoryID: p(1), App: "terraform"},
			{ID: 5, Name: "badkey", Playbook: "x.yml", InventoryID: p(3), RepositoryID: p(1)},
		},
		TemplateVault: []TemplateVault{{TemplateID: 1, VaultKeyID: p(5)}},
		Schedules: []Schedule{
			{ID: 1, TemplateID: 1, CronFormat: "0 3 * * *", Active: true},
			{ID: 2, TemplateID: 1, CronFormat: "bogus"},
			{ID: 3, TemplateID: 2, CronFormat: "0 3 * * *"},
		},
		Users:        []User{{ID: 1, Username: "alice", Admin: true}},
		ProjectUsers: []ProjectUser{{ProjectID: 1, UserID: 1, Role: "owner"}},
		TaskCount:    7,
	}
	keys := map[int64]KeyInfo{
		1: {Login: "deploy", Fingerprint: "SHA256:abc", Unprotected: true},
		2: {Login: "root"},
		4: {Err: "decryption failed"},
		5: {},
	}
	tgt := Target{
		Workspace: "default",
		Projects:  []ACPProject{{ID: "p-1", Slug: "ansible", URL: "https://git.example.org/ops/ansible", DefaultRef: "main"}},
		Inventories: []ACPInventory{
			{Slug: "prod", Mode: "git", Hosts: []string{"web1", "192.0.2.10", "db1"}},
			{Slug: "other", Hosts: []string{"zzz"}},
		},
		Mappings: map[string]string{},
	}
	return s, keys, tgt
}

func statusOf(items []Item, id string) Item {
	for _, i := range items {
		if i.SourceID == id {
			return i
		}
	}
	return Item{}
}

func TestMapFull(t *testing.T) {
	s, keys, tgt := fixture()
	r := Map(s, keys, tgt, Options{})

	if statusOf(r.Repositories, "1").Status != StatusMatched || statusOf(r.Repositories, "2").Status != StatusUnmappable {
		t.Fatalf("repos: %+v", r.Repositories)
	}
	if statusOf(r.Secrets, "1").Status != StatusNew || statusOf(r.Secrets, "3").Status != StatusSkipped || statusOf(r.Secrets, "4").Status != StatusUnmappable {
		t.Fatalf("secrets: %+v", r.Secrets)
	}
	types := map[int64]string{}
	for _, ps := range r.PlanSecrets {
		types[ps.SourceKeyID] = ps.Type
	}
	if types[1] != "ssh_key" || types[2] != "password" || types[5] != "vault_password" {
		t.Fatalf("secret types: %v", types)
	}
	if len(r.PlanInventories) != 2 || r.PlanInventories[0].Slug != "semaphore-prod-hosts" || r.PlanInventories[0].Format != "ini" {
		t.Fatalf("inventories: %+v", r.PlanInventories)
	}
	if statusOf(r.Inventories, "2").Status != StatusUnmappable {
		t.Fatalf("file inventory: %+v", r.Inventories)
	}
	if len(r.Suggestions) != 1 || r.Suggestions[0].Slug != "prod" || r.Suggestions[0].Overlap < 0.66 {
		t.Fatalf("suggestions: %+v", r.Suggestions)
	}
	if statusOf(r.Templates, "1").Status != StatusNew {
		t.Fatalf("template 1: %+v", statusOf(r.Templates, "1"))
	}
	for _, id := range []string{"2", "3", "4", "5"} {
		if statusOf(r.Templates, id).Status != StatusUnmappable {
			t.Fatalf("template %s should be unmappable: %+v", id, statusOf(r.Templates, id))
		}
	}
	pt := r.PlanTemplates[0]
	if pt.Limit != "web1" || pt.Tags != "a,b" || pt.Verbosity != 2 || !pt.Check || pt.Diff {
		t.Fatalf("args: %+v", pt)
	}
	if pt.ExtraVars["ansible_user"] != "deploy" || pt.ExtraVars["foo"] != "bar" || pt.EnvVars["A"] != "b" {
		t.Fatalf("vars: %+v", pt)
	}
	if pt.SSHKeyID == nil || *pt.SSHKeyID != 1 || pt.BecomeID == nil || *pt.BecomeID != 2 || len(pt.VaultKeys) != 1 {
		t.Fatalf("keys: %+v", pt)
	}
	if len(pt.Survey) != 1 || len(pt.Survey[0].Choices) != 1 {
		t.Fatalf("survey: %+v", pt.Survey)
	}
	notes := strings.Join(statusOf(r.Templates, "1").Notes, "|")
	if !strings.Contains(notes, "-e") || strings.Contains(notes, "secret") && strings.Contains(notes, "x=secret") {
		t.Fatalf("notes leak or miss: %s", notes)
	}
	// Schedules: only #1 maps and is planned; always disabled in target text.
	if statusOf(r.Schedules, "1").Status != StatusNew || statusOf(r.Schedules, "2").Status != StatusUnmappable || statusOf(r.Schedules, "3").Status != StatusUnmappable {
		t.Fatalf("schedules: %+v", r.Schedules)
	}
	if !strings.Contains(statusOf(r.Schedules, "1").Target, "disabled") {
		t.Fatal("schedule must be disabled")
	}
	if len(r.Users) != 1 || r.Users[0].Roles != "owner" || r.TaskCount != 7 {
		t.Fatalf("users/tasks: %+v %d", r.Users, r.TaskCount)
	}
}

func TestMapInventoryMapAndExisting(t *testing.T) {
	s, keys, tgt := fixture()
	tgt.Mappings[MappingKey(KindSecret, 1)] = "x"
	tgt.Mappings[MappingKey(KindTemplate, 1)] = "y"
	r := Map(s, keys, tgt, Options{InventoryMap: map[int64]string{1: "prod", 2: "missing"}})
	if statusOf(r.Secrets, "1").Status != StatusExists || statusOf(r.Templates, "1").Status != StatusExists {
		t.Fatalf("exists: %+v %+v", r.Secrets, r.Templates)
	}
	if statusOf(r.Inventories, "1").Status != StatusMatched || len(r.PlanInventories) != 2 || r.PlanInventories[0].Slug != "semaphore-prod-hosts" {
		t.Fatalf("every static inventory is archived, also when mapped: %+v", r.PlanInventories)
	}
	if !strings.Contains(strings.Join(statusOf(r.Inventories, "1").Notes, "|"), "archive semaphore-prod-hosts will be created") {
		t.Fatalf("dry run must show the archive: %+v", statusOf(r.Inventories, "1"))
	}
	if len(r.PlanVaults) != 1 || r.PlanVaults[0].Label != "semaphore-vault" {
		t.Fatalf("vault plan: %+v", r.PlanVaults)
	}
	if !strings.Contains(statusOf(r.Inventories, "2").Reason, "does not exist") {
		t.Fatalf("missing target: %+v", statusOf(r.Inventories, "2"))
	}
	if len(r.PlanTemplates) != 0 {
		t.Fatalf("existing template must not be planned: %+v", r.PlanTemplates)
	}
}

func TestNormalizeGitURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"git@Git.Example.org:ops/ansible.git", "git.example.org/ops/ansible", true},
		{"https://git.example.org/ops/ansible/", "git.example.org/ops/ansible", true},
		{"ssh://git@git.example.org:22/ops/ansible.git", "git.example.org/ops/ansible", true},
		{"ssh://git@git.example.org:2222/ops/ansible", "git.example.org:2222/ops/ansible", true},
		{"/srv/repo", "", false},
		{"file:///srv/repo", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := NormalizeGitURL(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%q: got %q,%v", tt.in, got, ok)
		}
	}
}

func TestParseArguments(t *testing.T) {
	tests := []struct {
		in       string
		want     ParsedArgs
		unmapped string
	}{
		{`["--limit","a","-t","b","--skip-tags=c","-vvv","-D"]`, ParsedArgs{Limit: "a", Tags: "b", SkipTags: "c", Verbosity: 3, Diff: true}, ""},
		{`--limit a --check`, ParsedArgs{Limit: "a", Check: true}, ""},
		{`["--forks","10","--become"]`, ParsedArgs{}, "--become,--forks"},
		{``, ParsedArgs{}, ""},
	}
	for _, tt := range tests {
		got := ParseArguments(tt.in)
		um := strings.Join(uniq(got.Unmapped), ",")
		got.Unmapped = nil
		if !reflect.DeepEqual(got, tt.want) || um != tt.unmapped {
			t.Errorf("%q: got %+v unmapped=%q", tt.in, got, um)
		}
	}
}

func TestSlugify(t *testing.T) {
	if Slugify("Prod  Hosts!") != "prod-hosts" || Slugify("###") != "x" {
		t.Fatal("slugify")
	}
}

func TestMapTrimsNames(t *testing.T) {
	s, keys, tgt := fixture()
	s.Keys[0].Name = "  deploy \n"
	s.Inventories[0].Name = " Prod Hosts "
	s.Templates[0].Name = "site "
	r := Map(s, keys, tgt, Options{})
	if r.PlanSecrets[0].Name != "semaphore: deploy" || r.PlanInventories[0].Name != "semaphore-Prod Hosts" || r.PlanTemplates[0].Name != "site" {
		t.Fatalf("names not trimmed: %+v %+v %+v", r.PlanSecrets[0], r.PlanInventories[0], r.PlanTemplates[0].Name)
	}
}
