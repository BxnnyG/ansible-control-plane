package main

import (
	"io"
	"strings"
	"testing"
)

func TestParseProjectArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
		err  string // substring; empty means success
	}{
		{"no args", "", "usage"},
		{"unknown sub", "delete", "unknown project command"},
		{"list ok", "list --workspace default", ""},
		{"list no workspace", "list", "--workspace is required"},
		{"sync ok", "sync --workspace d --project p", ""},
		{"sync no project", "sync --workspace d", "--project is required"},
		{"sync no workspace", "sync --project p", "--workspace is required"},
		{"extra arg", "list --workspace d extra", "unexpected argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseProjectArgs(strings.Fields(tc.args), io.Discard)
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
				t.Fatalf("got %v, want substring %q", err, tc.err)
			}
		})
	}
}
