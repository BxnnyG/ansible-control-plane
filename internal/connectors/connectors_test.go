package connectors

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testRegistry() *Registry {
	r := NewRegistry()
	r.Register(Spec{
		Type: "http", Label: "HTTP", BaseURL: true, SecretTypes: []string{"api_token"}, SecretRequired: true,
		Fields: append([]Field{{Key: "site", Label: "Site", Kind: FieldString, Required: true}}, HTTPFields()...),
	}, nil2)
	r.Register(Spec{Type: "plain", Label: "Plain"}, nil2)
	return r
}

func nil2(Instance, Credential) (Connector, error) { return nil, errors.New("unused") }

func TestRegistryValidate(t *testing.T) {
	r := testRegistry()
	ok := map[string]any{"site": "a"}
	tests := []struct {
		name    string
		typ     string
		url     string
		cfg     map[string]any
		secret  string
		hasSec  bool
		wantErr string
	}{
		{"valid", "http", "https://fw.example.test", ok, "api_token", true, ""},
		{"valid tls opts", "http", "http://fw.example.test:8080/p", map[string]any{"site": "a", "tls_skip_verify": true, "timeout_seconds": 5}, "api_token", true, ""},
		{"unknown type", "nope", "", nil, "", false, "unknown connector type"},
		{"missing url", "http", "", ok, "api_token", true, "base URL is required"},
		{"bad url", "http", "ftp://x", ok, "api_token", true, "base URL"},
		{"url not allowed", "plain", "https://x", nil, "", false, "takes no base URL"},
		{"unknown option", "http", "https://x", map[string]any{"site": "a", "zzz": 1}, "api_token", true, "unknown option"},
		{"wrong kind", "http", "https://x", map[string]any{"site": 1}, "api_token", true, "must be of kind"},
		{"required option", "http", "https://x", map[string]any{}, "api_token", true, "is required"},
		{"bad CA", "http", "https://x", map[string]any{"site": "a", "ca_pem": "junk"}, "api_token", true, "CA PEM"},
		{"wrong secret type", "http", "https://x", ok, "password", true, "not accepted"},
		{"secret missing", "http", "https://x", ok, "", false, "secret is required"},
		{"secret not taken", "plain", "", nil, "api_token", true, "takes no secret"},
	}
	for _, tc := range tests {
		err := r.Validate(tc.typ, tc.url, tc.cfg, tc.secret, tc.hasSec)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err=%v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	r := testRegistry()
	r.Register(Spec{Type: "plain"}, nil2)
}

func TestBackoffDelay(t *testing.T) {
	m := time.Minute
	tests := []struct {
		interval time.Duration
		failures int
		want     time.Duration
	}{
		{5 * m, 0, 5 * m},
		{5 * m, 1, 10 * m},
		{5 * m, 2, 20 * m},
		{5 * m, 3, 40 * m},
		{5 * m, 4, time.Hour},
		{5 * m, 50, time.Hour},
		{2 * time.Hour, 3, 2 * time.Hour}, // never below the interval
	}
	for _, tc := range tests {
		if got := BackoffDelay(tc.interval, tc.failures); got != tc.want {
			t.Errorf("BackoffDelay(%s,%d)=%s want %s", tc.interval, tc.failures, got, tc.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	got := sanitize(errors.New("login failed for tok-123456\n  with\ttok-123456"), []string{"tok-123456", "ab"})
	if strings.Contains(got, "tok-123456") || strings.ContainsAny(got, "\n\t") {
		t.Errorf("not sanitized: %q", got)
	}
	if n := len(sanitize(errors.New(strings.Repeat("x", 1000)), nil)); n > maxErrorLen+3 {
		t.Errorf("error not truncated: %d", n)
	}
}

func TestValidateResult(t *testing.T) {
	if _, err := validate(Result{Observations: []HostObservation{{ExternalKey: "a", Kind: "bogus"}}}); err == nil {
		t.Error("invalid kind accepted")
	}
	if _, err := validate(Result{Observations: []HostObservation{{ExternalKey: "a", Kind: KindVM}, {ExternalKey: "a", Kind: KindVM}}}); err == nil {
		t.Error("duplicate key accepted")
	}
	if _, err := validate(Result{Networks: []Network{{ExternalKey: "n"}}}); err == nil {
		t.Error("invalid CIDR accepted")
	}
}
