// Package scan analyzes a checked-out Ansible project tree (ADR-0013).
//
// It is pure Go and read-only: no Git, no database, no network. The walk is
// bounded in file count and file size and never follows symlinks. Files
// that are Ansible Vault encrypted are never read beyond their header line.
package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/vault"
	"gopkg.in/yaml.v3"
)

// Limits bound the work done by Scan. Zero values select the defaults.
type Limits struct {
	MaxFiles    int   // regular files visited (default 20000)
	MaxFileSize int64 // largest file that is parsed, in bytes (default 1 MiB)
}

// Default limits.
const (
	DefaultMaxFiles    = 20000
	DefaultMaxFileSize = 1 << 20
	headerProbe        = 256
)

// Result is the scan outcome. All slices are non-nil and sorted by path.
type Result struct {
	Playbooks    []Playbook    `json:"playbooks"`
	Roles        []Role        `json:"roles"`
	Requirements []Requirement `json:"requirements"`
	AnsibleCfg   *AnsibleCfg   `json:"ansible_cfg,omitempty"`
	VarsFiles    []VarsFile    `json:"vars_files"`
	Inventories  []Inventory   `json:"inventories"`
	VaultFiles   []VaultFile   `json:"vault_files"`
	InlineVault  []InlineVault `json:"inline_vault"`
	Warnings     []Warning     `json:"warnings"`
}

// Playbook is a YAML file containing a list of plays.
type Playbook struct {
	Path    string   `json:"path"`
	Name    string   `json:"name,omitempty"` // name of the first play
	Plays   int      `json:"plays"`
	Hosts   []string `json:"hosts"`
	Roles   []string `json:"roles"`
	Imports []string `json:"imports"` // import_playbook targets
}

// Role is a directory under roles/ with the usual role layout.
type Role struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	EntryPoints []string `json:"entry_points"`
	Surveys     []Survey `json:"surveys"`
}

// Requirement is one role or collection from a requirements file.
type Requirement struct {
	File    string `json:"file"`
	Kind    string `json:"kind"` // "role" or "collection"
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Src     string `json:"src,omitempty"` // credentials in URLs are removed
}

// AnsibleCfg holds the whitelisted, non-secret settings of ansible.cfg.
// Keys are "section.key".
type AnsibleCfg struct {
	Path     string            `json:"path"`
	Settings map[string]string `json:"settings"`
}

// VarsFile is a group_vars/host_vars file.
type VarsFile struct {
	Path    string `json:"path"`
	Scope   string `json:"scope"` // "group" or "host"
	Name    string `json:"name"`
	Vaulted bool   `json:"vaulted"`
}

// Inventory is an inventory candidate.
type Inventory struct {
	Path   string `json:"path"`
	Format string `json:"format"` // "ini" or "yaml"
	Source string `json:"source"` // "convention" or "ansible.cfg"
}

// VaultFile is a whole-file vault; only the header is evaluated.
type VaultFile struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	VaultID string `json:"vault_id,omitempty"`
}

// InlineVault lists the YAML paths of `!vault` values in a file.
type InlineVault struct {
	File  string   `json:"file"`
	Paths []string `json:"paths"`
}

// Warning is a non-fatal problem with one file.
type Warning struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type entry struct {
	rel  string
	size int64
}

// cfgKeys is the whitelist of ansible.cfg settings that are reported.
var cfgKeys = map[string]bool{
	"defaults.inventory":                 true,
	"defaults.roles_path":                true,
	"defaults.collections_path":          true,
	"defaults.collections_paths":         true,
	"defaults.remote_user":               true,
	"defaults.host_key_checking":         true,
	"defaults.forks":                     true,
	"defaults.timeout":                   true,
	"defaults.gathering":                 true,
	"defaults.interpreter_python":        true,
	"defaults.stdout_callback":           true,
	"defaults.retry_files_enabled":       true,
	"defaults.strategy":                  true,
	"privilege_escalation.become":        true,
	"privilege_escalation.become_method": true,
	"privilege_escalation.become_user":   true,
}

type scanner struct {
	root string
	lim  Limits
	res  *Result
	warn map[string]bool
	invs map[string]Inventory
}

// Scan analyzes the project tree at root.
func Scan(ctx context.Context, root string, opts Limits) (*Result, error) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = DefaultMaxFileSize
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("scan: resolve root: %w", err)
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return nil, errors.New("scan: root is not a directory")
	}
	s := &scanner{
		root: real,
		lim:  opts,
		warn: map[string]bool{},
		invs: map[string]Inventory{},
		res: &Result{
			Playbooks: []Playbook{}, Roles: []Role{}, Requirements: []Requirement{},
			VarsFiles: []VarsFile{}, Inventories: []Inventory{}, VaultFiles: []VaultFile{},
			InlineVault: []InlineVault{}, Warnings: []Warning{},
		},
	}
	files, err := s.walk(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.process(ctx, files); err != nil {
		return nil, err
	}
	s.finish()
	return s.res, nil
}

func (s *scanner) addWarning(p, msg string) {
	k := p + "\x00" + msg
	if s.warn[k] {
		return
	}
	s.warn[k] = true
	s.res.Warnings = append(s.res.Warnings, Warning{Path: p, Message: msg})
}

func (s *scanner) walk(ctx context.Context) ([]entry, error) {
	var files []entry
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, rerr := filepath.Rel(s.root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if err != nil {
			s.addWarning(rel, "unreadable")
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.IsDir():
			if d.Name() == ".git" {
				return fs.SkipDir
			}
		case d.Type()&fs.ModeSymlink != 0:
			s.addWarning(rel, "symlink skipped")
		case d.Type().IsRegular():
			if len(files) >= s.lim.MaxFiles {
				s.addWarning(".", fmt.Sprintf("file limit of %d reached, scan truncated", s.lim.MaxFiles))
				return fs.SkipAll
			}
			info, ierr := d.Info()
			if ierr != nil {
				s.addWarning(rel, "unreadable")
				return nil
			}
			files = append(files, entry{rel: rel, size: info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan: walk: %w", err)
	}
	return files, nil
}

// readHead reads at most n bytes from the start of a file.
func (s *scanner) readHead(rel string, n int) ([]byte, error) {
	f, err := os.Open(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:m], nil
}

func (s *scanner) readAll(e entry) ([]byte, error) {
	if e.size > s.lim.MaxFileSize {
		s.addWarning(e.rel, "file too large, skipped")
		return nil, errors.New("too large")
	}
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(e.rel)))
	if err != nil {
		s.addWarning(e.rel, "unreadable")
		return nil, err
	}
	return data, nil
}

func isYAML(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".yml" || ext == ".yaml"
}

func segments(p string) []string { return strings.Split(p, "/") }

func hasSegment(segs []string, names ...string) bool {
	for _, s := range segs[:len(segs)-1] {
		for _, n := range names {
			if s == n {
				return true
			}
		}
	}
	return false
}

func (s *scanner) process(ctx context.Context, files []entry) error {
	// ansible.cfg first: it may point at inventories.
	cfgInv := s.readCfg(files)

	roleDirs := map[string]*roleAcc{}
	for _, e := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		segs := segments(e.rel)

		head, err := s.readHead(e.rel, headerProbe)
		if err != nil {
			s.addWarning(e.rel, "unreadable")
			continue
		}
		if vault.IsVaultFile(head) {
			s.recordVault(e, head, segs)
			continue
		}

		// Role bookkeeping works on paths only.
		if ra := trackRole(roleDirs, segs); ra != nil {
			ra.files = append(ra.files, e)
		}

		isVars := varsScope(segs) != ""
		inInvDir := hasSegment(segs, "inventories", "inventory") || underAny(e.rel, cfgInv)
		if isVars && !isVarsFileName(segs) {
			continue
		}
		if isVars {
			s.res.VarsFiles = append(s.res.VarsFiles, newVarsFile(segs, e.rel, false))
		}
		if !isYAML(e.rel) {
			if inInvDir && !isVars && !hasSegment(segs, "roles") {
				s.maybeINI(e, cfgInv)
			}
			continue
		}
		if hasSegment(segs, "templates", "files") {
			continue
		}
		data, err := s.readAll(e)
		if err != nil {
			continue
		}
		s.handleYAML(e, segs, data, inInvDir, isVars, cfgInv)
	}
	s.finishRoles(ctx, roleDirs)
	return nil
}

func isJSON(p string) bool { return strings.EqualFold(path.Ext(p), ".json") }

func (s *scanner) recordVault(e entry, head []byte, segs []string) {
	h, err := vault.ParseHeader(firstLine(head))
	if err != nil {
		s.addWarning(e.rel, "invalid vault header")
		return
	}
	s.res.VaultFiles = append(s.res.VaultFiles, VaultFile{Path: e.rel, Version: h.Version, VaultID: h.VaultID})
	if varsScope(segs) != "" {
		s.res.VarsFiles = append(s.res.VarsFiles, newVarsFile(segs, e.rel, true))
	}
}

func firstLine(b []byte) []byte {
	b = bytes.TrimLeft(b, " \t\r\n")
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}

// varsScope returns "group" or "host" when the path is inside a vars dir.
func varsScope(segs []string) string {
	for _, sg := range segs[:len(segs)-1] {
		switch sg {
		case "group_vars":
			return "group"
		case "host_vars":
			return "host"
		}
	}
	return ""
}

// isVarsFileName reports whether a file below group_vars/host_vars is a vars
// file the way Ansible loads them: no extension, .yml, .yaml or .json. A file
// directly in the vars directory may also carry a dotted host name such as
// host_vars/web.example.test; known non-vars extensions are still skipped.
func isVarsFileName(segs []string) bool {
	last := segs[len(segs)-1]
	ext := strings.ToLower(path.Ext(last))
	if ext == "" || isYAML(last) || isJSON(last) {
		return true
	}
	if len(segs) < 2 {
		return false
	}
	parent := segs[len(segs)-2]
	if parent != "host_vars" {
		return false
	}
	switch ext {
	case ".orig", ".ini", ".cfg", ".retry", ".md", ".txt", ".j2", ".bak", ".swp", ".tmp", ".rej":
		return false
	}
	return true
}

func newVarsFile(segs []string, rel string, vaulted bool) VarsFile {
	scope := varsScope(segs)
	name := ""
	for i, sg := range segs[:len(segs)-1] {
		if sg == "group_vars" || sg == "host_vars" {
			name = segs[i+1]
			if i+1 == len(segs)-1 && (isYAML(name) || isJSON(name)) {
				name = strings.TrimSuffix(name, path.Ext(name))
			}
			break
		}
	}
	return VarsFile{Path: rel, Scope: scope, Name: name, Vaulted: vaulted}
}

func underAny(rel string, prefixes []string) bool {
	for _, p := range prefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// readCfg parses ansible.cfg at the project root and returns the cleaned,
// root-relative inventory paths it references.
func (s *scanner) readCfg(files []entry) []string {
	for _, e := range files {
		if e.rel != "ansible.cfg" {
			continue
		}
		head, err := s.readHead(e.rel, headerProbe)
		if err == nil && vault.IsVaultFile(head) {
			return nil
		}
		data, err := s.readAll(e)
		if err != nil {
			return nil
		}
		cfg := &AnsibleCfg{Path: e.rel, Settings: map[string]string{}}
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line[0] == '#' || line[0] == ';' {
				continue
			}
			if line[0] == '[' && strings.HasSuffix(line, "]") {
				section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				k, v, ok = strings.Cut(line, ":")
			}
			if !ok {
				continue
			}
			key := section + "." + strings.ToLower(strings.TrimSpace(k))
			if !cfgKeys[key] {
				continue
			}
			v = stripInlineComment(strings.TrimSpace(v))
			if len(v) > 512 {
				v = v[:512]
			}
			cfg.Settings[key] = v
		}
		s.res.AnsibleCfg = cfg
		var out []string
		for _, p := range strings.Split(cfg.Settings["defaults.inventory"], ",") {
			p = strings.TrimSpace(p)
			if p == "" || path.IsAbs(p) || strings.HasPrefix(p, "~") || strings.Contains(p, "$") {
				continue
			}
			p = path.Clean(p)
			if p == "." || p == ".." || strings.HasPrefix(p, "../") {
				continue
			}
			out = append(out, p)
		}
		return out
	}
	return nil
}

func stripInlineComment(v string) string {
	for _, m := range []string{" #", " ;"} {
		if i := strings.Index(v, m); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
	}
	return v
}

func (s *scanner) addInventory(rel, format string, cfgInv []string) {
	src := "convention"
	if underAny(rel, cfgInv) {
		src = "ansible.cfg"
	}
	s.invs[rel] = Inventory{Path: rel, Format: format, Source: src}
}

func (s *scanner) maybeINI(e entry, cfgInv []string) {
	ext := strings.ToLower(path.Ext(e.rel))
	switch ext {
	case "", ".ini", ".cfg", ".hosts":
		s.addInventory(e.rel, "ini", cfgInv)
	}
}

func (s *scanner) handleYAML(e entry, segs []string, data []byte, inInvDir, isVars bool, cfgInv []string) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		s.addWarning(e.rel, "yaml parse error: "+firstLineStr(err.Error()))
		return
	}
	if found, err := vault.FindInlineVault(data); err == nil && len(found) > 0 {
		paths := make([]string, 0, len(found))
		for _, f := range found {
			paths = append(paths, f.Path)
		}
		s.res.InlineVault = append(s.res.InlineVault, InlineVault{File: e.rel, Paths: paths})
	}
	if len(doc.Content) == 0 {
		return
	}
	root := doc.Content[0]

	base := path.Base(e.rel)
	if base == "requirements.yml" || base == "requirements.yaml" {
		if e.rel == base || e.rel == "roles/"+base || e.rel == "collections/"+base {
			s.parseRequirements(e.rel, root)
			return
		}
	}
	if isVars || hasSegment(segs, "roles", "ansible_collections") {
		return
	}
	if inInvDir {
		if root.Kind == yaml.MappingNode && looksLikeInventory(root) {
			s.addInventory(e.rel, "yaml", cfgInv)
		}
		return
	}
	if pb, ok := parsePlaybook(e.rel, root); ok {
		s.res.Playbooks = append(s.res.Playbooks, pb)
	}
}

func firstLineStr(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// yaml.v3 messages may quote content; keep only the position and kind.
	if i := strings.IndexByte(s, '`'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func mapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalarList(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!vault" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Tag != "!vault" {
				out = append(out, c.Value)
			}
		}
		return out
	}
	return nil
}

func looksLikeInventory(root *yaml.Node) bool {
	if mapGet(root, "all") != nil {
		return true
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		v := root.Content[i+1]
		if v.Kind == yaml.MappingNode && (mapGet(v, "hosts") != nil || mapGet(v, "children") != nil) {
			return true
		}
	}
	return false
}

func parsePlaybook(rel string, root *yaml.Node) (Playbook, bool) {
	if root.Kind != yaml.SequenceNode || len(root.Content) == 0 {
		return Playbook{}, false
	}
	pb := Playbook{Path: rel, Hosts: []string{}, Roles: []string{}, Imports: []string{}}
	hosts, roles := map[string]bool{}, map[string]bool{}
	for _, play := range root.Content {
		if play.Kind != yaml.MappingNode {
			return Playbook{}, false
		}
		h := mapGet(play, "hosts")
		imp := mapGet(play, "import_playbook")
		if h == nil && imp == nil {
			return Playbook{}, false
		}
		pb.Plays++
		if pb.Plays == 1 {
			if n := mapGet(play, "name"); n != nil && n.Kind == yaml.ScalarNode {
				pb.Name = n.Value
			}
		}
		for _, v := range scalarList(h) {
			hosts[v] = true
		}
		pb.Imports = append(pb.Imports, scalarList(imp)...)
		if r := mapGet(play, "roles"); r != nil && r.Kind == yaml.SequenceNode {
			for _, item := range r.Content {
				switch item.Kind {
				case yaml.ScalarNode:
					roles[item.Value] = true
				case yaml.MappingNode:
					for _, k := range []string{"role", "name"} {
						if v := mapGet(item, k); v != nil && v.Kind == yaml.ScalarNode {
							roles[v.Value] = true
							break
						}
					}
				}
			}
		}
	}
	pb.Hosts = sortedKeys(hosts)
	pb.Roles = sortedKeys(roles)
	sort.Strings(pb.Imports)
	return pb, true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseRequirements handles both the list form (roles) and the mapping form
// with roles: and collections: keys.
func (s *scanner) parseRequirements(rel string, root *yaml.Node) {
	switch root.Kind {
	case yaml.SequenceNode:
		s.requirementItems(rel, "role", root)
	case yaml.MappingNode:
		if r := mapGet(root, "roles"); r != nil {
			s.requirementItems(rel, "role", r)
		}
		if c := mapGet(root, "collections"); c != nil {
			s.requirementItems(rel, "collection", c)
		}
	}
}

func (s *scanner) requirementItems(rel, kind string, seq *yaml.Node) {
	if seq.Kind != yaml.SequenceNode {
		return
	}
	for _, it := range seq.Content {
		r := Requirement{File: rel, Kind: kind}
		switch it.Kind {
		case yaml.ScalarNode:
			r.Name = it.Value
		case yaml.MappingNode:
			r.Name = scalarOf(mapGet(it, "name"))
			r.Version = scalarOf(mapGet(it, "version"))
			r.Src = sanitizeSrc(scalarOf(mapGet(it, "src")))
			if r.Name == "" {
				r.Name = r.Src
			}
		}
		if r.Name != "" {
			r.Name = sanitizeSrc(r.Name)
			s.res.Requirements = append(s.res.Requirements, r)
		}
	}
}

func scalarOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!vault" {
		return ""
	}
	return n.Value
}

// sanitizeSrc removes userinfo (credentials) from URL-like values.
func sanitizeSrc(v string) string {
	i := strings.Index(v, "://")
	if i < 0 {
		return v
	}
	rest := v[i+3:]
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		hostEnd = len(rest)
	}
	if at := strings.LastIndexByte(rest[:hostEnd], '@'); at >= 0 {
		rest = rest[at+1:]
	}
	return v[:i+3] + rest
}

type roleAcc struct {
	name  string
	dir   string
	files []entry
}

var roleSubdirs = map[string]bool{
	"tasks": true, "meta": true, "defaults": true, "vars": true,
	"handlers": true, "templates": true, "files": true,
}

// trackRole returns the accumulator for the role the path belongs to, if
// any. The first "roles" directory in the path defines the role root;
// vendored collections are ignored.
func trackRole(roles map[string]*roleAcc, segs []string) *roleAcc {
	for i, sg := range segs {
		if sg == "ansible_collections" {
			return nil
		}
		if sg == "roles" && i+3 < len(segs) && roleSubdirs[segs[i+2]] {
			dir := strings.Join(segs[:i+2], "/")
			ra := roles[dir]
			if ra == nil {
				ra = &roleAcc{name: segs[i+1], dir: dir}
				roles[dir] = ra
			}
			return ra
		}
		if sg == "roles" {
			return nil
		}
	}
	return nil
}

func (s *scanner) finishRoles(ctx context.Context, roles map[string]*roleAcc) {
	for _, ra := range roles {
		if ctx.Err() != nil {
			return
		}
		role := Role{Name: ra.name, Path: ra.dir, EntryPoints: []string{}, Surveys: []Survey{}}
		specs := map[string]entrySpec{}
		// meta/main.yml first so that argument_specs.yml wins per entry point.
		for _, pass := range []string{"main", "argument_specs"} {
			for _, e := range ra.files {
				b := strings.TrimPrefix(e.rel, ra.dir+"/")
				want := "meta/" + pass
				if b != want+".yml" && b != want+".yaml" {
					continue
				}
				data, err := s.readAll(e)
				if err != nil {
					continue
				}
				var sf specFile
				if err := yaml.Unmarshal(data, &sf); err != nil {
					s.addWarning(e.rel, "argument_specs parse error: "+firstLineStr(err.Error()))
					continue
				}
				for k, v := range sf.ArgumentSpecs {
					specs[k] = v
				}
			}
		}
		for k := range specs {
			role.EntryPoints = append(role.EntryPoints, k)
		}
		sort.Strings(role.EntryPoints)
		for _, k := range role.EntryPoints {
			role.Surveys = append(role.Surveys, buildSurvey(ra.name, k, specs[k]))
		}
		s.res.Roles = append(s.res.Roles, role)
	}
}

func (s *scanner) finish() {
	r := s.res
	for _, inv := range s.invs {
		r.Inventories = append(r.Inventories, inv)
	}
	sort.Slice(r.Playbooks, func(i, j int) bool { return r.Playbooks[i].Path < r.Playbooks[j].Path })
	sort.Slice(r.Roles, func(i, j int) bool { return r.Roles[i].Path < r.Roles[j].Path })
	sort.SliceStable(r.Requirements, func(i, j int) bool {
		a, b := r.Requirements[i], r.Requirements[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	sort.Slice(r.VarsFiles, func(i, j int) bool { return r.VarsFiles[i].Path < r.VarsFiles[j].Path })
	sort.Slice(r.Inventories, func(i, j int) bool { return r.Inventories[i].Path < r.Inventories[j].Path })
	sort.Slice(r.VaultFiles, func(i, j int) bool { return r.VaultFiles[i].Path < r.VaultFiles[j].Path })
	sort.Slice(r.InlineVault, func(i, j int) bool { return r.InlineVault[i].File < r.InlineVault[j].File })
	sort.Slice(r.Warnings, func(i, j int) bool {
		if r.Warnings[i].Path != r.Warnings[j].Path {
			return r.Warnings[i].Path < r.Warnings[j].Path
		}
		return r.Warnings[i].Message < r.Warnings[j].Message
	})
}
