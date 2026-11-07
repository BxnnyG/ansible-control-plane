package main

import (
	"io"
	"testing"
)

func TestParseSecretArgs(t *testing.T) {
	l, err := parseSecretArgs([]string{"list", "--workspace", "w"}, io.Discard)
	if err != nil || l.Sub != "list" || l.Workspace != "w" {
		t.Errorf("list: %v %+v", err, l)
	}
	d, err := parseSecretArgs([]string{"delete", "--workspace", "w", "--secret", "tok"}, io.Discard)
	if err != nil || d.Secret != "tok" {
		t.Errorf("delete: %v %+v", err, d)
	}
	bad := [][]string{
		{},
		{"nope"},
		{"list"},
		{"delete", "--workspace", "w"},
		{"list", "--workspace", "w", "extra"},
		{"list", "--workspace", "w", "--secret", "x"},
	}
	for _, b := range bad {
		if _, err := parseSecretArgs(b, io.Discard); err == nil {
			t.Errorf("expected error for %v", b)
		}
	}
}
