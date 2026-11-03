package auth

import (
	"sort"
)

// extractRoles reads role names from a claim that is either an object keyed by
// role name (Zitadel project roles) or an array of strings. Unknown shapes
// yield no roles.
func extractRoles(claims map[string]any, claim string) []string {
	var out []string
	switch v := claims[claim].(type) {
	case map[string]any:
		for k := range v {
			out = append(out, k)
		}
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}
