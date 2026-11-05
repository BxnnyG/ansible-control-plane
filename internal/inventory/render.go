package inventory

import (
	"sort"
)

// RenderOptions controls Render.
type RenderOptions struct {
	// Export mimics `ansible-inventory --list --export`: variables stay on
	// the host or group that defines them instead of being merged per host.
	Export bool
	// MaskVault replaces vault values with VaultMask (previews).
	MaskVault bool
}

// Render produces the structure of `ansible-inventory --list`: groups with
// hosts and children, "all" with the top-level groups, and _meta.hostvars.
// Without Export the host variables are merged in Ansible's precedence order:
// all, then parent before child groups (by depth, group priority, name), then
// the host itself.
func Render(d *Doc, o RenderOptions) map[string]any {
	parents := d.parents()
	hostGroups := d.hostGroups()
	depth := d.depths(parents)

	// ancestors returns all groups a host belongs to, directly or not.
	ancestors := func(host string) []string {
		seen := map[string]bool{}
		var walk func(string)
		walk = func(g string) {
			if seen[g] {
				return
			}
			seen[g] = true
			for _, p := range parents[g] {
				walk(p)
			}
		}
		for _, g := range hostGroups[host] {
			walk(g)
		}
		out := make([]string, 0, len(seen))
		for g := range seen {
			out = append(out, g)
		}
		return out
	}
	prio := func(g *Group) int {
		switch p := g.Vars["ansible_group_priority"].(type) {
		case int:
			return p
		case int64:
			return int(p)
		case float64:
			return int(p)
		}
		return 1
	}

	hostvars := map[string]any{}
	for _, h := range d.Hosts {
		merged := map[string]any{}
		if !o.Export {
			groups := make([]*Group, 0)
			if all := d.group(GroupAll); all != nil {
				groups = append(groups, all)
			}
			for _, name := range ancestors(h.Name) {
				groups = append(groups, d.group(name))
			}
			sort.SliceStable(groups, func(i, j int) bool {
				a, b := groups[i], groups[j]
				if (a.Name == GroupAll) != (b.Name == GroupAll) {
					return a.Name == GroupAll
				}
				if depth[a.Name] != depth[b.Name] {
					return depth[a.Name] < depth[b.Name]
				}
				if prio(a) != prio(b) {
					return prio(a) < prio(b)
				}
				return a.Name < b.Name
			})
			for _, g := range groups {
				for k, v := range g.Vars {
					merged[k] = v
				}
			}
		}
		for k, v := range h.Vars {
			merged[k] = v
		}
		hostvars[h.Name] = merged
	}

	out := map[string]any{}
	var tops, ungrouped []string
	for _, h := range d.Hosts {
		if len(hostGroups[h.Name]) == 0 {
			ungrouped = append(ungrouped, h.Name)
		}
	}
	for _, g := range d.Groups {
		if g.Name == GroupAll {
			continue
		}
		entry := groupEntry(g.Hosts, g.Children, nil)
		if o.Export && len(g.Vars) > 0 {
			entry["vars"] = g.Vars
		}
		out[g.Name] = entry
		if len(parents[g.Name]) == 0 {
			tops = append(tops, g.Name)
		}
	}
	sort.Strings(tops)
	allEntry := groupEntry(nil, append(tops, GroupUngrouped), nil)
	if g := d.group(GroupAll); o.Export && g != nil && len(g.Vars) > 0 {
		allEntry["vars"] = g.Vars
	}
	out[GroupAll] = allEntry
	out[GroupUngrouped] = groupEntry(ungrouped, nil, nil)
	out["_meta"] = map[string]any{"hostvars": hostvars}
	if o.MaskVault {
		return maskVault(out).(map[string]any)
	}
	return out
}

// groupEntry builds a group object in the Ansible script inventory format.
// hosts, children and vars are always present (empty, never null): ansible-core
// treats an empty group object as malformed and mistakes it for a host.
func groupEntry(hosts, children []string, vars map[string]any) map[string]any {
	if vars == nil {
		vars = map[string]any{}
	}
	return map[string]any{
		"hosts":    append([]string{}, hosts...),
		"children": append([]string{}, children...),
		"vars":     vars,
	}
}

// depths computes the nesting depth of every group: top-level groups are 1.
func (d *Doc) depths(parents map[string][]string) map[string]int {
	depth := map[string]int{}
	var calc func(string) int
	calc = func(g string) int {
		if v, ok := depth[g]; ok {
			return v
		}
		depth[g] = 1 // cycle guard; the model is acyclic
		best := 0
		for _, p := range parents[g] {
			if v := calc(p); v > best {
				best = v
			}
		}
		depth[g] = best + 1
		return depth[g]
	}
	for _, g := range d.Groups {
		calc(g.Name)
	}
	return depth
}

// MaskVars returns a copy of vars with vault values replaced by VaultMask.
func MaskVars(vars map[string]any) map[string]any {
	m, _ := maskVault(vars).(map[string]any)
	return m
}

func maskVault(v any) any {
	if _, ok := IsVault(v); ok {
		return VaultMask
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = maskVault(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = maskVault(e)
		}
		return out
	}
	return v
}
