package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"gopkg.in/yaml.v3"
)

// InlineVault is a `!vault` tagged scalar found in a YAML document.
type InlineVault struct {
	Path     string // e.g. "db.password" or "items[2].token"
	Envelope []byte // the vault envelope (indentation already stripped by YAML)
}

// FindInlineVault returns all `!vault` tagged scalars in yamlData, in
// document order. Multi-document streams are supported.
func FindInlineVault(yamlData []byte) ([]InlineVault, error) {
	dec := yaml.NewDecoder(bytes.NewReader(yamlData))
	var found []InlineVault
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return found, nil
			}
			return nil, fmt.Errorf("vault: parse yaml: %w", err)
		}
		walk(&doc, "", &found)
	}
}

func walk(n *yaml.Node, path string, out *[]InlineVault) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, path, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			p := n.Content[i].Value
			if path != "" {
				p = path + "." + p
			}
			walk(n.Content[i+1], p, out)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			walk(c, path+"["+strconv.Itoa(i)+"]", out)
		}
	case yaml.ScalarNode:
		if n.Tag == "!vault" {
			*out = append(*out, InlineVault{Path: path, Envelope: []byte(n.Value)})
		}
	case yaml.AliasNode:
		// Aliases point at nodes already visited through their anchor.
	}
}
