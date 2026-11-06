package connectors

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAlertPayload(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		check   func(t *testing.T, u []AlertUpdate)
	}{
		{name: "acp firing and resolved", body: `{"alerts":[
			{"key":"k1","host":"web1.example.test","ip":"192.0.2.10","service":"disk","severity":"CRITICAL","summary":"full","labels":{"a":"b"},"status":"firing","started_at":"2026-01-02T03:04:05Z"},
			{"key":"k2","status":"resolved"}]}`,
			check: func(t *testing.T, u []AlertUpdate) {
				if len(u) != 2 || u[0].Severity != SeverityCritical || u[0].Resolved || u[0].Host != "web1.example.test" ||
					u[0].StartedAt.Year() != 2026 || u[0].Labels["a"] != "b" || !u[1].Resolved || u[1].Severity != SeverityUnknown {
					t.Errorf("got %+v", u)
				}
			}},
		{name: "acp default status and duplicate key", body: `{"alerts":[{"key":"k","severity":"warning","summary":"a"},{"key":"k","severity":"info","summary":"b"}]}`,
			check: func(t *testing.T, u []AlertUpdate) {
				if len(u) != 1 || u[0].Summary != "b" || u[0].Severity != SeverityInfo {
					t.Errorf("later duplicate must win: %+v", u)
				}
			}},
		{name: "alertmanager v4", body: `{"version":"4","status":"firing","alerts":[
			{"status":"firing","labels":{"alertname":"HighLoad","severity":"warning","instance":"192.0.2.20:9100"},"annotations":{"summary":"load is high","description":"long"},"startsAt":"2026-02-03T04:05:06Z","fingerprint":"abc123"},
			{"status":"resolved","labels":{"alertname":"Down","severity":"page","hostname":"db1.example.test"},"annotations":{"description":"gone"},"startsAt":"0001-01-01T00:00:00Z","fingerprint":"def456"},
			{"status":"firing","labels":{"alertname":"NoFp","instance":"node1.example.test:9100"}}]}`,
			check: func(t *testing.T, u []AlertUpdate) {
				if len(u) != 3 {
					t.Fatalf("got %d", len(u))
				}
				a, b, c := u[0], u[1], u[2]
				if a.ExternalKey != "abc123" || a.IP != "192.0.2.20" || a.Host != "" || a.Service != "HighLoad" || a.Severity != SeverityWarning || a.Summary != "load is high" || a.Resolved {
					t.Errorf("a=%+v", a)
				}
				if b.ExternalKey != "def456" || b.Host != "db1.example.test" || b.Severity != SeverityCritical || b.Summary != "gone" || !b.Resolved || !b.StartedAt.IsZero() {
					t.Errorf("b=%+v", b)
				}
				if len(c.ExternalKey) != 16 || c.Host != "node1.example.test" || c.Severity != SeverityUnknown {
					t.Errorf("c=%+v", c)
				}
			}},
		{name: "empty alerts", body: `{"alerts":[]}`, wantErr: true},
		{name: "not json", body: `nope`, wantErr: true},
		{name: "missing key", body: `{"alerts":[{"severity":"warning"}]}`, wantErr: true},
		{name: "bad severity", body: `{"alerts":[{"key":"k","severity":"loud"}]}`, wantErr: true},
		{name: "bad status", body: `{"alerts":[{"key":"k","status":"maybe"}]}`, wantErr: true},
		{name: "non-string label", body: `{"alerts":[{"key":"k","labels":{"a":1}}]}`, wantErr: true},
		{name: "too many", body: `{"alerts":[` + strings.TrimSuffix(strings.Repeat(`{"key":"k"},`, 1001), ",") + `]}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := ParseAlertPayload([]byte(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrWebhookPayload) {
					t.Errorf("error must wrap ErrWebhookPayload: %v", err)
				}
				return
			}
			tc.check(t, u)
		})
	}
}

func TestHostFromLabels(t *testing.T) {
	tests := []struct {
		labels   map[string]string
		host, ip string
	}{
		{map[string]string{"host": "a.example.test", "instance": "192.0.2.1:9100"}, "a.example.test", ""},
		{map[string]string{"instance": "192.0.2.1:9100"}, "", "192.0.2.1"},
		{map[string]string{"instance": "192.0.2.1"}, "", "192.0.2.1"},
		{map[string]string{"instance": "[2001:db8::1]:9100"}, "", "2001:db8::1"},
		{map[string]string{"instance": "node1:9100"}, "node1", ""},
		{map[string]string{"instance": "node1"}, "node1", ""},
		{map[string]string{}, "", ""},
	}
	for _, tc := range tests {
		if h, ip := hostFromLabels(tc.labels); h != tc.host || ip != tc.ip {
			t.Errorf("%v: got (%q,%q), want (%q,%q)", tc.labels, h, ip, tc.host, tc.ip)
		}
	}
}

func TestBearerAndSecretsEqual(t *testing.T) {
	for in, want := range map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "Bearer": "", "": "", "abc": ""} {
		if got, ok := bearerToken(in); got != want || ok != (want != "") {
			t.Errorf("%q: got (%q,%v)", in, got, ok)
		}
	}
	if !SecretsEqual([]byte("x"), []byte("x")) || SecretsEqual([]byte("x"), []byte("y")) || SecretsEqual([]byte("x"), nil) {
		t.Error("SecretsEqual")
	}
}

func TestNormalizeAlertBounds(t *testing.T) {
	long := strings.Repeat("é", 2000)
	a, err := normalizeAlert(Alert{ExternalKey: " k ", Severity: SeverityInfo, Summary: long, Host: long, Labels: map[string]string{long: long}})
	if err != nil || a.ExternalKey != "k" || len(a.Summary) > maxAlertText || len(a.Host) > maxAlertShort {
		t.Fatalf("%v %+v", err, a)
	}
	for k, v := range a.Labels {
		if len(k) > maxAlertLabelKV || len(v) > maxAlertLabelKV {
			t.Error("label not bounded")
		}
	}
	if _, err := normalizeAlert(Alert{ExternalKey: "k", Severity: "x"}); err == nil {
		t.Error("severity must be validated")
	}
}
