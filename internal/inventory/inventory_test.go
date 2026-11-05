package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name) //nolint:gosec // fixed test fixtures
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func js(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// dump renders a Doc as stable text lines for comparison.
func dump(d *Doc) string {
	var out []string
	for _, h := range d.Hosts {
		out = append(out, fmt.Sprintf("host %s %s", h.Name, js(h.Vars)))
	}
	for _, g := range d.Groups {
		out = append(out, fmt.Sprintf("group %s hosts=%v children=%v vars=%s", g.Name, g.Hosts, g.Children, js(g.Vars)))
	}
	return strings.Join(out, "\n")
}

func TestParseINI(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string // lines expected in the dump
		errPart  string
	}{
		{name: "fixture", in: string(fixture(t, "hosts.ini")), want: []string{
			`host standalone.example.test {"ansible_connection":"local"}`,
			`host web02.example.test {"http_port":8080}`,
			`host edge.example.test {"ansible_port":2222,"enabled":true,"role":"front end"}`,
			`host db-b.example.test {}`,
			`group all hosts=[] children=[] vars={"ntp_server":"ntp.example.test"}`,
			`group app hosts=[] children=[db web] vars={}`,
			`group db hosts=[db-a.example.test db-b.example.test db-c.example.test] children=[] vars={}`,
			`group web hosts=[edge.example.test web01.example.test web02.example.test web03.example.test] children=[] vars={"retries":3,"tier":"frontend"}`,
		}},
		{name: "quoted and comments", in: "h1 a='x y' b=\"q\\\"z\" # trailing\n; comment\n", want: []string{
			`host h1 {"a":"x y","b":"q\"z"}`,
		}},
		{name: "typed values", in: "h1 n=5 f=1.5 t=True s=text l=[1,2] none=None\n", want: []string{
			`host h1 {"f":1.5,"l":[1,2],"n":5,"none":null,"s":"text","t":true}`,
		}},
		{name: "ungrouped section", in: "[ungrouped]\nh1\n[g]\nh2\n", want: []string{
			"host h1 {}", "host h2 {}", "group g hosts=[h2] children=[] vars={}",
		}},
		{name: "padded and stepped range", in: "h[08:12:2]\n", want: []string{
			"host h08 {}", "host h10 {}", "host h12 {}",
		}},
		{name: "unterminated quote", in: "h1 a=\"x\n", errPart: "line 1"},
		{name: "bad section", in: "[g\nh1\n", errPart: "malformed section"},
		{name: "cycle", in: "[a:children]\nb\n[b:children]\na\n", errPart: "cycle"},
		{name: "huge range", in: "h[0:99999999]\n", errPart: "too large"},
		{name: "bad host name", in: "[g]\nh1 =x\n", errPart: "key=value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseINI([]byte(tc.in))
			if tc.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errPart) || !errors.Is(err, ErrInvalid) {
					t.Fatalf("err=%v, want containing %q", err, tc.errPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := dump(d)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestParseYAML(t *testing.T) {
	tests := []struct {
		name, in string
		want     []string
		errPart  string
	}{
		{name: "fixture", in: string(fixture(t, "hosts.yml")), want: []string{
			`host standalone.example.test {"ansible_connection":"local"}`,
			`host web01.example.test {"http_port":8080}`,
			`host db01.example.test {"db_password":{"__ansible_vault":"$ANSIBLE_VAULT;1.1;AES256\n30303030303030303030303030303030\n"}}`,
			`group all hosts=[] children=[] vars={"ntp_server":"ntp.example.test"}`,
			`group app hosts=[] children=[db web] vars={}`,
			`group web hosts=[web01.example.test web02.example.test] children=[] vars={"tier":"frontend"}`,
		}},
		{name: "top-level groups without all", in: "web:\n  hosts:\n    h1:\n", want: []string{
			"host h1 {}", "group web hosts=[h1] children=[] vars={}",
		}},
		{name: "host range", in: "all:\n  hosts:\n    h[1:2]:\n      x: 1\n", want: []string{
			`host h1 {"x":1}`, `host h2 {"x":1}`,
		}},
		{name: "group reused under two parents", in: "all:\n  children:\n    a:\n      children:\n        c: {}\n    b:\n      children:\n        c:\n          hosts: {h1: }\n", want: []string{
			"group a hosts=[] children=[c]", "group b hosts=[] children=[c]", "group c hosts=[h1]",
		}},
		{name: "anchors and merge keys", in: "all:\n  vars:\n    base: &b {x: 1}\n    derived:\n      <<: *b\n      y: 2\n", want: []string{
			`vars={"base":{"x":1},"derived":{"x":1,"y":2}}`,
		}},
		{name: "empty document", in: "", want: nil},
		{name: "unexpected key", in: "all:\n  bogus: 1\n", errPart: "unexpected key"},
		{name: "not a mapping", in: "- a\n", errPart: "mapping"},
		{name: "hosts not a mapping", in: "all:\n  hosts: [a]\n", errPart: "must be a mapping"},
		{name: "multiple documents", in: "a: {}\n---\nb: {}\n", errPart: "multiple documents"},
		{name: "cycle", in: "a:\n  children:\n    b:\n      children:\n        a: {}\n", errPart: "cycle"},
		{name: "invalid yaml", in: "all: [", errPart: "yaml"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseYAML([]byte(tc.in))
			if tc.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errPart) || !errors.Is(err, ErrInvalid) {
					t.Fatalf("err=%v, want containing %q", err, tc.errPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := dump(d)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestParseVarsYAML(t *testing.T) {
	m, err := ParseVarsYAML("a: 1\nb: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3030\n")
	if err != nil {
		t.Fatal(err)
	}
	if env, ok := IsVault(m["b"]); !ok || !strings.HasPrefix(env, "$ANSIBLE_VAULT;") {
		t.Errorf("vault not preserved: %v", m)
	}
	if m, err := ParseVarsYAML(" \n"); err != nil || len(m) != 0 {
		t.Errorf("empty: %v %v", m, err)
	}
	for _, bad := range []string{"- a", "just text", "a: ["} {
		if _, err := ParseVarsYAML(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err=%v", bad, err)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	ini, err := ParseINI(fixture(t, "hosts.ini"))
	if err != nil {
		t.Fatal(err)
	}
	yml, err := ParseYAML(fixture(t, "hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]*Doc{"ini": ini, "yaml": yml} {
		t.Run(name+" via yaml", func(t *testing.T) {
			out, err := MarshalYAML(d)
			if err != nil {
				t.Fatal(err)
			}
			again, err := MarshalYAML(d)
			if err != nil || string(again) != string(out) {
				t.Fatalf("not deterministic: %v", err)
			}
			back, err := ParseYAML(out)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if dump(back) != dump(d) {
				t.Errorf("round trip differs:\n%s\n---\n%s\n--- yaml:\n%s", dump(d), dump(back), out)
			}
		})
	}
	t.Run("ini via ini", func(t *testing.T) {
		out, err := MarshalINI(ini)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseINI(out)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if dump(back) != dump(ini) {
			t.Errorf("round trip differs:\n%s\n---\n%s\n--- ini:\n%s", dump(ini), dump(back), out)
		}
	})
	t.Run("vault cannot be written as ini", func(t *testing.T) {
		if _, err := MarshalINI(yml); !errors.Is(err, ErrVaultInINI) {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("vault tag is kept in yaml", func(t *testing.T) {
		out, _ := MarshalYAML(yml)
		if !strings.Contains(string(out), "db_password: !vault |") {
			t.Errorf("missing !vault tag:\n%s", out)
		}
	})
}

func TestMarshalINIValues(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"plain", "plain"}, {"with space", `"with space"`}, {"", `""`}, {"5", `"5"`}, {"True", `"True"`},
		{true, "True"}, {int64(3), "3"}, {1.5, "1.5"}, {nil, "None"},
		{[]any{"a", int64(1)}, `["a",1]`}, {map[string]any{"k": "v"}, `{"k":"v"}`},
	}
	for _, tc := range tests {
		got, err := iniValue(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("iniValue(%v)=%q,%v want %q", tc.in, got, err, tc.want)
		}
		if back := parseINIValue(got); !reflect.DeepEqual(norm(back), norm(tc.in)) {
			t.Errorf("value %v does not round trip: got %v", tc.in, back)
		}
	}
}

func norm(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func TestRender(t *testing.T) {
	d, err := ParseYAML([]byte(`
all:
  vars: {a: all, b: all, c: all}
  hosts:
    lone: {}
  children:
    parent:
      vars: {b: parent, c: parent}
      children:
        child:
          vars: {c: child}
          hosts:
            h1: {d: host}
            h2:
    other:
      vars: {c: other}
      hosts: {h1: }
`))
	if err != nil {
		t.Fatal(err)
	}
	r := Render(d, RenderOptions{})
	hv := r["_meta"].(map[string]any)["hostvars"].(map[string]any)
	// other (depth 1) loses against child (depth 2); parent < child.
	if got, want := js(hv["h1"]), `{"a":"all","b":"parent","c":"child","d":"host"}`; got != want {
		t.Errorf("h1 hostvars=%s want %s", got, want)
	}
	if got, want := js(hv["lone"]), `{"a":"all","b":"all","c":"all"}`; got != want {
		t.Errorf("lone hostvars=%s want %s", got, want)
	}
	if got, want := js(r["all"]), `{"children":["other","parent","ungrouped"],"hosts":[],"vars":{}}`; got != want {
		t.Errorf("all=%s want %s", got, want)
	}
	if got, want := js(r["ungrouped"]), `{"children":[],"hosts":["lone"],"vars":{}}`; got != want {
		t.Errorf("ungrouped=%s want %s", got, want)
	}
	if got, want := js(r["child"]), `{"children":[],"hosts":["h1","h2"],"vars":{}}`; got != want {
		t.Errorf("child=%s want %s", got, want)
	}
	if got, want := js(r["parent"]), `{"children":["child"],"hosts":[],"vars":{}}`; got != want {
		t.Errorf("parent=%s want %s", got, want)
	}

	ex := Render(d, RenderOptions{Export: true})
	if got, want := js(ex["child"]), `{"children":[],"hosts":["h1","h2"],"vars":{"c":"child"}}`; got != want {
		t.Errorf("export child=%s want %s", got, want)
	}
	exHost := ex["_meta"].(map[string]any)["hostvars"].(map[string]any)["h1"]
	if got, want := js(exHost), `{"d":"host"}`; got != want {
		t.Errorf("export h1=%s want %s", got, want)
	}
}

func TestRenderGroupPriority(t *testing.T) {
	d, err := ParseYAML([]byte("all:\n  children:\n    a:\n      vars: {x: a, ansible_group_priority: 10}\n      hosts: {h: }\n    b:\n      vars: {x: b}\n      hosts: {h: }\n"))
	if err != nil {
		t.Fatal(err)
	}
	hv := Render(d, RenderOptions{})["_meta"].(map[string]any)["hostvars"].(map[string]any)["h"].(map[string]any)
	if hv["x"] != "a" {
		t.Errorf("priority ignored: %v", hv)
	}
}

func TestRenderMasksVault(t *testing.T) {
	d, err := ParseYAML(fixture(t, "hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	masked := js(Render(d, RenderOptions{MaskVault: true}))
	if strings.Contains(masked, "ANSIBLE_VAULT") || !strings.Contains(masked, `"db_password":"<vault>"`) {
		t.Errorf("vault not masked: %s", masked)
	}
	if raw := js(Render(d, RenderOptions{})); !strings.Contains(raw, "__ansible_vault") {
		t.Errorf("unmasked render lost the vault object")
	}
	if m := MaskVars(map[string]any{"l": []any{map[string]any{VaultKey: "x"}}}); js(m) != `{"l":["<vault>"]}` {
		t.Errorf("MaskVars: %s", js(m))
	}
}

func TestApplyVarsDirs(t *testing.T) {
	newDoc := func() *Doc {
		d, err := ParseINI([]byte("h1 x=inline\n[web]\nh2\n[web:vars]\ny=inline\n"))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	fsys := fstest.MapFS{
		"inv/hosts":                         {Data: []byte("")},
		"inv/group_vars/all.yml":            {Data: []byte("z: all\n")},
		"inv/group_vars/web/10-a.yml":       {Data: []byte("y: file\nw: 1\n")},
		"inv/group_vars/web/20-b.yaml":      {Data: []byte("w: 2\n")},
		"inv/group_vars/web/.hidden.yml":    {Data: []byte("bad: [\n")},
		"inv/group_vars/ghost.yml":          {Data: []byte("a: 1\n")},
		"inv/group_vars/readme.md":          {Data: []byte("doc")},
		"inv/host_vars/h1.yml":              {Data: []byte("x: file\n")},
		"inv/host_vars/h2":                  {Data: []byte("secret: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  3030\n")},
		"other/group_vars/web.yml":          {Data: []byte("ignored: true\n")},
		"inv/host_vars/h3.yml":              {Data: []byte("$ANSIBLE_VAULT;1.1;AES256\n3030\n")},
		"inv/group_vars/web/30-vaulted.yml": {Data: []byte("$ANSIBLE_VAULT;1.1;AES256\n3030\n")},
	}
	d := newDoc()
	warnings, err := ApplyVarsDirs(d, fsys, "inv")
	if err != nil {
		t.Fatal(err)
	}
	want := `host h1 {"x":"file"}
host h2 {"secret":{"__ansible_vault":"$ANSIBLE_VAULT;1.1;AES256\n3030\n"}}
group all hosts=[] children=[] vars={"z":"all"}
group web hosts=[h2] children=[] vars={"w":2,"y":"file"}`
	if got := dump(d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	sort.Strings(warnings)
	if len(warnings) != 3 || !strings.Contains(warnings[0], "ghost.yml") || !strings.Contains(warnings[1], "h3.yml") && !strings.Contains(warnings[1], "30-vaulted") {
		t.Errorf("warnings=%v", warnings)
	}
	if _, err := ApplyVarsDirs(newDoc(), fstest.MapFS{"inv/host_vars/h1.yml": {Data: []byte("a: [")}}, "inv"); !errors.Is(err, ErrInvalid) {
		t.Errorf("broken vars file: %v", err)
	}
	if w, err := ApplyVarsDirs(newDoc(), fstest.MapFS{}, "inv"); err != nil || len(w) != 0 {
		t.Errorf("no dirs: %v %v", w, err)
	}
}

func TestApplyVarsDirsDottedExtensionlessNames(t *testing.T) {
	d, err := ParseINI([]byte("web.example.test x=inline\n[db.prod]\nweb.example.test\n"))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"inv/host_vars/web.example.test":  {Data: []byte("x: file\n")},
		"inv/group_vars/db.prod":          {Data: []byte("tier: db\n")},
		"inv/host_vars/unrelated.example": {Data: []byte("a: [\n")}, // no such host: must not be parsed
	}
	warnings, err := ApplyVarsDirs(d, fsys, "inv")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
	if v := d.Host("web.example.test").Vars["x"]; v != "file" {
		t.Errorf("host var = %v", v)
	}
	if v := d.Group("db.prod").Vars["tier"]; v != "db" {
		t.Errorf("group var = %v", v)
	}
	src, err := collectVarsSources(d, fsys, "inv")
	if err != nil || len(src) != 2 {
		t.Fatalf("sources: %v %v", src, err)
	}
}

// TestRenderGroupShape guards the Ansible script inventory format: every group
// is an object with list-valued hosts/children and a vars object, even when
// empty (ansible-core 2.21 warns about "malformed group" otherwise).
func TestRenderGroupShape(t *testing.T) {
	for name, yml := range map[string]string{
		"empty":   "all:\n  children:\n    g: {}\n",
		"grouped": "all:\n  children:\n    g:\n      hosts: {h: }\n",
		"mixed":   "all:\n  hosts:\n    lone: {}\n  children:\n    g:\n      hosts: {h: }\n",
	} {
		d, err := ParseYAML([]byte(yml))
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range []RenderOptions{{}, {Export: true}, {MaskVault: true}} {
			var back map[string]any
			if err := json.Unmarshal([]byte(js(Render(d, o))), &back); err != nil {
				t.Fatal(err)
			}
			for _, g := range []string{"all", "ungrouped", "g"} {
				obj, ok := back[g].(map[string]any)
				if !ok {
					t.Fatalf("%s %+v: group %s is %T", name, o, g, back[g])
				}
				for _, k := range []string{"hosts", "children"} {
					if _, ok := obj[k].([]any); !ok {
						t.Errorf("%s: %s.%s is %T, want list", name, g, k, obj[k])
					}
				}
				if _, ok := obj["vars"].(map[string]any); !ok {
					t.Errorf("%s: %s.vars is %T, want object", name, g, obj["vars"])
				}
			}
			if _, ok := back["_meta"].(map[string]any)["hostvars"].(map[string]any); !ok {
				t.Errorf("%s: _meta.hostvars missing", name)
			}
		}
	}
}
