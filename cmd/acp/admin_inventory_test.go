package main

import (
	"io"
	"strings"
	"testing"
)

func TestParseInventoryArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
		err  string // substring; empty means success
	}{
		{"no args", "", "usage"},
		{"unknown sub", "delete", "unknown inventory command"},
		{"list ok", "list --workspace default", ""},
		{"list no workspace", "list", "--workspace is required"},
		{"import ok", "import --workspace d --project p --path hosts.ini --name Lab", ""},
		{"import db ok", "import --workspace d --project p --path hosts.ini --name Lab --mode db", ""},
		{"missing project", "import --workspace d --path hosts.ini --name Lab", "--project is required"},
		{"missing path", "import --workspace d --project p --name Lab", "--path is required"},
		{"missing name", "import --workspace d --project p --path x", "--name is required"},
		{"bad mode", "import --workspace d --project p --path x --name n --mode file", "--mode must be"},
		{"db with branch", "import --workspace d --project p --path x --name n --mode db --write-branch b", "only valid in git mode"},
		{"extra arg", "list --workspace d extra", "unexpected argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseInventoryArgs(strings.Fields(tc.args), io.Discard)
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("got %v, want substring %q", err, tc.err)
			}
		})
	}
}
