package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

func TestAlertsAPIListAndScope(t *testing.T) {
	e := newConnEnv(t, policy.RoleViewer)
	hostA, hostB := e.fleet.hosts[0], e.fleet.hosts[1]
	e.fleet.alerts = []fleet.AlertView{
		{ID: uuid.New(), HostID: &hostA.ID, Severity: "critical"},
		{ID: uuid.New(), HostID: &hostB.ID, Severity: "warning"},
		{ID: uuid.New(), Severity: "info"}, // unlinked
	}
	rec := e.do("GET", "/alerts", "")
	var all []fleet.AlertView
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &all) != nil || len(all) != 3 {
		t.Fatalf("unrestricted: %d %s", rec.Code, rec.Body.String())
	}
	netA := netScope("00000000-0000-0000-0000-00000000000a")
	scoped := newConnEnv(t, policy.RoleViewer, netA)
	scoped.fleet.hosts, scoped.fleet.alerts = e.fleet.hosts, e.fleet.alerts
	rec = scoped.do("GET", "/alerts", "")
	var some []fleet.AlertView
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &some) != nil || len(some) != 1 || some[0].Severity != "critical" {
		t.Fatalf("scoped sees only alerts of visible hosts: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAlertsAPIFilters(t *testing.T) {
	hid, cid := uuid.NewString(), uuid.NewString()
	e := newConnEnv(t, policy.RoleViewer)
	if rec := e.do("GET", "/alerts?state=open&host_id="+hid+"&connector="+cid, ""); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if f := e.fleet.lastAlert; f.State != "open" || f.HostID == nil || f.HostID.String() != hid || f.ConnectorID == nil || f.ConnectorID.String() != cid {
		t.Errorf("filter %+v", f)
	}
	for _, q := range []string{"state=bogus", "host_id=nope", "connector=nope"} {
		if rec := e.do("GET", "/alerts?"+q, ""); rec.Code != 400 {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestAlertsAPIRequiresFleetRead(t *testing.T) {
	if rec := newConnEnv(t, "").do("GET", "/alerts", ""); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("no membership: %d", rec.Code)
	}
}

func TestConnectorCreateShowsGeneratedSecretOnce(t *testing.T) {
	e := newConnEnv(t, policy.RoleAdmin)
	e.conns.generated = "generated-once-value"
	rec := e.do("POST", "/connectors", `{"type":"alert_webhook","name":"hook"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["generated_secret"] != "generated-once-value" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("generated secret missing: %v", out)
	}
	e.conns.generated = ""
	rec = e.do("POST", "/connectors", `{"type":"alert_webhook","name":"hook2"}`)
	if strings.Contains(rec.Body.String(), "generated_secret") {
		t.Error("no generated secret expected")
	}
}

func TestAlertHookEndpoint(t *testing.T) {
	e := newConnEnv(t, policy.RoleViewer)
	post := func(id, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/hooks/alerts/"+id, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	known := e.conns.hookID.String()
	if rec := post(known, "Bearer s3cret", `{"alerts":[]}`); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"alerts":2`) {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body.String())
	}
	if e.conns.hookAuth != "Bearer s3cret" {
		t.Errorf("authorization header not passed through")
	}
	if len(*e.audits) != 1 || (*e.audits)[0] != "connector.alert_webhook" {
		t.Errorf("audits %v", *e.audits)
	}
	// Unknown id, malformed id and rejected credentials are indistinguishable.
	for name, id := range map[string]string{"unknown": uuid.NewString(), "malformed": "nope"} {
		if rec := post(id, "Bearer s3cret", `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	e.conns.hookErr = connectors.ErrWebhookRejected
	if rec := post(known, "Bearer wrong", `{}`); rec.Code != http.StatusNotFound || rec.Body.String() != "not found\n" {
		t.Errorf("rejected: %d %q", rec.Code, rec.Body.String())
	}
	e.conns.hookErr = errors.Join(connectors.ErrWebhookPayload, errors.New("x"))
	if rec := post(known, "Bearer s3cret", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad payload: %d", rec.Code)
	}
	big := strings.Repeat("a", connectors.MaxAlertWebhookBody+10)
	if rec := post(known, "Bearer s3cret", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("large: %d", rec.Code)
	}
}
