package inventory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MarshalYAML serializes the inventory in Ansible's YAML format. The output
// is deterministic: keys are sorted, a host's variables appear at its first
// occurrence and later occurrences are empty mappings.
func MarshalYAML(d *Doc) ([]byte, error) {
	parents := d.parents()
	hostGroups := d.hostGroups()
	seenHost := map[string]bool{}
	seenGroup := map[string]bool{}

	hostsNode := func(names []string) (*yaml.Node, error) {
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, name := range names {
			val := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if h := d.Host(name); h != nil && !seenHost[name] {
				seenHost[name] = true
				v, err := varsNode(h.Vars)
				if err != nil {
					return nil, err
				}
				val = v
			}
			n.Content = append(n.Content, scalar(name), val)
		}
		return n, nil
	}
	var groupNode func(name string) (*yaml.Node, error)
	groupNode = func(name string) (*yaml.Node, error) {
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		g := d.group(name)
		if g == nil || seenGroup[name] {
			return n, nil
		}
		seenGroup[name] = true
		if len(g.Hosts) > 0 {
			hn, err := hostsNode(g.Hosts)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, scalar("hosts"), hn)
		}
		if len(g.Vars) > 0 {
			vn, err := varsNode(g.Vars)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, scalar("vars"), vn)
		}
		if len(g.Children) > 0 {
			cn := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for _, c := range g.Children {
				child, err := groupNode(c)
				if err != nil {
					return nil, err
				}
				cn.Content = append(cn.Content, scalar(c), child)
			}
			n.Content = append(n.Content, scalar("children"), cn)
		}
		return n, nil
	}

	all := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	var ungrouped []string
	for _, h := range d.Hosts {
		if len(hostGroups[h.Name]) == 0 {
			ungrouped = append(ungrouped, h.Name)
		}
	}
	// Hosts are emitted at their first occurrence in document order, which is
	// ungrouped hosts first, then the group tree.
	if len(ungrouped) > 0 {
		hn, err := hostsNode(ungrouped)
		if err != nil {
			return nil, err
		}
		all.Content = append(all.Content, scalar("hosts"), hn)
	}
	if g := d.group(GroupAll); g != nil && len(g.Vars) > 0 {
		vn, err := varsNode(g.Vars)
		if err != nil {
			return nil, err
		}
		all.Content = append(all.Content, scalar("vars"), vn)
	}
	var tops []string
	for _, g := range d.Groups {
		if g.Name != GroupAll && len(parents[g.Name]) == 0 {
			tops = append(tops, g.Name)
		}
	}
	if len(tops) > 0 {
		cn := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, t := range tops {
			gn, err := groupNode(t)
			if err != nil {
				return nil, err
			}
			cn.Content = append(cn.Content, scalar(t), gn)
		}
		all.Content = append(all.Content, scalar("children"), cn)
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalar("all"), all}}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, fmt.Errorf("marshal inventory yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal inventory yaml: %w", err)
	}
	return buf.Bytes(), nil
}

func scalar(s string) *yaml.Node {
	n := &yaml.Node{}
	_ = n.Encode(s)
	return n
}

// varsNode builds a mapping node with sorted keys. Vault objects become
// `!vault` literal scalars.
func varsNode(m map[string]any) (*yaml.Node, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, k := range sortedKeys(m) {
		v, err := valueNode(m[k])
		if err != nil {
			return nil, err
		}
		n.Content = append(n.Content, scalar(k), v)
	}
	return n, nil
}

func valueNode(v any) (*yaml.Node, error) {
	if env, ok := IsVault(v); ok {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!vault", Style: yaml.LiteralStyle, Value: env}, nil
	}
	switch x := v.(type) {
	case map[string]any:
		return varsNode(x)
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range x {
			en, err := valueNode(e)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, en)
		}
		return n, nil
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, fmt.Errorf("encode value: %w", err)
	}
	return n, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrVaultInINI is returned when an inventory with vault values would have to
// be written as INI, which cannot represent them.
var ErrVaultInINI = fmt.Errorf("%w: vault values cannot be written in INI format", ErrInvalid)

// MarshalINI serializes the inventory in Ansible's INI format, deterministic
// like MarshalYAML. Variables are written inline as key=value.
func MarshalINI(d *Doc) ([]byte, error) {
	hostGroups := d.hostGroups()
	seenHost := map[string]bool{}
	var buf strings.Builder

	line := func(name string) error {
		buf.WriteString(name)
		if h := d.Host(name); h != nil && !seenHost[name] {
			seenHost[name] = true
			for _, k := range sortedKeys(h.Vars) {
				s, err := iniValue(h.Vars[k])
				if err != nil {
					return err
				}
				buf.WriteString(" " + k + "=" + s)
			}
		}
		buf.WriteString("\n")
		return nil
	}
	var ungrouped []string
	for _, h := range d.Hosts {
		if len(hostGroups[h.Name]) == 0 {
			ungrouped = append(ungrouped, h.Name)
		}
	}
	for _, n := range ungrouped {
		if err := line(n); err != nil {
			return nil, err
		}
	}
	for _, g := range d.Groups {
		if g.Name == GroupAll {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString("[" + g.Name + "]\n")
		for _, h := range g.Hosts {
			if err := line(h); err != nil {
				return nil, err
			}
		}
		if len(g.Children) > 0 {
			buf.WriteString("\n[" + g.Name + ":children]\n" + strings.Join(g.Children, "\n") + "\n")
		}
		if err := writeINIVars(&buf, g.Name, g.Vars); err != nil {
			return nil, err
		}
	}
	if g := d.group(GroupAll); g != nil && len(g.Vars) > 0 {
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		if err := writeINIVars(&buf, GroupAll, g.Vars); err != nil {
			return nil, err
		}
	}
	return []byte(buf.String()), nil
}

func writeINIVars(buf *strings.Builder, group string, vars map[string]any) error {
	if len(vars) == 0 {
		return nil
	}
	buf.WriteString("\n[" + group + ":vars]\n")
	for _, k := range sortedKeys(vars) {
		s, err := iniValue(vars[k])
		if err != nil {
			return err
		}
		buf.WriteString(k + "=" + s + "\n")
	}
	return nil
}

func iniValue(v any) (string, error) {
	if _, ok := IsVault(v); ok {
		return "", ErrVaultInINI
	}
	switch x := v.(type) {
	case string:
		if x == "" || strings.ContainsAny(x, " \t\"'#;=\\[{\n") || intRe.MatchString(x) || floatRe.MatchString(x) ||
			x == "True" || x == "False" || x == "None" {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x) + `"`, nil
		}
		return x, nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case nil:
		return "None", nil
	case int, int64, uint64:
		return fmt.Sprint(x), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	default:
		if hasVault(v) {
			return "", ErrVaultInINI
		}
		b, err := json.Marshal(x)
		if err != nil {
			return "", fmt.Errorf("encode value: %w", err)
		}
		return string(b), nil
	}
}

func hasVault(v any) bool {
	if _, ok := IsVault(v); ok {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for _, e := range x {
			if hasVault(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if hasVault(e) {
				return true
			}
		}
	}
	return false
}

// MarshalVarsYAML renders variables as a YAML mapping with sorted keys; vault
// values keep their `!vault` tag. An empty map yields an empty string.
func MarshalVarsYAML(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	n, err := varsNode(m)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", fmt.Errorf("marshal vars: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("marshal vars: %w", err)
	}
	return buf.String(), nil
}
