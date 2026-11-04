package inventory

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/vault"
)

// MaxVarsFileSize bounds a single vars file read by ApplyVarsDirs.
const MaxVarsFileSize = 1 << 20

var varsExts = []string{"", ".yml", ".yaml", ".json"}

// ApplyVarsDirs merges the group_vars/ and host_vars/ directories next to an
// inventory file into the model like Ansible does: a file or directory per
// group or host, files in a directory in lexical order, and values from these
// files override variables defined inside the inventory. Entries for groups
// or hosts that the inventory does not define are ignored with a warning.
// Whole-file vault encrypted vars files cannot be read and are skipped with a
// warning. invDir is the directory of the inventory file inside fsys.
func ApplyVarsDirs(d *Doc, fsys fs.FS, invDir string) ([]string, error) {
	var warnings []string
	for _, kind := range []string{"group_vars", "host_vars"} {
		dir := path.Join(invDir, kind)
		entries, err := fs.ReadDir(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return warnings, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range entries {
			name, plain := varsEntryName(e)
			var vars map[string]any
			if kind == "group_vars" {
				g := d.group(name)
				if g == nil && name == GroupAll {
					g = &Group{Name: GroupAll, Vars: map[string]any{}}
					d.Groups = append(d.Groups, g)
					sort.Slice(d.Groups, func(i, j int) bool { return d.Groups[i].Name < d.Groups[j].Name })
				}
				if g != nil {
					vars = g.Vars
				}
			} else if h := d.Host(name); h != nil {
				vars = h.Vars
			}
			rel := path.Join(dir, e.Name())
			if vars == nil {
				if plain && !knownExt(e) {
					continue // e.g. README.md: not a vars file for any entity
				}
				warnings = append(warnings, fmt.Sprintf("%s: no matching %s in the inventory, ignored", rel, strings.TrimSuffix(kind, "_vars")))
				continue
			}
			loaded, w, err := loadVarsPath(fsys, rel)
			warnings = append(warnings, w...)
			if err != nil {
				return warnings, err
			}
			for k, v := range loaded {
				vars[k] = v
			}
		}
	}
	return warnings, nil
}

// varsEntryName returns the group or host name a top-level entry of
// group_vars/host_vars stands for. Ansible tries the plain name and the name
// plus .yml, .yaml or .json, so an entry whose suffix is not one of those is a
// plain name that merely contains dots (host_vars/web.example.test). plain
// reports that the name is the full entry name.
func varsEntryName(e fs.DirEntry) (name string, plain bool) {
	if !e.IsDir() {
		switch ext := path.Ext(e.Name()); ext {
		case ".yml", ".yaml", ".json":
			return strings.TrimSuffix(e.Name(), ext), false
		}
	}
	return e.Name(), true
}

func knownExt(e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	for _, x := range varsExts {
		if path.Ext(e.Name()) == x {
			return true
		}
	}
	return false
}

// loadVarsPath reads a vars file or all files of a vars directory.
func loadVarsPath(fsys fs.FS, p string) (map[string]any, []string, error) {
	info, err := fs.Stat(fsys, p)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", p, err)
	}
	if !info.IsDir() {
		m, w, err := loadVarsFile(fsys, p, info)
		return m, w, err
	}
	merged := map[string]any{}
	var warnings []string
	walkErr := fs.WalkDir(fsys, p, func(fp string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), "~") {
			return err
		}
		if !knownExt(e) {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		m, w, err := loadVarsFile(fsys, fp, fi)
		warnings = append(warnings, w...)
		for k, v := range m {
			merged[k] = v
		}
		return err
	})
	return merged, warnings, walkErr
}

func loadVarsFile(fsys fs.FS, p string, info fs.FileInfo) (map[string]any, []string, error) {
	if !info.Mode().IsRegular() {
		return nil, []string{p + ": not a regular file, ignored"}, nil
	}
	if info.Size() > MaxVarsFileSize {
		return nil, []string{p + ": larger than 1 MiB, ignored"}, nil
	}
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", p, err)
	}
	if vault.IsVaultFile(data) {
		return nil, []string{p + ": whole-file vault, skipped"}, nil
	}
	m, err := ParseVarsYAML(string(data))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p, err)
	}
	return m, nil, nil
}
