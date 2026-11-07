package runs

import (
	"errors"
	"testing"
)

func TestVarDenied(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"ansible_ssh_common_args", true},
		{"ansible_ssh_extra_args", true},
		{"ansible_scp_extra_args", true},
		{"ansible_ssh_executable", true},
		{"ansible_connection", true},
		{"ansible_python_interpreter", true},
		{"ansible_ruby_interpreter", true},
		{"ansible_shell_executable", true},
		{"ansible_become_exe", true},
		{"ansible_ssh_private_key_file", true},
		{"ansible_host", true},
		{"ANSIBLE_HOST", true},
		{"ansible_user", false},
		{"ansible_become_password", false},
		{"app_args", false},
		{"my_version", false},
	}
	for _, tt := range tests {
		if got := VarDenied(tt.name); got != tt.want {
			t.Errorf("VarDenied(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheckVarsAllowed(t *testing.T) {
	if err := CheckVarsAllowed(map[string]any{"a": 1}, "x"); err != nil {
		t.Errorf("unexpected: %v", err)
	}
	err := CheckVarsAllowed(map[string]any{"ansible_ssh_common_args": "-o ProxyCommand=x"}, "x")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid, got %v", err)
	}
	if err := CheckSurveyAllowed([]byte(`[{"name":"ansible_connection","type":"str"}]`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("survey: %v", err)
	}
	if err := CheckSurveyAllowed([]byte(`[{"name":"version","type":"str"}]`)); err != nil {
		t.Errorf("survey ok: %v", err)
	}
}

func TestEnvDenied(t *testing.T) {
	for _, k := range []string{"PATH", "HOME", "LD_PRELOAD", "LD_AUDIT", "PYTHONPATH", "PYTHONWARNINGS", "ANSIBLE_LIBRARY",
		"ANSIBLE_CONFIG", "ANSIBLE_ACTION_PLUGINS", "ANSIBLE_CALLBACK_PLUGINS", "ANSIBLE_FILTER_PLUGINS", "ACP_TOKEN",
		"ansible_connection_plugins", "BASH_ENV", "GIT_SSH_COMMAND", "ANSIBLE_SSH_ARGS"} {
		if !envDenied(k) {
			t.Errorf("%s must be denied", k)
		}
	}
	for _, k := range []string{"MY_VAR", "HTTPS_PROXY", "ANSIBLE_HOST_KEY_CHECKING", "ANSIBLE_TIMEOUT"} {
		if envDenied(k) {
			t.Errorf("%s must be allowed", k)
		}
	}
}

func TestPatternRejectsFileToken(t *testing.T) {
	if err := ValidatePattern("@/etc/hosts"); !errors.Is(err, ErrInvalid) {
		t.Errorf("@file: %v", err)
	}
	if err := ValidatePattern("web:&db"); err != nil {
		t.Errorf("regular pattern: %v", err)
	}
}
