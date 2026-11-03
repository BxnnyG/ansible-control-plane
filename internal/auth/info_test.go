package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicConfig(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/api/auth/config", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["oidc"] || got["breakglass"] {
		t.Errorf("config: %v", got)
	}
	if strings.Contains(rec.Body.String(), "client-1") || strings.Contains(rec.Body.String(), "http") {
		t.Errorf("config leaks details: %s", rec.Body.String())
	}
}

func TestInfoAndRoleMappings(t *testing.T) {
	e := newEnv(t)
	in := e.svc.Info()
	if !in.OIDC || in.BreakGlass || in.IssuerHost == "" || strings.Contains(in.IssuerHost, "/") {
		t.Errorf("info: %+v", in)
	}
	m := RoleMappings()
	if len(m) != 4 || m[0].IdPRole != "acp-admin" || m[0].Role != "admin" || m[3].IdPRole != "acp-viewer" {
		t.Errorf("mappings: %+v", m)
	}
}
