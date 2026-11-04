package auth

import (
	"sort"
	"strings"
)

// zitadelRolesClaim is the default Zitadel project roles claim. Zitadel also
// emits urn:zitadel:iam:org:project:<projectId>:roles with the same shape.
const zitadelRolesClaim = "urn:zitadel:iam:org:project:roles"

// extractRoles reads role names from a claim that is either an object keyed by
// role name (Zitadel project roles) or an array of strings. Unknown shapes
// yield no roles. When the configured claim is the Zitadel default and yields
// nothing, project-specific claims (urn:zitadel:iam:org:project:<id>:roles)
// are used instead (union of role names).
func extractRoles(claims map[string]any, claim string) []string {
	out := rolesFromClaim(claims[claim])
	if len(out) == 0 && claim == zitadelRolesClaim {
		seen := map[string]bool{}
		for k, v := range claims {
			if !strings.HasPrefix(k, "urn:zitadel:iam:org:project:") || !strings.HasSuffix(k, ":roles") || k == claim {
				continue
			}
			for _, r := range rolesFromClaim(v) {
				if !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func rolesFromClaim(raw any) []string {
	var out []string
	switch v := raw.(type) {
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

// claimKeys returns the sorted claim names (never values) for diagnostics.
func claimKeys(claims map[string]any) []string {
	keys := make([]string, 0, len(claims))
	for k := range claims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
