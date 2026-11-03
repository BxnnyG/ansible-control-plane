package auth

import (
	"net/http"
	"net/url"
)

// RoleMapping is one IdP role to workspace role pair.
type RoleMapping struct {
	IdPRole string `json:"idp_role"`
	Role    string `json:"role"`
}

// Info is the non-secret authentication status shown to the UI.
type Info struct {
	OIDC       bool
	BreakGlass bool
	// IssuerHost is the host (with port) of the OIDC issuer, empty without OIDC.
	IssuerHost string
}

// Info reports which login methods are enabled. It contains no secrets.
func (s *Service) Info() Info {
	in := Info{OIDC: s.o.Issuer != "", BreakGlass: s.o.BreakGlass}
	if u, err := url.Parse(s.o.Issuer); err == nil {
		in.IssuerHost = u.Host
	}
	return in
}

// RoleMappings lists the IdP role names that map onto workspace roles, in
// descending privilege.
func RoleMappings() []RoleMapping {
	out := make([]RoleMapping, 0, len(idpRoles))
	for i := len(idpRoles) - 1; i >= 0; i-- {
		out = append(out, RoleMapping{IdPRole: idpRoles[i].idp, Role: idpRoles[i].role})
	}
	return out
}

// handleConfig is public: the login page needs it before a session exists.
func (s *Service) handleConfig(w http.ResponseWriter, _ *http.Request) {
	in := s.Info()
	writeJSON(w, http.StatusOK, map[string]bool{"oidc": in.OIDC, "breakglass": in.BreakGlass})
}
