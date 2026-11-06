package worker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

func TestBuildArgs(t *testing.T) {
	d := "/tmp/runs/r1"
	full := runproto.Spec{
		Playbook: "site.yml", Limit: "web:!db", Tags: "a,b", SkipTags: "c", Check: true, Diff: true, Verbosity: 3,
		SSHKey: true, Become: true, VaultIDs: []string{"default", "prod"},
	}
	got, err := buildArgs(full, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-i", d + "/inventory.sh", "-e", "@" + d + "/extra_vars.json",
		"--private-key", d + "/secrets/ssh_key", "-e", "ansible_ssh_private_key_file=" + d + "/secrets/ssh_key", "--become-password-file", d + "/secrets/become_pass",
		"--vault-id", "default@" + d + "/secrets/vault/default", "--vault-id", "prod@" + d + "/secrets/vault/prod",
		"--limit", "web:!db", "--tags", "a,b", "--skip-tags", "c", "--check", "--diff", "-vvv", "--", "site.yml",
	}
	if !slices.Equal(got, want) {
		t.Errorf("argv:\n got %q\nwant %q", got, want)
	}
	min, err := buildArgs(runproto.Spec{Playbook: "-evil.yml"}, d)
	if err != nil || min[len(min)-2] != "--" || min[len(min)-1] != "-evil.yml" {
		t.Errorf("minimal argv: %q %v", min, err)
	}
	for _, bad := range []string{"", "../x.yml", "/etc/passwd", "a/../../x.yml"} {
		if _, err := buildArgs(runproto.Spec{Playbook: bad}, d); err == nil {
			t.Errorf("playbook %q accepted", bad)
		}
	}
	if _, err := buildArgs(runproto.Spec{Playbook: "a.yml", VaultIDs: []string{"a@b"}}, d); err == nil {
		t.Error("bad vault label accepted")
	}
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestBuildEnvEnforcedLast(t *testing.T) {
	sp := runproto.Spec{Env: map[string]string{
		"FOO": "bar", "ANSIBLE_CALLBACKS_ENABLED": "other", "ANSIBLE_STDOUT_CALLBACK": "json", "ACP_EVENTS_FILE": "/dev/null",
		"ANSIBLE_LOCAL_TEMP": "/elsewhere", "ANSIBLE_FORCE_COLOR": "0", "TMPDIR": "/x",
	}}
	base := []string{
		"PATH=/usr/bin", "HOME=/home/acp", "ACP_WORKER_TOKEN_FILE=/run/secrets/worker_token", "DATABASE_URL=nope",
		"ANSIBLE_CALLBACK_PLUGINS=/opt/cb", "ANSIBLE_CALLBACKS_ENABLED=acp", "ANSIBLE_HOST_KEY_CHECKING=False", "LC_ALL=C.UTF-8",
	}
	m := envMap(buildEnv(sp, "/tmp/runs/r1", base, "/c", "/r"))
	exp := map[string]string{
		"ANSIBLE_CALLBACKS_ENABLED": "acp", "ANSIBLE_STDOUT_CALLBACK": "default", "ACP_EVENTS_FILE": "/tmp/runs/r1/events.jsonl",
		"ANSIBLE_LOCAL_TEMP": "/tmp/runs/r1/tmp/local", "ANSIBLE_FORCE_COLOR": "1", "TMPDIR": "/tmp/runs/r1/tmp",
		"ANSIBLE_CALLBACK_PLUGINS": "/opt/cb", "ANSIBLE_COLLECTIONS_PATH": "/c", "ANSIBLE_ROLES_PATH": "/r",
		"FOO": "bar", "PATH": "/usr/bin", "ANSIBLE_HOST_KEY_CHECKING": "False", "LC_ALL": "C.UTF-8",
	}
	for k, v := range exp {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if _, ok := m["ACP_WORKER_TOKEN_FILE"]; ok {
		t.Error("worker variable leaked into ansible env")
	}
	if _, ok := m["DATABASE_URL"]; ok {
		t.Error("unlisted variable passed")
	}
}

func makeBundle(t *testing.T, entries map[string]string, extra ...tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range entries {
		mode := int64(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	for i := range extra {
		if err := tw.WriteHeader(&extra[i]); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	b := makeBundle(t, map[string]string{"spec.json": "{}", "project/a/b.yml": "x", "project/run.sh": "#!/bin/sh"},
		tar.Header{Name: "project/link", Typeflag: tar.TypeSymlink, Linkname: "a/b.yml"})
	if err := extract(bytes.NewReader(b), dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "project/a/b.yml")); string(got) != "x" {
		t.Errorf("content = %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "project/run.sh")); fi == nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("exec file mode: %v", fi)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "spec.json")); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode: %v", fi)
	}
	if l, err := os.Readlink(filepath.Join(dir, "project/link")); err != nil || l != "a/b.yml" {
		t.Errorf("symlink: %q %v", l, err)
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	outside := t.TempDir()
	tests := map[string][]byte{
		"dotdot":   makeBundle(t, map[string]string{"../evil": "x"}),
		"absolute": makeBundle(t, map[string]string{"/tmp/evil": "x"}),
		"through symlink": makeBundle(t, nil,
			tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside},
			tar.Header{Name: "link/evil", Typeflag: tar.TypeReg, Mode: 0o644}),
	}
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := extract(bytes.NewReader(b), dir); err == nil {
				t.Error("expected error")
			}
			if _, err := os.Stat(filepath.Join(outside, "evil")); err == nil {
				t.Error("file written outside the run dir")
			}
		})
	}
}

func TestRequirementsKeyAndKinds(t *testing.T) {
	dir := t.TempDir()
	if k, files, err := requirementsKey(dir); err != nil || k != "" || files != nil {
		t.Fatalf("empty: %q %v %v", k, files, err)
	}
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("requirements.yml", "collections:\n  - name: community.general\nroles:\n  - name: x.y\n")
	k1, files, err := requirementsKey(dir)
	if err != nil || len(k1) != 64 || !slices.Equal(files, []string{"requirements.yml"}) {
		t.Fatalf("key: %q %v %v", k1, files, err)
	}
	write("requirements.yml", "collections:\n  - name: community.general\n    version: 1.0.0\n")
	if k2, _, _ := requirementsKey(dir); k2 == k1 {
		t.Error("key must change with content")
	}
	kinds := func(name, body string) []string {
		write(name, body)
		k, err := requirementKinds(name, dir)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if got := kinds("requirements.yml", "collections: [a.b]\nroles: [c.d]\n"); !slices.Equal(got, []string{"role", "collection"}) {
		t.Errorf("map: %v", got)
	}
	if got := kinds("requirements.yml", "collections:\n  - a.b\n"); !slices.Equal(got, []string{"collection"}) {
		t.Errorf("collections only: %v", got)
	}
	if got := kinds("roles/requirements.yml", "- src: x\n"); !slices.Equal(got, []string{"role"}) {
		t.Errorf("legacy roles: %v", got)
	}
	if got := kinds("collections/requirements.yml", "- name: a.b\n"); !slices.Equal(got, []string{"collection"}) {
		t.Errorf("legacy collections: %v", got)
	}
}
