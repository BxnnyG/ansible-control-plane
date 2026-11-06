package main

import (
	"io"
	"testing"
)

func TestParseRemediationArgs(t *testing.T) {
	a, err := parseRemediationArgs("remediation", []string{"create", "--workspace", "w", "--name", "n", "--template", "t", "--target", "group",
		"--group", "web", "--check-first", "--min-severity", "warning"}, io.Discard)
	if err != nil || a.Name != "n" || a.Target != "group" || a.RuleGroup != "web" || !a.CheckFirst || a.MinSeverity != "warning" {
		t.Errorf("create %+v %v", a, err)
	}
	if a, err := parseRemediationArgs("remediation", []string{"list", "--workspace", "w"}, io.Discard); err != nil || a.Sub != "list" {
		t.Errorf("list %+v %v", a, err)
	}
	a, err = parseRemediationArgs("alert", []string{"remediate", "--workspace", "w", "--alert", "a", "--rule", "r", "--skip-check"}, io.Discard)
	if err != nil || a.Alert != "a" || a.Rule != "r" || !a.SkipCheck {
		t.Errorf("remediate %+v %v", a, err)
	}
	for _, bad := range [][]string{{}, {"nope"}, {"list"}, {"create", "--workspace", "w", "--name", "n"}, {"list", "--workspace", "w", "extra"}} {
		if _, err := parseRemediationArgs("remediation", bad, io.Discard); err == nil {
			t.Errorf("remediation args %v accepted", bad)
		}
	}
	for _, bad := range [][]string{{"remediate", "--workspace", "w", "--alert", "a"}, {"list", "--workspace", "w"}} {
		if _, err := parseRemediationArgs("alert", bad, io.Discard); err == nil {
			t.Errorf("alert args %v accepted", bad)
		}
	}
}
