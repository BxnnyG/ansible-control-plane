package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"gopkg.in/yaml.v3"
)

// ParseVarsYAML parses a YAML mapping of variables. Empty input yields an
// empty map. `!vault` scalars become opaque vault objects.
func ParseVarsYAML(text string) (map[string]any, error) {
	root, err := parseRoot([]byte(text))
	if err != nil {
		return nil, err
	}
	if root == nil {
		return map[string]any{}, nil
	}
	return mapping(root, "vars")
}

// parseRoot decodes a single YAML document and returns its root node, or nil
// for an empty document.
func parseRoot(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, invalid("yaml: %v", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, invalid("yaml: multiple documents are not supported")
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	markVault(&doc)
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return nil, nil
	}
	return root, nil
}

// markVault rewrites `!vault` scalars into {VaultKey: envelope} mappings so
// that a plain decode keeps them opaque.
func markVault(n *yaml.Node) {
	for i, c := range n.Content {
		if c.Kind == yaml.ScalarNode && c.Tag == "!vault" {
			n.Content[i] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: VaultKey},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: c.Value},
			}}
			continue
		}
		markVault(c)
	}
}

// mapping decodes a mapping node into normalized JSON-compatible values.
func mapping(n *yaml.Node, what string) (map[string]any, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil, invalid("%s must be a mapping", what)
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, invalid("%s: %v", what, err)
	}
	nv, err := normalize(v)
	if err != nil {
		return nil, invalid("%s: %v", what, err)
	}
	m, _ := nv.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// normalize converts decoded YAML into JSON-compatible values.
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[k] = ne
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(k)] = ne
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	case time.Time:
		return x.Format(time.RFC3339), nil
	default:
		return v, nil
	}
}

// ParseYAML parses an inventory in Ansible's YAML (or JSON) format. Top-level
// keys are groups; "all" holds the implicit root group.
func ParseYAML(data []byte) (*Doc, error) {
	root, err := parseRoot(data)
	if err != nil {
		return nil, err
	}
	b := newBuilder()
	if root == nil {
		return b.doc()
	}
	if root.Kind == yaml.AliasNode {
		root = root.Alias
	}
	if root.Kind != yaml.MappingNode {
		return nil, invalid("top level must be a mapping of groups")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if err := b.yamlGroup(root.Content[i].Value, root.Content[i+1], 0); err != nil {
			return nil, err
		}
	}
	return b.doc()
}

const maxGroupDepth = 64

func (b *builder) yamlGroup(name string, body *yaml.Node, depth int) error {
	if depth > maxGroupDepth {
		return invalid("groups nested deeper than %d", maxGroupDepth)
	}
	if body.Kind == yaml.AliasNode {
		body = body.Alias
	}
	g, err := b.group(name)
	if err != nil {
		return err
	}
	if body.Kind == yaml.ScalarNode && body.Tag == "!!null" {
		return nil
	}
	if body.Kind != yaml.MappingNode {
		return invalid("group %q must be a mapping", name)
	}
	for i := 0; i+1 < len(body.Content); i += 2 {
		key, val := body.Content[i].Value, body.Content[i+1]
		switch key {
		case "vars":
			vars, err := mapping(val, "vars of group "+name)
			if err != nil {
				return err
			}
			for k, v := range vars {
				g.Vars[k] = v
			}
		case "hosts":
			if err := b.yamlHosts(name, val); err != nil {
				return err
			}
		case "children":
			if val.Kind == yaml.AliasNode {
				val = val.Alias
			}
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
				continue
			}
			if val.Kind != yaml.MappingNode {
				return invalid("children of group %q must be a mapping", name)
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				child := val.Content[j].Value
				if err := b.link(name, child); err != nil {
					return err
				}
				if err := b.yamlGroup(child, val.Content[j+1], depth+1); err != nil {
					return err
				}
			}
		default:
			return invalid("unexpected key %q in group %q", key, name)
		}
	}
	return nil
}

func (b *builder) yamlHosts(group string, n *yaml.Node) error {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return invalid("hosts of group %q must be a mapping", group)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		pattern := n.Content[i].Value
		names, err := ExpandHostPattern(pattern)
		if err != nil {
			return err
		}
		val := n.Content[i+1]
		var vars map[string]any
		if val.Kind != yaml.ScalarNode || val.Tag != "!!null" {
			if vars, err = mapping(val, "vars of host "+pattern); err != nil {
				return err
			}
		}
		for _, name := range names {
			if err := b.member(group, name); err != nil {
				return err
			}
			h := b.hosts[name]
			for k, v := range vars {
				h.Vars[k] = v
			}
		}
	}
	return nil
}
