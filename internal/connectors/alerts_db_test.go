package connectors_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/alertwebhook"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

func (e *env) alerts(t *testing.T, c dbgen.Connector) map[string]dbgen.Alert {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), "SELECT external_key, resolved_at, started_at, severity, summary, labels FROM alerts WHERE connector_id = $1", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]dbgen.Alert{}
	for rows.Next() {
		var a dbgen.Alert
		if err := rows.Scan(&a.ExternalKey, &a.ResolvedAt, &a.StartedAt, &a.Severity, &a.Summary, &a.Labels); err != nil {
			t.Fatal(err)
		}
		out[a.ExternalKey] = a
	}
	return out
}

func alert(key, sev string) connectors.Alert {
	return connectors.Alert{ExternalKey: key, Host: "web1.example.test", IP: "192.0.2.10", Severity: sev, Summary: key + " problem",
		Labels: map[string]string{"state": "CRIT"}}
}

func TestPullSyncUpsertsAndResolvesAlerts(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c := e.create(t, "pull", 60)
	started := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	a1 := alert("a1", connectors.SeverityCritical)
	a1.StartedAt = started
	e.script.Set(connectors.Result{ReplaceAlerts: true, Alerts: []connectors.Alert{a1, alert("a2", connectors.SeverityWarning)}})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerManual); err != nil {
		t.Fatal(err)
	}
	got := e.alerts(t, c)
	if len(got) != 2 || got["a1"].ResolvedAt.Valid || !got["a1"].StartedAt.Time.Equal(started) || got["a1"].Severity != "critical" ||
		!strings.Contains(string(got["a1"].Labels), "CRIT") {
		t.Fatalf("after first sync: %+v", got)
	}

	// a2 disappears -> resolved; a1 stays open, keeps its start, takes the new text.
	a1b := alert("a1", connectors.SeverityWarning)
	a1b.Summary = "changed"
	a1b.StartedAt = time.Now().UTC()
	e.script.Set(connectors.Result{ReplaceAlerts: true, Alerts: []connectors.Alert{a1b}})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	got = e.alerts(t, c)
	if !got["a2"].ResolvedAt.Valid || got["a1"].ResolvedAt.Valid || got["a1"].Summary != "changed" || got["a1"].Severity != "warning" ||
		!got["a1"].StartedAt.Time.Equal(started) {
		t.Fatalf("after second sync: %+v", got)
	}

	// a2 fires again: a new incident (resolved_at cleared, started_at replaced).
	a2 := alert("a2", connectors.SeverityCritical)
	a2.StartedAt = time.Now().Add(-time.Minute)
	e.script.Set(connectors.Result{ReplaceAlerts: true, Alerts: []connectors.Alert{a1b, a2}})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if got = e.alerts(t, c); got["a2"].ResolvedAt.Valid || time.Since(got["a2"].StartedAt.Time) > 5*time.Minute {
		t.Fatalf("re-fired: %+v", got["a2"])
	}

	// A sync without ReplaceAlerts (webhook type) never resolves anything.
	e.script.Set(connectors.Result{})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if got = e.alerts(t, c); got["a1"].ResolvedAt.Valid || got["a2"].ResolvedAt.Valid {
		t.Fatalf("sync without ReplaceAlerts resolved alerts: %+v", got)
	}

	// An empty replacement resolves everything; resolved alerts older than 30 days are pruned.
	e.script.Set(connectors.Result{ReplaceAlerts: true})
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if got = e.alerts(t, c); !got["a1"].ResolvedAt.Valid || !got["a2"].ResolvedAt.Valid {
		t.Fatalf("empty replace: %+v", got)
	}
	if _, err := e.pool.Exec(ctx, "UPDATE alerts SET resolved_at = now() - interval '31 days' WHERE connector_id = $1 AND external_key = 'a1'", c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Run(ctx, c, connectors.TriggerSchedule); err != nil {
		t.Fatal(err)
	}
	if got = e.alerts(t, c); len(got) != 1 || got["a2"].ExternalKey == "" {
		t.Fatalf("prune: %+v", got)
	}
}

func TestSyncRejectsInvalidAlerts(t *testing.T) {
	e := setup(t, 0)
	c := e.create(t, "bad", 60)
	for name, alerts := range map[string][]connectors.Alert{
		"no key":    {{Severity: connectors.SeverityInfo}},
		"bad sev":   {{ExternalKey: "k", Severity: "loud"}},
		"duplicate": {alert("k", "info"), alert("k", "info")},
	} {
		e.script.Set(connectors.Result{ReplaceAlerts: true, Alerts: alerts})
		if _, err := e.svc.Run(context.Background(), c, connectors.TriggerManual); err == nil {
			t.Errorf("%s: sync must fail", name)
		}
	}
	if len(e.alerts(t, c)) != 0 {
		t.Error("nothing may be stored on a failed sync")
	}
}

func (e *env) webhook(t *testing.T) (dbgen.Connector, string) {
	t.Helper()
	c, secret, err := e.svc.CreateGenerating(context.Background(), e.ws, connectors.CreateInput{Type: alertwebhook.Type, Name: "hook-" + uuid.NewString()[:6], Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return c, secret
}

func TestWebhookConnectorGeneratesSecretOnce(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c, secret := e.webhook(t)
	if len(secret) < 32 || !c.SecretID.Valid {
		t.Fatalf("secret len %d, linked %v", len(secret), c.SecretID.Valid)
	}
	res, err := e.sec.Resolve(ctx, e.ws, c.SecretID.Bytes)
	if err != nil || string(res.Value.Reveal()) != secret {
		t.Fatalf("stored secret differs: %v", err)
	}
	// Other types never generate; an explicit secret is used as given.
	f, plain, err := e.svc.CreateGenerating(ctx, e.ws, connectors.CreateInput{Type: "static", Name: "plain", Enabled: true})
	if err == nil && (plain != "" || f.SecretID.Valid) {
		t.Error("static type must not generate a secret")
	}
	own, err := e.sec.Create(ctx, e.ws, secrets.CreateInput{Name: "own-hook", Type: secrets.TypeToken, Value: []byte("my-own-hook-secret")})
	if err != nil {
		t.Fatal(err)
	}
	sid := own.ID
	c2, plain, err := e.svc.CreateGenerating(ctx, e.ws, connectors.CreateInput{Type: alertwebhook.Type, Name: "own", SecretID: &sid, Enabled: true})
	if err != nil || plain != "" || c2.SecretID.Bytes != sid {
		t.Errorf("explicit secret: %v plain=%q", err, plain)
	}
	// The sync loop leaves webhook alerts alone.
	if _, err := e.svc.Run(ctx, c, connectors.TriggerManual); err != nil {
		t.Fatal(err)
	}
}

func TestHandleAlertWebhook(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	c, secret := e.webhook(t)
	bearer := "Bearer " + secret
	fire := `{"alerts":[{"key":"k1","host":"web1.example.test","severity":"critical","summary":"down"},{"key":"k2","ip":"192.0.2.9","severity":"warning"}]}`

	// Every rejection is the same error and stores nothing.
	other, _ := e.webhook(t)
	disabled, dsecret := e.webhook(t)
	off := false
	if _, err := e.svc.Update(ctx, e.ws, disabled.ID, connectors.UpdateInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	sync := e.create(t, "notahook", 60)
	rejected := map[string]struct {
		id   uuid.UUID
		auth string
	}{
		"unknown connector": {uuid.New(), bearer},
		"no header":         {c.ID, ""},
		"wrong scheme":      {c.ID, "Basic " + secret},
		"wrong secret":      {c.ID, "Bearer " + secret + "x"},
		"foreign secret":    {other.ID, bearer},
		"disabled":          {disabled.ID, "Bearer " + dsecret},
		"wrong type":        {sync.ID, bearer},
	}
	for name, r := range rejected {
		if _, n, err := e.svc.HandleAlertWebhook(ctx, r.id, r.auth, []byte(fire)); !errors.Is(err, connectors.ErrWebhookRejected) || n != 0 {
			t.Errorf("%s: n=%d err=%v", name, n, err)
		}
	}
	if len(e.alerts(t, c)) != 0 {
		t.Fatal("rejected deliveries must store nothing")
	}

	ws, n, err := e.svc.HandleAlertWebhook(ctx, c.ID, bearer, []byte(fire))
	if err != nil || n != 2 || ws != e.ws {
		t.Fatalf("n=%d ws=%v err=%v", n, ws, err)
	}
	if e.linked.Load() != 1 {
		t.Errorf("link calls %d", e.linked.Load())
	}
	got := e.alerts(t, c)
	if len(got) != 2 || got["k1"].ResolvedAt.Valid || got["k1"].Severity != "critical" {
		t.Fatalf("stored: %+v", got)
	}

	// Individual resolve; the other alert stays open.
	if _, n, err = e.svc.HandleAlertWebhook(ctx, c.ID, bearer, []byte(`{"alerts":[{"key":"k1","status":"resolved"},{"key":"unknown","status":"resolved"}]}`)); err != nil || n != 2 {
		t.Fatalf("resolve: n=%d err=%v", n, err)
	}
	got = e.alerts(t, c)
	if !got["k1"].ResolvedAt.Valid || got["k2"].ResolvedAt.Valid || len(got) != 2 {
		t.Fatalf("after resolve: %+v", got)
	}

	// Re-firing reopens; Alertmanager payloads work; a bad payload is reported after auth.
	am := `{"version":"4","alerts":[{"status":"firing","labels":{"alertname":"X","severity":"critical","instance":"192.0.2.9:9100"},"fingerprint":"fp1"}]}`
	if _, _, err = e.svc.HandleAlertWebhook(ctx, c.ID, bearer, []byte(`{"alerts":[{"key":"k1","severity":"info"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.svc.HandleAlertWebhook(ctx, c.ID, bearer, []byte(am)); err != nil {
		t.Fatal(err)
	}
	got = e.alerts(t, c)
	if got["k1"].ResolvedAt.Valid || got["fp1"].Severity != "critical" {
		t.Fatalf("after refire: %+v", got)
	}
	if _, _, err = e.svc.HandleAlertWebhook(ctx, c.ID, bearer, []byte(`{"alerts":[{"key":"ok","severity":"info"},{"severity":"info"}]}`)); !errors.Is(err, connectors.ErrWebhookPayload) {
		t.Errorf("bad payload: %v", err)
	}
	if _, ok := e.alerts(t, c)["ok"]; ok {
		t.Error("a delivery with an invalid alert must be applied atomically (not at all)")
	}
}

func TestDeleteConnectorSecretPolicy(t *testing.T) {
	e := setup(t, 0)
	ctx := context.Background()
	// Generated secret goes away with the connector (default Delete).
	c, _ := e.webhook(t)
	sid := c.SecretID.Bytes
	if err := e.svc.Delete(ctx, e.ws, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Get(ctx, e.ws, sid); err == nil {
		t.Error("generated secret must be deleted with the connector")
	}
	// An explicit secret survives the default delete.
	own, err := e.sec.Create(ctx, e.ws, secrets.CreateInput{Name: "own-keep", Type: secrets.TypeToken, Value: []byte("my-own-hook-secret")})
	if err != nil {
		t.Fatal(err)
	}
	id := own.ID
	mk := func(name string) connectors.CreateInput {
		return connectors.CreateInput{Type: alertwebhook.Type, Name: name, SecretID: &id, Enabled: true}
	}
	a, _, err := e.svc.CreateGenerating(ctx, e.ws, mk("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := e.svc.CreateGenerating(ctx, e.ws, mk("b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.ws, a.ID); err != nil {
		t.Fatal(err)
	}
	// Still referenced by b: kept even with DeleteSecretUnused.
	if del, err := e.svc.DeleteConnector(ctx, e.ws, b.ID, connectors.KeepSecret); err != nil || del {
		t.Fatalf("keep: deleted=%v err=%v", del, err)
	}
	c3, _, err := e.svc.CreateGenerating(ctx, e.ws, mk("c"))
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := e.svc.CreateGenerating(ctx, e.ws, mk("d"))
	if err != nil {
		t.Fatal(err)
	}
	if del, err := e.svc.DeleteConnector(ctx, e.ws, c3.ID, connectors.DeleteSecretUnused); err != nil || del {
		t.Fatalf("shared secret must stay: deleted=%v err=%v", del, err)
	}
	if del, err := e.svc.DeleteConnector(ctx, e.ws, d.ID, connectors.DeleteSecretUnused); err != nil || !del {
		t.Fatalf("unused secret must go: deleted=%v err=%v", del, err)
	}
}
