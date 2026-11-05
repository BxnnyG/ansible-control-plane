package runs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/scan"
)

// ParseSurvey decodes a stored template survey. Two shapes are accepted: a
// JSON array of fields, or an object in the shape of scan.Survey (with a
// "fields" array). Empty input yields no fields.
func ParseSurvey(raw []byte) ([]scan.Field, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields []scan.Field
	switch raw[0] {
	case '[':
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("%w: survey is not a list of fields", ErrInvalid)
		}
	case '{':
		var s scan.Survey
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%w: survey has an invalid shape", ErrInvalid)
		}
		fields = s.Fields
	default:
		return nil, fmt.Errorf("%w: survey must be a list or an object", ErrInvalid)
	}
	if err := checkFieldDefs(fields, ""); err != nil {
		return nil, err
	}
	return fields, nil
}

func checkFieldDefs(fields []scan.Field, prefix string) error {
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Name == "" {
			return fmt.Errorf("%w: survey field without a name", ErrInvalid)
		}
		if seen[f.Name] {
			return fmt.Errorf("%w: duplicate survey field %s%s", ErrInvalid, prefix, f.Name)
		}
		seen[f.Name] = true
		switch f.Type {
		case scan.TypeStr, scan.TypeInt, scan.TypeFloat, scan.TypeBool, scan.TypeList, scan.TypeDict, scan.TypePath, scan.TypeRaw:
		default:
			return fmt.Errorf("%w: survey field %s%s has unknown type", ErrInvalid, prefix, f.Name)
		}
		if err := checkFieldDefs(f.Fields, prefix+f.Name+"."); err != nil {
			return err
		}
	}
	return nil
}

// SecretFieldNames returns the top-level survey fields flagged secret.
func SecretFieldNames(fields []scan.Field) []string {
	var out []string
	for _, f := range fields {
		if f.Secret {
			out = append(out, f.Name)
		}
	}
	sort.Strings(out)
	return out
}

// ApplySurvey fills defaults of top-level survey fields that are absent from
// vars and validates the result. With strict false, missing required fields
// are tolerated (used for scanned role specs where the playbook may supply
// the value itself). Messages never contain values.
func ApplySurvey(fields []scan.Field, vars map[string]any, strict bool) error {
	for _, f := range fields {
		if _, ok := vars[f.Name]; !ok && f.Default != nil {
			vars[f.Name] = f.Default
		}
	}
	var problems []string
	validateFields(fields, vars, "", strict, &problems)
	if len(problems) > 0 {
		return fmt.Errorf("%w: extra_vars: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	return nil
}

func validateFields(fields []scan.Field, vars map[string]any, path string, strict bool, out *[]string) {
	for _, f := range fields {
		v, ok := vars[f.Name]
		p := path + f.Name
		if !ok || v == nil {
			if f.Required && strict {
				*out = append(*out, p+" is required")
			}
			continue
		}
		validateValue(f, v, p, strict, out)
	}
}

func validateValue(f scan.Field, v any, p string, strict bool, out *[]string) {
	bad := func(want string) { *out = append(*out, p+" must be "+want) }
	switch f.Type {
	case scan.TypeStr, scan.TypePath:
		if _, ok := v.(string); !ok {
			bad("a string")
			return
		}
	case scan.TypeInt:
		switch n := v.(type) {
		case float64:
			if n != math.Trunc(n) || math.IsInf(n, 0) {
				bad("an integer")
				return
			}
		case int, int64, uint64:
		default:
			bad("an integer")
			return
		}
	case scan.TypeFloat:
		switch v.(type) {
		case float64, int, int64, uint64:
		default:
			bad("a number")
			return
		}
	case scan.TypeBool:
		if _, ok := v.(bool); !ok {
			bad("a boolean")
			return
		}
	case scan.TypeList:
		list, ok := v.([]any)
		if !ok {
			bad("a list")
			return
		}
		for i, el := range list {
			ep := fmt.Sprintf("%s[%d]", p, i)
			switch f.Elements {
			case "":
			case scan.TypeDict:
				m, ok := el.(map[string]any)
				if !ok {
					*out = append(*out, ep+" must be a dict")
					continue
				}
				validateFields(f.Fields, m, ep+".", strict, out)
			default:
				validateValue(scan.Field{Type: f.Elements}, el, ep, strict, out)
			}
		}
	case scan.TypeDict:
		m, ok := v.(map[string]any)
		if !ok {
			bad("a dict")
			return
		}
		validateFields(f.Fields, m, p+".", strict, out)
	}
	if len(f.Choices) > 0 && !inChoices(f.Choices, v) {
		*out = append(*out, p+" is not one of the allowed choices")
	}
}

func inChoices(choices []any, v any) bool {
	for _, c := range choices {
		if fmt.Sprint(c) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}
