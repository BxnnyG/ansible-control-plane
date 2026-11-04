package vaultops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type pathSeg struct {
	key   string
	index int
	isIdx bool
}

// parseKeyPath parses paths in the notation of the project scan, for example
// "db.password" or "items[2].token". Keys that contain '.' or '[' cannot be
// addressed.
func parseKeyPath(p string) ([]pathSeg, error) {
	if p == "" {
		return nil, invalid("key path must not be empty")
	}
	var segs []pathSeg
	for i := 0; i < len(p); {
		if p[i] == '[' {
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, invalid("key path %q: missing ']'", p)
			}
			n, err := strconv.Atoi(p[i+1 : i+end])
			if err != nil || n < 0 {
				return nil, invalid("key path %q: bad index", p)
			}
			segs = append(segs, pathSeg{index: n, isIdx: true})
			i += end + 1
		} else {
			end := strings.IndexAny(p[i:], ".[")
			key := p[i:]
			if end >= 0 {
				key = p[i : i+end]
			}
			if key == "" {
				return nil, invalid("key path %q has an empty key", p)
			}
			segs = append(segs, pathSeg{key: key})
			i += len(key)
		}
		if i < len(p) && p[i] == '.' {
			i++
			if i == len(p) {
				return nil, invalid("key path %q ends with '.'", p)
			}
		}
	}
	return segs, nil
}

// parseDoc parses a YAML file. With single, a multi-document stream is
// rejected (it cannot be re-encoded faithfully).
func parseDoc(data []byte, single bool) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, invalid("file is empty")
		}
		return nil, invalid("file is not valid YAML")
	}
	if single {
		var extra yaml.Node
		if err := dec.Decode(&extra); err == nil {
			return nil, invalid("multi-document YAML files cannot be edited")
		}
	}
	return &doc, nil
}

func encodeDoc(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// lookup returns the node at segs. With create, a missing last key of a
// mapping is added with an empty scalar value.
func lookup(doc *yaml.Node, segs []pathSeg, create bool) (*yaml.Node, error) {
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, ErrKeyNotFound
		}
		n = n.Content[0]
	}
	for i, s := range segs {
		if n.Kind == yaml.AliasNode {
			return nil, invalid("key path passes through a YAML alias")
		}
		switch {
		case s.isIdx && n.Kind == yaml.SequenceNode && s.index < len(n.Content):
			n = n.Content[s.index]
		case !s.isIdx && n.Kind == yaml.MappingNode:
			var next *yaml.Node
			for j := 0; j+1 < len(n.Content); j += 2 {
				if n.Content[j].Value == s.key {
					next = n.Content[j+1]
					break
				}
			}
			if next == nil {
				if !create || i != len(segs)-1 {
					return nil, ErrKeyNotFound
				}
				next = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str"}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s.key}, next)
			}
			n = next
		default:
			return nil, ErrKeyNotFound
		}
	}
	if n.Kind == yaml.AliasNode {
		return nil, invalid("key path points to a YAML alias")
	}
	return n, nil
}

// setVault turns n into a `!vault` block scalar holding envelope.
func setVault(n *yaml.Node, envelope []byte) error {
	if n.Kind != yaml.ScalarNode {
		return invalid("key path must point to a scalar value")
	}
	n.Tag, n.Style, n.Content = "!vault", yaml.LiteralStyle, nil
	n.Value = string(envelope)
	return nil
}

// walkVault calls fn for every `!vault` scalar below n in document order.
func walkVault(n *yaml.Node, path string, fn func(path string, n *yaml.Node) error) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := walkVault(c, path, fn); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			if err := walkVault(n.Content[i+1], p, fn); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := walkVault(c, path+"["+strconv.Itoa(i)+"]", fn); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if n.Tag == "!vault" {
			return fn(path, n)
		}
	}
	return nil
}
