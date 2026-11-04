package scan

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Field types as exposed to the UI. They are the argument_specs types
// reduced to the set the form renderer understands.
const (
	TypeStr   = "str"
	TypeInt   = "int"
	TypeFloat = "float"
	TypeBool  = "bool"
	TypeList  = "list"
	TypeDict  = "dict"
	TypePath  = "path"
	TypeRaw   = "raw"
)

// Survey is a form definition generated from one role entry point
// (argument_specs).
type Survey struct {
	Role        string  `json:"role"`
	EntryPoint  string  `json:"entry_point"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Fields      []Field `json:"fields"`
}

// Field is one input of a survey. Nested options of dict (or list of dict)
// fields appear in Fields.
type Field struct {
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Required    bool    `json:"required"`
	Secret      bool    `json:"secret,omitempty"`
	Default     any     `json:"default,omitempty"`
	Choices     []any   `json:"choices,omitempty"`
	Description string  `json:"description,omitempty"`
	Elements    string  `json:"elements,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
}

type specFile struct {
	ArgumentSpecs map[string]entrySpec `yaml:"argument_specs"`
}

type entrySpec struct {
	ShortDescription string             `yaml:"short_description"`
	Description      yaml.Node          `yaml:"description"`
	Options          map[string]optSpec `yaml:"options"`
}

type optSpec struct {
	Type        string             `yaml:"type"`
	Required    bool               `yaml:"required"`
	Default     yaml.Node          `yaml:"default"`
	Choices     []yaml.Node        `yaml:"choices"`
	Description yaml.Node          `yaml:"description"`
	Elements    string             `yaml:"elements"`
	NoLog       bool               `yaml:"no_log"`
	Options     map[string]optSpec `yaml:"options"`
}

// maxSpecDepth bounds nested option recursion.
const maxSpecDepth = 8

func buildSurvey(role, entry string, e entrySpec) Survey {
	return Survey{
		Role:        role,
		EntryPoint:  entry,
		Title:       e.ShortDescription,
		Description: joinDescription(&e.Description),
		Fields:      buildFields(e.Options, 0),
	}
}

func buildFields(opts map[string]optSpec, depth int) []Field {
	fields := make([]Field, 0, len(opts))
	names := make([]string, 0, len(opts))
	for n := range opts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		o := opts[n]
		f := Field{
			Name:        n,
			Type:        mapType(o.Type),
			Required:    o.Required,
			Secret:      o.NoLog,
			Description: joinDescription(&o.Description),
		}
		if o.Elements != "" {
			f.Elements = mapType(o.Elements)
		}
		if !o.NoLog && !hasVaultTag(&o.Default) {
			f.Default = decodeValue(&o.Default)
		}
		for i := range o.Choices {
			if hasVaultTag(&o.Choices[i]) {
				continue
			}
			if v := decodeValue(&o.Choices[i]); v != nil {
				f.Choices = append(f.Choices, v)
			}
		}
		if depth < maxSpecDepth {
			if nested := buildFields(o.Options, depth+1); len(nested) > 0 {
				f.Fields = nested
			}
		}
		fields = append(fields, f)
	}
	return fields
}

func mapType(t string) string {
	switch t {
	case "", "str", "bytes", "bits", "json", "jsonarg":
		return TypeStr
	case "int":
		return TypeInt
	case "float":
		return TypeFloat
	case "bool":
		return TypeBool
	case "list":
		return TypeList
	case "dict":
		return TypeDict
	case "path":
		return TypePath
	default:
		return TypeRaw
	}
}

func joinDescription(n *yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(n.Value)
	case yaml.SequenceNode:
		parts := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode {
				parts = append(parts, strings.TrimSpace(c.Value))
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func hasVaultTag(n *yaml.Node) bool {
	if n.Tag == "!vault" {
		return true
	}
	for _, c := range n.Content {
		if hasVaultTag(c) {
			return true
		}
	}
	return false
}

func decodeValue(n *yaml.Node) any {
	if n.Kind == 0 {
		return nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil
	}
	return normalize(v)
}

// normalize makes decoded YAML JSON-safe (string map keys).
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = normalize(x)
		}
		return m
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	default:
		return v
	}
}

// Schema renders the survey as a JSON-schema-like object (draft 2020-12
// subset) for the UI form renderer. Extra "x-acp-type" keys carry the
// original field type.
func (s Survey) Schema() map[string]any {
	return objectSchema(s.Title, s.Description, s.Fields)
}

func objectSchema(title, desc string, fields []Field) map[string]any {
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		props[f.Name] = f.Schema()
		if f.Required {
			required = append(required, f.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if title != "" {
		out["title"] = title
	}
	if desc != "" {
		out["description"] = desc
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// Schema renders a single field as a JSON-schema-like fragment.
func (f Field) Schema() map[string]any {
	out := map[string]any{"x-acp-type": f.Type}
	if f.Description != "" {
		out["description"] = f.Description
	}
	if f.Default != nil {
		out["default"] = f.Default
	}
	if len(f.Choices) > 0 {
		out["enum"] = f.Choices
	}
	if f.Secret {
		out["writeOnly"] = true
	}
	switch f.Type {
	case TypeStr:
		out["type"] = "string"
	case TypePath:
		out["type"] = "string"
		out["format"] = "path"
	case TypeInt:
		out["type"] = "integer"
	case TypeFloat:
		out["type"] = "number"
	case TypeBool:
		out["type"] = "boolean"
	case TypeDict:
		out["type"] = "object"
		if len(f.Fields) > 0 {
			o := objectSchema("", "", f.Fields)
			out["properties"] = o["properties"]
			if r, ok := o["required"]; ok {
				out["required"] = r
			}
		}
	case TypeList:
		out["type"] = "array"
		items := map[string]any{}
		switch {
		case f.Elements == TypeDict && len(f.Fields) > 0:
			items = objectSchema("", "", f.Fields)
			items["x-acp-type"] = TypeDict
		case f.Elements != "":
			items = Field{Type: f.Elements}.Schema()
		}
		out["items"] = items
	}
	return out
}
