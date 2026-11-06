package main

import (
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/grouprules"
)

func TestParseGroupRuleArgs(t *testing.T) {
	a, err := parseGroupRuleArgs([]string{"apply", "--workspace", "w", "--rule", "lan", "--author-email", "a@example.com"}, io.Discard)
	if err != nil || a.Sub != "apply" || a.Rule != "lan" || a.AuthorEmail != "a@example.com" || a.AuthorName != "ACP admin" {
		t.Errorf("parsed %+v %v", a, err)
	}
	if a, err := parseGroupRuleArgs([]string{"list", "--workspace", "w"}, io.Discard); err != nil || a.Sub != "list" {
		t.Errorf("list %+v %v", a, err)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"list"}, {"preview", "--workspace", "w"}, {"apply", "--workspace", "w"}, {"list", "--workspace", "w", "extra"}} {
		if _, err := parseGroupRuleArgs(bad, io.Discard); err == nil {
			t.Errorf("args %v accepted", bad)
		}
	}
}

func TestFormatPlan(t *testing.T) {
	got := formatPlan(grouprules.Plan{
		GroupCreate: true, Matched: 3, Keep: 1,
		Add:     []grouprules.AddItem{{Name: "a.lan", CreateHost: true, FleetHostID: uuid.Nil}, {Name: "s", CreateHost: true, AnsibleHost: "192.0.2.2"}},
		Remove:  []string{"old"},
		Untrack: []string{"gone"},
		Skipped: []grouprules.Skipped{{Name: "x", Reason: "dup"}},
	})
	for _, want := range []string{"matched: 3", "group_create: true", "add: a.lan create_host=true ansible_host=-", "add: s create_host=true ansible_host=192.0.2.2",
		"remove: old", "untrack: gone", "skipped: x (dup)"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan lacks %q:\n%s", want, got)
		}
	}
}
