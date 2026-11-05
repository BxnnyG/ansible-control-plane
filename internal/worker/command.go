package worker

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

// Paths inside a run directory.
const (
	projectDir   = runproto.DirProject
	inventorySh  = "inventory.sh"
	eventsFile   = "events.jsonl"
	tmpSubdir    = "tmp"
	defaultCbDir = "/usr/share/acp/ansible/callback_plugins"
)

// validPlaybook rejects playbook paths that leave the project directory.
func validPlaybook(p string) error {
	c := path.Clean(p)
	if p == "" || path.IsAbs(c) || c == ".." || strings.HasPrefix(c, "../") || strings.ContainsAny(p, "\x00\n") {
		return fmt.Errorf("playbook path %q is not inside the project", p)
	}
	return nil
}

// buildArgs returns the ansible-playbook argv (no shell involved). dir is the
// run directory; the working directory of the process is dir/project.
func buildArgs(sp runproto.Spec, dir string) ([]string, error) {
	if err := validPlaybook(sp.Playbook); err != nil {
		return nil, err
	}
	args := []string{"-i", filepath.Join(dir, inventorySh), "-e", "@" + filepath.Join(dir, runproto.FileExtraVars)}
	if sp.SSHKey {
		args = append(args, "--private-key", filepath.Join(dir, runproto.FileSSHKey))
	}
	if sp.Become {
		args = append(args, "--become-password-file", filepath.Join(dir, runproto.FileBecomePass))
	}
	for _, l := range sp.VaultIDs {
		if strings.ContainsAny(l, "@/\x00 ") {
			return nil, fmt.Errorf("vault identity label %q is not usable", l)
		}
		args = append(args, "--vault-id", l+"@"+filepath.Join(dir, runproto.DirVault, l))
	}
	if sp.Limit != "" {
		args = append(args, "--limit", sp.Limit)
	}
	if sp.Tags != "" {
		args = append(args, "--tags", sp.Tags)
	}
	if sp.SkipTags != "" {
		args = append(args, "--skip-tags", sp.SkipTags)
	}
	if sp.Check {
		args = append(args, "--check")
	}
	if sp.Diff {
		args = append(args, "--diff")
	}
	if sp.Verbosity > 0 {
		args = append(args, "-"+strings.Repeat("v", min(sp.Verbosity, 4)))
	}
	// "--" ends option parsing: a playbook name starting with "-" is a file.
	return append(args, "--", sp.Playbook), nil
}

// passEnv lists the variables of the worker process that reach ansible.
var passEnv = map[string]bool{
	"PATH": true, "HOME": true, "LANG": true, "TZ": true, "USER": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "REQUESTS_CA_BUNDLE": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "no_proxy": true,
}

// buildEnv assembles the environment: worker passthrough first, then the
// template's env vars, then the worker-enforced variables last so a template
// cannot switch off the event callback or redirect temp directories.
func buildEnv(sp runproto.Spec, dir string, base []string, collections, roles string) []string {
	m := map[string]string{}
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if passEnv[k] || strings.HasPrefix(k, "LC_") || (strings.HasPrefix(k, "ANSIBLE_") && !strings.HasPrefix(k, "ANSIBLE_CALLBACK")) {
			m[k] = v
		}
	}
	cbDir := defaultCbDir
	for _, kv := range base {
		if v, ok := strings.CutPrefix(kv, "ANSIBLE_CALLBACK_PLUGINS="); ok && v != "" {
			cbDir = v
		}
	}
	for k, v := range sp.Env {
		m[k] = v
	}
	tmp := filepath.Join(dir, tmpSubdir)
	m["ANSIBLE_CALLBACKS_ENABLED"] = "acp"
	m["ANSIBLE_CALLBACK_PLUGINS"] = cbDir
	m["ANSIBLE_STDOUT_CALLBACK"] = "default"
	m["ANSIBLE_FORCE_COLOR"] = "1"
	m["ACP_EVENTS_FILE"] = filepath.Join(dir, eventsFile)
	m["ANSIBLE_LOCAL_TEMP"] = filepath.Join(tmp, "local")
	m["ANSIBLE_REMOTE_TEMP"] = filepath.Join(tmp, "remote")
	m["TMPDIR"] = tmp
	m["PYTHONUNBUFFERED"] = "1"
	m["ANSIBLE_NOCOLOR"] = "0"
	if collections != "" {
		m["ANSIBLE_COLLECTIONS_PATH"] = collections
	}
	if roles != "" {
		m["ANSIBLE_ROLES_PATH"] = roles
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// writeInventoryScript writes the executable that prints the inventory for
// ansible's script plugin (`--list`; hostvars are embedded in _meta).
func writeInventoryScript(dir string) error {
	body := "#!/bin/sh\nexec cat '" + strings.ReplaceAll(filepath.Join(dir, runproto.FileInventory), "'", `'\''`) + "'\n"
	return os.WriteFile(filepath.Join(dir, inventorySh), []byte(body), 0o700) //nolint:gosec // must be executable
}
