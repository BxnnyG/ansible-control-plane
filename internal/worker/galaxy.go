package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"
)

// requirementFiles are the places a project may declare Galaxy requirements
// (collections and roles); the first one is the common single-file form.
var requirementFiles = []string{"requirements.yml", "collections/requirements.yml", "roles/requirements.yml"}

// galaxyCache is the result of an install: directories to put on the
// collection and role search paths.
type galaxyCache struct {
	Collections, Roles string
}

// requirementsKey returns the cache key (sha256 over names and contents of the
// requirement files) and the files present; key is empty without any.
func requirementsKey(project string) (string, []string, error) {
	h := sha256.New()
	var present []string
	for _, f := range requirementFiles {
		b, err := os.ReadFile(filepath.Join(project, f)) //nolint:gosec // fixed relative names
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", f, err)
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
		present = append(present, f)
	}
	if len(present) == 0 {
		return "", nil, nil
	}
	return hex.EncodeToString(h.Sum(nil)), present, nil
}

// installRequirements installs collections and roles into
// <cache>/galaxy/<key>, reusing a finished install. A file lock serializes
// concurrent runs with the same requirements. Output goes to out.
func (r *Runner) installRequirements(ctx context.Context, project string, env []string, job *jobIdent, out io.Writer) (*galaxyCache, error) {
	key, files, err := requirementsKey(project)
	if err != nil || key == "" {
		return nil, err
	}
	root := filepath.Join(r.CacheDir, "galaxy")
	if err := os.MkdirAll(root, cacheMode(job)); err != nil {
		return nil, fmt.Errorf("create galaxy cache: %w", err)
	}
	if job != nil { // MkdirAll is subject to the umask
		_ = os.Chmod(r.CacheDir, cacheMode(job))
		_ = os.Chmod(root, cacheMode(job))
	}
	final := filepath.Join(root, key)
	res := &galaxyCache{Collections: filepath.Join(final, "collections"), Roles: filepath.Join(final, "roles")}

	lock, err := os.OpenFile( //nolint:gosec // path built from the cache dir and a hex digest
		filepath.Join(root, key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open galaxy lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := lockFile(ctx, lock); err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()

	if _, err := os.Stat(filepath.Join(final, ".done")); err == nil {
		_, _ = fmt.Fprintf(out, "[acp] reusing cached Galaxy requirements %s\n", key[:12])
		return res, nil
	}
	_, _ = fmt.Fprintf(out, "[acp] installing Galaxy requirements %s\n", key[:12])
	tmp := final + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Join(tmp, "collections"), 0o700); err != nil {
		return nil, fmt.Errorf("create galaxy dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "roles"), 0o700); err != nil {
		return nil, fmt.Errorf("create galaxy dir: %w", err)
	}
	if err := giveTree(tmp, job); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("prepare galaxy dir: %w", err)
	}
	for _, f := range files {
		file := filepath.Join(project, f)
		kinds, err := requirementKinds(f, project)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		for _, kind := range kinds {
			target := filepath.Join(tmp, kind+"s")
			cmd := exec.CommandContext(ctx, r.galaxyBin(), kind, "install", "-r", file, "-p", target) //nolint:gosec // fixed argv
			cmd.Dir = project
			cmd.Env = env
			cmd.SysProcAttr = &syscall.SysProcAttr{Credential: job.credential()}
			cmd.Stdout, cmd.Stderr = out, out
			if err := cmd.Run(); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, fmt.Errorf("ansible-galaxy %s install (%s): %w", kind, f, err)
			}
		}
	}
	if err := reclaim(tmp, job, true); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("take over galaxy dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".done"), nil, 0o600); err != nil {
		return nil, fmt.Errorf("mark galaxy cache: %w", err)
	}
	_ = os.RemoveAll(final)
	if err := os.Rename(tmp, final); err != nil {
		return nil, fmt.Errorf("publish galaxy cache: %w", err)
	}
	return res, nil
}

// lockFile takes an exclusive flock, giving up when ctx is done.
func lockFile(ctx context.Context, f *os.File) error {
	done := make(chan error, 1)
	go func() { done <- syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("lock galaxy cache: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// requirementKinds tells which Galaxy installs a requirements file needs:
// a mapping lists "roles" and/or "collections"; a plain list is the legacy
// roles format (or collections when the file sits in collections/).
func requirementKinds(name, project string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(project, name)) //nolint:gosec // fixed relative names
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	var doc any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	switch d := doc.(type) {
	case map[string]any:
		var kinds []string
		if v, ok := d["roles"]; ok && v != nil {
			kinds = append(kinds, "role")
		}
		if v, ok := d["collections"]; ok && v != nil {
			kinds = append(kinds, "collection")
		}
		return kinds, nil
	case []any:
		if name == "collections/requirements.yml" {
			return []string{"collection"}, nil
		}
		return []string{"role"}, nil
	}
	return nil, nil
}
