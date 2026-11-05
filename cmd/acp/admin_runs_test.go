package main

import (
	"io"
	"strings"
	"testing"
)

func TestParseRunsArgs(t *testing.T) {
	id := "6f1c1f0e-5a0c-4c1e-9d0e-0b7f4f2d2a11"
	tests := []struct {
		name, group, args, err string
	}{
		{"tpl no args", "template", "", "usage"},
		{"tpl unknown", "template", "delete", "unknown template command"},
		{"tpl list", "template", "list", ""},
		{"tpl create ok", "template", "create --name n --project p --playbook site.yml --inventory i --vault-identity a --vault-identity b --max-concurrent 2", ""},
		{"tpl create no playbook", "template", "create --name n --project p --inventory i", "--playbook is required"},
		{"tpl create bad max", "template", "create --name n --project p --playbook s --inventory i --max-concurrent 500", "--max-concurrent"},
		{"run start ok", "run", "start --template t --limit h --check -e a=b -e c=d=e --wait", ""},
		{"run start no tpl", "run", "start --wait", "--template is required"},
		{"run start bad e", "run", "start --template t -e novalue", "want key=value"},
		{"run show ok", "run", "show " + id + " --tail 5", ""},
		{"run show flags first", "run", "show --log " + id, ""},
		{"run show no id", "run", "show --log", "exactly one run id"},
		{"run show bad id", "run", "show xyz", "invalid run id"},
		{"run list ok", "run", "list --status failed --limit 5", ""},
		{"run list bad limit", "run", "list --limit 0", "--limit must be"},
		{"run extra arg", "run", "list foo", "unexpected argument"},
		{"empty workspace", "run", "list --workspace ''", "--workspace must not be empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := strings.Fields(tc.args)
			for i, s := range args {
				if s == "''" {
					args[i] = ""
				}
			}
			_, err := parseRunsArgs(tc.group, args, io.Discard)
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("got %v, want substring %q", err, tc.err)
			}
		})
	}
}

func TestParseRunsArgsFlags(t *testing.T) {
	a, err := parseRunsArgs("run", strings.Fields("start --template t --check=false -e a=b=c"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !a.CheckSet || a.Check || a.DiffSet || a.Workspace != "default" {
		t.Fatalf("flags: %+v", a)
	}
	vars, _ := parseExtraVars(a.ExtraVars)
	if vars["a"] != "b=c" {
		t.Fatalf("vars: %v", vars)
	}
}

func TestStripANSIAndTail(t *testing.T) {
	if got := stripANSI("\x1b[0;32mok\x1b[0m: host\n"); got != "ok: host\n" {
		t.Fatalf("got %q", got)
	}
	if got := tailLines("a\nb\nc\n", 2); got != "b\nc\n" {
		t.Fatalf("got %q", got)
	}
	if got := tailLines("a\nb\n", 5); got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
}
