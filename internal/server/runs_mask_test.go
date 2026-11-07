package server

import (
	"encoding/json"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

func TestMaskRun(t *testing.T) {
	mk := func() runs.Run {
		var r runs.Run
		r.Params.ExtraVars = map[string]any{"db_pass": "hunter2"}
		r.Params.EnvVars = map[string]any{"TOKEN": "abc"}
		r.Overrides = json.RawMessage(`{"limit":"web","extra_vars":{"k":"v"}}`)
		return r
	}
	for role, masked := range map[string]bool{
		policy.RoleViewer: true, policy.RoleOperator: true, policy.RoleEditor: false, policy.RoleAdmin: false,
	} {
		d := policy.Decision{Membership: dbgen.Membership{Role: role}}
		in := mk()
		got := maskRun(d, in)
		b, _ := json.Marshal(got)
		leaks := containsAny(string(b), "hunter2", `"abc"`, `"v"`)
		if masked && leaks {
			t.Errorf("%s: values leak: %s", role, b)
		}
		if masked && (got.Params.ExtraVars["db_pass"] != runs.MaskedValue || got.Params.EnvVars["TOKEN"] != runs.MaskedValue) {
			t.Errorf("%s: keys must stay with masked values: %v", role, got.Params)
		}
		if !masked && !leaks {
			t.Errorf("%s: values must be visible", role)
		}
		if in.Params.ExtraVars["db_pass"] != "hunter2" {
			t.Errorf("%s: input modified", role)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		for i := 0; i+len(x) <= len(s); i++ {
			if s[i:i+len(x)] == x {
				return true
			}
		}
	}
	return false
}
