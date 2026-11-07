package worker

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExtractRejectsLinksOutsideProject(t *testing.T) {
	tests := map[string][]tar.Header{
		"absolute target":     {{Name: "project/l", Typeflag: tar.TypeSymlink, Linkname: "/run/secrets/worker_token"}},
		"parent of project":   {{Name: "project/l", Typeflag: tar.TypeSymlink, Linkname: "../secrets/ssh_key"}},
		"deep climb":          {{Name: "project/a/b/l", Typeflag: tar.TypeSymlink, Linkname: "../../../spec.json"}},
		"link outside":        {{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "project"}},
		"chain through links": {{Name: "project/a", Typeflag: tar.TypeSymlink, Linkname: "d/e"}, {Name: "project/d/e/x", Typeflag: tar.TypeSymlink, Linkname: "../../../../..", Mode: 0}},
	}
	for name, hs := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := extract(bytes.NewReader(makeBundle(t, map[string]string{"project/d/e/keep": "x"}, hs...)), dir); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestExtractAllowsLinksInsideProject(t *testing.T) {
	dir := t.TempDir()
	b := makeBundle(t, map[string]string{"project/roles/r/tasks/main.yml": "x"},
		tar.Header{Name: "project/up", Typeflag: tar.TypeSymlink, Linkname: "."},
		tar.Header{Name: "project/r", Typeflag: tar.TypeSymlink, Linkname: "roles/r"},
		tar.Header{Name: "project/roles/r/back", Typeflag: tar.TypeSymlink, Linkname: "../../site.yml"})
	if err := extract(bytes.NewReader(b), dir); err != nil {
		t.Fatal(err)
	}
}

func TestInsideProject(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(project, "ok.yml"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(root, "secret"), nil, 0o600)
	_ = os.Symlink("../secret", filepath.Join(project, "evil.yml"))
	if err := insideProject(project, "ok.yml"); err != nil {
		t.Errorf("ok.yml: %v", err)
	}
	if err := insideProject(project, "missing.yml"); err != nil {
		t.Errorf("missing.yml: %v", err)
	}
	if err := insideProject(project, "evil.yml"); err == nil {
		t.Error("evil.yml must be rejected")
	}
}

func TestSlotsAreUniqueAndReused(t *testing.T) {
	r := &Runner{JobUIDBase: 10100}
	a, relA := r.acquireSlot()
	b, _ := r.acquireSlot()
	if a == b {
		t.Fatalf("slots collide: %d", a)
	}
	relA()
	if c, _ := r.acquireSlot(); c != a {
		t.Errorf("freed slot not reused: %d", c)
	}
	if id := r.ident(b); id.uid != 10100+b {
		t.Errorf("uid = %d", id.uid)
	}
	if (&Runner{}).ident(0) != nil {
		t.Error("isolation must be off without a base")
	}
}

// TestHandOverAndReclaim needs CAP_CHOWN, i.e. root in the test container.
func TestHandOverAndReclaim(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := filepath.Join(t.TempDir(), "run")
	for _, d := range []string{"project/sub", "tmp/home", "secrets"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"project/sub/a.yml", "secrets/ssh_key", eventsFile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink("sub/a.yml", filepath.Join(dir, "project/link"))
	job := &jobIdent{uid: 20042}
	if err := handOver(dir, job); err != nil {
		t.Fatal(err)
	}
	owner := func(rel string) (uint32, uint32, os.FileMode) {
		fi, err := os.Lstat(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		return st.Uid, st.Gid, fi.Mode().Perm()
	}
	if u, _, m := owner("secrets/ssh_key"); u != 20042 || m != 0o600 {
		t.Errorf("ssh_key owner %d mode %o", u, m)
	}
	if u, g, m := owner(eventsFile); u != 20042 || g != uint32(os.Getegid()) || m != 0o640 { //nolint:gosec // ids fit
		t.Errorf("events owner %d group %d mode %o", u, g, m)
	}
	if u, g, m := owner("project/sub/a.yml"); u != 20042 || g != uint32(os.Getegid()) || m != 0o640 { //nolint:gosec // ids fit
		t.Errorf("project file owner %d group %d mode %o", u, g, m)
	}
	if u, g, m := owner("."); u != uint32(os.Geteuid()) || g != 20042 || m != 0o710 { //nolint:gosec // ids fit
		t.Errorf("run dir owner %d group %d mode %o", u, g, m)
	}
	// A job-made directory the supervisor cannot enter must still be removable.
	if err := os.Mkdir(filepath.Join(dir, "tmp/jobdir"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(dir, "tmp/jobdir"), 20042, 20042); err != nil {
		t.Fatal(err)
	}
	removeRun(dir, job)
	if _, err := os.Stat(dir); err == nil {
		t.Error("run dir not removed")
	}
}

// TestGalaxyInstallIsolated runs the install under a job uid and checks the
// published cache belongs to the supervisor and is read-only for job users.
func TestGalaxyInstallIsolated(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	project := filepath.Join(base, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(project, "requirements.yml"), []byte("collections:\n  - name: a.b\n"), 0o600)
	fake := filepath.Join(base, "galaxy.sh")
	// $6 is the -p target; the script must run as the job uid (id -u is recorded).
	_ = os.WriteFile(fake, []byte("#!/bin/sh\nmkdir -p \"$6/ansible_collections/a/b\"\nid -u > \"$6/ansible_collections/a/b/uid\"\n"), 0o755) //nolint:gosec // test fixture
	job := &jobIdent{uid: 20043}
	if err := handOver(base, job); err != nil { // project must be reachable by the job
		t.Fatal(err)
	}
	_ = os.Chmod(base, 0o755)               //nolint:gosec // test fixture
	_ = os.Chmod(filepath.Dir(base), 0o755) //nolint:gosec // t.TempDir parent is 0700
	_ = os.Chmod(fake, 0o755)               //nolint:gosec // test fixture
	r := &Runner{CacheDir: filepath.Join(base, "cache"), GalaxyBin: fake, JobUIDBase: 20000}
	var out bytes.Buffer
	gc, err := r.installRequirements(t.Context(), project, []string{"PATH=/usr/bin:/bin"}, job, &out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	f := filepath.Join(gc.Collections, "ansible_collections/a/b/uid")
	b, err := os.ReadFile(f)
	if err != nil || string(b) != "20043\n" {
		t.Fatalf("install did not run as the job uid: %q %v", b, err)
	}
	fi, _ := os.Stat(f)
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 0 || fi.Mode().Perm()&0o044 != 0o044 || fi.Mode().Perm()&0o022 != 0 {
		t.Errorf("cache file owner %d mode %o", st.Uid, fi.Mode().Perm())
	}
}
