package runs

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Connection and execution variables decide how and where the worker connects
// or which local programs Ansible runs. A value such as
// ansible_ssh_common_args="-o ProxyCommand=..." is code execution in the
// worker, ansible_host/ansible_port redirect credentials to another system.
// They may only be set by workspace admins on the template; run-time
// overrides (extra vars, survey answers) never accept them.
var deniedVarNames = map[string]bool{
	"ansible_connection": true, "ansible_host": true, "ansible_port": true,
	"ansible_ssh_host": true, "ansible_ssh_port": true,
	"ansible_ssh_private_key_file": true, "ansible_private_key_file": true,
	"ansible_ssh_executable": true, "ansible_shell_executable": true,
	"ansible_become_exe": true, "ansible_become_flags": true,
	"ansible_python_interpreter": true, "ansible_playbook_python": true,
	"ansible_pkcs11_provider": true, "ansible_ssh_transfer_method": true,
	"ansible_become_method": true,
}

// deniedVarSuffixes catch families such as ansible_ssh_common_args,
// ansible_scp_extra_args, ansible_ruby_interpreter or ansible_winrm_*_command.
var deniedVarSuffixes = []string{"_args", "_executable", "_interpreter", "_exe", "_flags"}

// VarDenied reports whether name is a connection or execution variable.
func VarDenied(name string) bool {
	n := strings.ToLower(name)
	if deniedVarNames[n] {
		return true
	}
	if !strings.HasPrefix(n, "ansible_") {
		return false
	}
	for _, s := range deniedVarSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// DeniedVars returns the sorted denied names among the keys of vars.
func DeniedVars(vars map[string]any) []string {
	var out []string
	for k := range vars {
		if VarDenied(k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// CheckVarsAllowed returns an ErrInvalid error naming denied variables.
func CheckVarsAllowed(vars map[string]any, where string) error {
	if d := DeniedVars(vars); len(d) > 0 {
		return fmt.Errorf("%w: %s must not set connection or execution variables (%s)", ErrInvalid, where, strings.Join(d, ", "))
	}
	return nil
}

// CheckSurveyAllowed rejects survey fields that would set denied variables.
func CheckSurveyAllowed(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	fields, err := ParseSurvey(raw)
	if err != nil {
		return nil // shape errors are reported by the regular validation
	}
	names := map[string]any{}
	for _, f := range fields {
		names[f.Name] = nil
	}
	return CheckVarsAllowed(names, "the survey")
}
