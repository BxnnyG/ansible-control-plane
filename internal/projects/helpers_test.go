package projects

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"My Playbooks":          "my-playbooks",
		"  --Infra_Repo!! ":     "infra-repo",
		"äöü":                   "",
		"a":                     "a",
		strings.Repeat("x", 80): strings.Repeat("x", 63),
	}
	for in, want := range tests {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSSHTarget(t *testing.T) {
	tests := []struct {
		url  string
		host string
		port int
		ok   bool
	}{
		{"ssh://git@git.example.org/org/repo.git", "git.example.org", 0, true},
		{"ssh://git@git.example.org:2222/org/repo.git", "git.example.org", 2222, true},
		{"git@git.example.org:org/repo.git", "git.example.org", 0, true},
		{"https://git.example.org/org/repo.git", "", 0, false},
		{"file:///tmp/x.git", "", 0, false},
		{"ssh://", "", 0, false},
	}
	for _, tc := range tests {
		h, p, ok := SSHTarget(tc.url)
		if h != tc.host || p != tc.port || ok != tc.ok {
			t.Errorf("SSHTarget(%q)=%q,%d,%v want %q,%d,%v", tc.url, h, p, ok, tc.host, tc.port, tc.ok)
		}
	}
}

func TestField(t *testing.T) {
	var v struct {
		A Field[int] `json:"a"`
		B Field[int] `json:"b"`
		C Field[int] `json:"c"`
	}
	if err := json.Unmarshal([]byte(`{"a":5,"b":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if !v.A.Set || v.A.Value == nil || *v.A.Value != 5 {
		t.Errorf("a: %+v", v.A)
	}
	if !v.B.Set || v.B.Value != nil {
		t.Errorf("b: %+v", v.B)
	}
	if v.C.Set {
		t.Errorf("c must be unset: %+v", v.C)
	}
	if err := json.Unmarshal([]byte(`{"a":"x"}`), &v); err == nil {
		t.Error("expected type error")
	}
}

func TestFingerprints(t *testing.T) {
	// base64("abc") is a stand-in key blob; only the hashing is under test.
	got := fingerprints("# comment\nhost ssh-ed25519 YWJj\n\nbroken\n")
	want := []string{"SHA256:ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}
