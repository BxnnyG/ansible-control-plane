package inventory

import "testing"

func TestAnsibleHost(t *testing.T) {
	tests := []struct{ name, vars, want string }{
		{"set", `{"ansible_host":"192.0.2.7","x":1}`, "192.0.2.7"},
		{"absent", `{"x":1}`, ""},
		{"not a string", `{"ansible_host":5}`, ""},
		{"empty", ``, ""},
		{"invalid", `{`, ""},
	}
	for _, tc := range tests {
		if got := ansibleHost([]byte(tc.vars)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
