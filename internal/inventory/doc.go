// Package inventory implements the inventory model of ADR-0014: parsing of
// INI and YAML inventories into one relational model, deterministic
// serialization back to YAML and INI, and rendering in the shape of
// `ansible-inventory --list`.
//
// The pure part (Doc, Parse*, Marshal*, Render) has no database or file system
// dependency. Vault-encrypted values are kept as opaque objects and are never
// decrypted; previews replace them with a mask.
package inventory

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// VaultKey marks an opaque vault-encrypted value. It is the same marker
// `ansible-inventory --list` uses: {"__ansible_vault": "<envelope>"}.
const VaultKey = "__ansible_vault"

// VaultMask replaces vault values in previews.
const VaultMask = "<vault>"

// Reserved group names. Both are implicit in Ansible: "all" contains every
// host, "ungrouped" every host without a group. Only "all" may be stored (for
// its vars); membership in either is always implicit.
const (
	GroupAll       = "all"
	GroupUngrouped = "ungrouped"
)

// Limits protect against hostile inputs.
const (
	MaxHosts      = 20000
	MaxGroups     = 5000
	MaxRangeItems = 10000
)

// ErrInvalid wraps all validation and parse errors.
var ErrInvalid = errors.New("invalid inventory")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Host is one inventory host with its own variables.
type Host struct {
	Name string
	Vars map[string]any
}

// Group is a group with variables, child group names and member host names.
type Group struct {
	Name     string
	Vars     map[string]any
	Children []string
	Hosts    []string
}

// Doc is the in-memory inventory. Hosts and Groups are sorted by name, as are
// the Children and Hosts of every group.
type Doc struct {
	Hosts  []*Host
	Groups []*Group
}

var (
	groupNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	hostNameRe  = regexp.MustCompile(`^[^\s\x00-\x1f\x7f]{1,255}$`)
)

// ValidGroupName reports whether name is acceptable as a group name.
func ValidGroupName(name string) bool { return groupNameRe.MatchString(name) }

// ValidHostName reports whether name is acceptable as a host name.
func ValidHostName(name string) bool { return hostNameRe.MatchString(name) }

// IsVault returns the envelope if v is an opaque vault value.
func IsVault(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := m[VaultKey].(string)
	return s, ok
}

// builder collects hosts, groups and relations while parsing.
type builder struct {
	hosts  map[string]*Host
	groups map[string]*Group
}

func newBuilder() *builder {
	return &builder{hosts: map[string]*Host{}, groups: map[string]*Group{}}
}

func (b *builder) host(name string) (*Host, error) {
	if h, ok := b.hosts[name]; ok {
		return h, nil
	}
	if !ValidHostName(name) {
		return nil, invalid("invalid host name %q", name)
	}
	if len(b.hosts) >= MaxHosts {
		return nil, invalid("more than %d hosts", MaxHosts)
	}
	h := &Host{Name: name, Vars: map[string]any{}}
	b.hosts[name] = h
	return h, nil
}

// group returns the group, creating it. "ungrouped" is never stored.
func (b *builder) group(name string) (*Group, error) {
	if g, ok := b.groups[name]; ok {
		return g, nil
	}
	if !ValidGroupName(name) {
		return nil, invalid("invalid group name %q", name)
	}
	if len(b.groups) >= MaxGroups {
		return nil, invalid("more than %d groups", MaxGroups)
	}
	g := &Group{Name: name, Vars: map[string]any{}}
	b.groups[name] = g
	return g, nil
}

// link records child as a child of parent. Links from "all" are implicit.
func (b *builder) link(parent, child string) error {
	if child == GroupAll {
		return invalid("group %q cannot be a child", GroupAll)
	}
	if parent == child {
		return invalid("group %q cannot be its own child", parent)
	}
	if _, err := b.group(child); err != nil {
		return err
	}
	if parent == GroupAll || child == GroupUngrouped {
		return nil
	}
	if parent == GroupUngrouped {
		return invalid("group %q cannot have children", GroupUngrouped)
	}
	p, err := b.group(parent)
	if err != nil {
		return err
	}
	p.Children = append(p.Children, child)
	return nil
}

// member records host as a member of group. Membership in the implicit
// groups is dropped.
func (b *builder) member(group, host string) error {
	if _, err := b.host(host); err != nil {
		return err
	}
	if group == GroupAll || group == GroupUngrouped {
		return nil
	}
	g, err := b.group(group)
	if err != nil {
		return err
	}
	g.Hosts = append(g.Hosts, host)
	return nil
}

func (b *builder) doc() (*Doc, error) {
	d := &Doc{}
	for _, h := range b.hosts {
		d.Hosts = append(d.Hosts, h)
	}
	for n, g := range b.groups {
		if n == GroupUngrouped || n == GroupAll && len(g.Vars) == 0 {
			continue
		}
		g.Children = uniqSorted(g.Children)
		g.Hosts = uniqSorted(g.Hosts)
		d.Groups = append(d.Groups, g)
	}
	sort.Slice(d.Hosts, func(i, j int) bool { return d.Hosts[i].Name < d.Hosts[j].Name })
	sort.Slice(d.Groups, func(i, j int) bool { return d.Groups[i].Name < d.Groups[j].Name })
	for _, g := range d.Groups {
		g.Children = filterNames(g.Children, func(n string) bool { return n != GroupUngrouped })
	}
	if err := d.CheckAcyclic(); err != nil {
		return nil, err
	}
	return d, nil
}

func filterNames(in []string, keep func(string) bool) []string {
	var out []string
	for _, s := range in {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func uniqSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	s := append([]string(nil), in...)
	sort.Strings(s)
	out := s[:1]
	for _, x := range s[1:] {
		if x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}

// CheckAcyclic reports an error if the group graph contains a cycle.
func (d *Doc) CheckAcyclic() error {
	kids := map[string][]string{}
	for _, g := range d.Groups {
		kids[g.Name] = g.Children
	}
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case 1:
			return invalid("group cycle involving %q", n)
		case 2:
			return nil
		}
		state[n] = 1
		for _, c := range kids[n] {
			if err := visit(c); err != nil {
				return err
			}
		}
		state[n] = 2
		return nil
	}
	for _, g := range d.Groups {
		if err := visit(g.Name); err != nil {
			return err
		}
	}
	return nil
}

// group returns the named group or nil.
func (d *Doc) group(name string) *Group {
	for _, g := range d.Groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// Group returns the named group or nil.
func (d *Doc) Group(name string) *Group { return d.group(name) }

// Host returns the named host or nil.
func (d *Doc) Host(name string) *Host {
	for _, h := range d.Hosts {
		if h.Name == name {
			return h
		}
	}
	return nil
}

// parents maps every group name to the sorted names of its parent groups.
func (d *Doc) parents() map[string][]string {
	p := map[string][]string{}
	for _, g := range d.Groups {
		for _, c := range g.Children {
			p[c] = append(p[c], g.Name)
		}
	}
	for k := range p {
		sort.Strings(p[k])
	}
	return p
}

// hostGroups maps every host to the sorted groups it is a direct member of.
func (d *Doc) hostGroups() map[string][]string {
	m := map[string][]string{}
	for _, g := range d.Groups {
		for _, h := range g.Hosts {
			m[h] = append(m[h], g.Name)
		}
	}
	for k := range m {
		sort.Strings(m[k])
	}
	return m
}
