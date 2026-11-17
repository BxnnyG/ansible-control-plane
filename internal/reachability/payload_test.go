package reachability

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParsePayload(t *testing.T) {
	id := uuid.NewString()
	long := strings.Repeat("x", 500)
	var b strings.Builder
	b.WriteByte('{')
	for i := range MaxResultsPerHost + 1 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + uuid.NewString() + `":{"status":"ok","latency_ms":1,"detail":""}`)
	}
	b.WriteByte('}')

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(t *testing.T, got map[uuid.UUID]CheckResult)
	}{
		{name: "ok", raw: `{"` + id + `":{"status":"ok","latency_ms":12,"detail":""}}`, check: func(t *testing.T, got map[uuid.UUID]CheckResult) {
			r := got[uuid.MustParse(id)]
			if r.Status != StatusOK || r.LatencyMS == nil || *r.LatencyMS != 12 {
				t.Errorf("got %+v", r)
			}
		}},
		{name: "null latency", raw: `{"` + id + `":{"status":"failed","latency_ms":null,"detail":"x"}}`},
		{name: "empty object", raw: `{}`},
		{name: "empty input", raw: ``, wantErr: true},
		{name: "not an object", raw: `[1]`, wantErr: true},
		{name: "unknown field", raw: `{"` + id + `":{"status":"ok","latency_ms":1,"detail":"","x":1}}`, wantErr: true},
		{name: "bad key", raw: `{"nope":{"status":"ok","latency_ms":1,"detail":""}}`, wantErr: true},
		{name: "status probe_unreachable is app-only", raw: `{"` + id + `":{"status":"probe_unreachable"}}`, wantErr: true},
		{name: "unknown status", raw: `{"` + id + `":{"status":"maybe"}}`, wantErr: true},
		{name: "negative latency", raw: `{"` + id + `":{"status":"ok","latency_ms":-1}}`, wantErr: true},
		{name: "huge latency", raw: `{"` + id + `":{"status":"ok","latency_ms":99999999}}`, wantErr: true},
		{name: "wrong type", raw: `{"` + id + `":{"status":"ok","latency_ms":"1"}}`, wantErr: true},
		{name: "trailing data", raw: `{} {}`, wantErr: true},
		{name: "too many results", raw: b.String(), wantErr: true},
		{name: "too large", raw: `{"` + id + `":{"status":"ok","detail":"` + strings.Repeat("a", MaxPayloadBytes) + `"}}`, wantErr: true},
		{name: "detail is cut and cleaned", raw: `{"` + id + `":{"status":"failed","detail":"a\nb\u0000` + long + `"}}`, check: func(t *testing.T, got map[uuid.UUID]CheckResult) {
			d := got[uuid.MustParse(id)].Detail
			if len(d) > MaxDetailBytes || strings.ContainsAny(d, "\n\x00") || !strings.HasPrefix(d, "a b ") {
				t.Errorf("detail %q (%d)", d[:10], len(d))
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePayload([]byte(tc.raw))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrPayload) {
				t.Errorf("error does not wrap ErrPayload: %v", err)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestTargetValidate(t *testing.T) {
	p := func(n int) *int { return &n }
	tests := []struct {
		name    string
		in      TargetInput
		wantErr bool
	}{
		{"icmp", TargetInput{Name: "gw", Address: "192.0.2.1", Check: CheckICMP}, false},
		{"icmp drops port", TargetInput{Name: "gw", Address: "gw.example.test", Check: CheckICMP, Port: p(1), DNSName: "x"}, false},
		{"tcp", TargetInput{Name: "ssh", Address: "2001:db8::1", Check: CheckTCP, Port: p(22)}, false},
		{"tcp without port", TargetInput{Name: "ssh", Address: "h", Check: CheckTCP}, true},
		{"tcp port 0", TargetInput{Name: "ssh", Address: "h", Check: CheckTCP, Port: p(0)}, true},
		{"tcp port 65536", TargetInput{Name: "ssh", Address: "h", Check: CheckTCP, Port: p(65536)}, true},
		{"dns", TargetInput{Name: "d", Address: "192.0.2.53", Check: CheckDNS, DNSName: "www.example.test"}, false},
		{"dns without name", TargetInput{Name: "d", Address: "192.0.2.53", Check: CheckDNS}, true},
		{"unknown check", TargetInput{Name: "d", Address: "h", Check: "http"}, true},
		{"empty name", TargetInput{Name: " ", Address: "h", Check: CheckICMP}, true},
		{"option-like address", TargetInput{Name: "n", Address: "-c1", Check: CheckICMP}, true},
		{"shell metachar address", TargetInput{Name: "n", Address: "a;rm", Check: CheckICMP}, true},
		{"space in address", TargetInput{Name: "n", Address: "a b", Check: CheckICMP}, true},
		{"jinja in address", TargetInput{Name: "n", Address: "{{x}}", Check: CheckICMP}, true},
		{"bad dns name", TargetInput{Name: "n", Address: "h", Check: CheckDNS, DNSName: "a b"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			err := in.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}
			if err == nil && in.Check == CheckICMP && (in.Port != nil || in.DNSName != "") {
				t.Errorf("icmp kept port/dns_name: %+v", in)
			}
		})
	}
}

func TestHostRe(t *testing.T) {
	for h, ok := range map[string]bool{"web1.example.test": true, "a-b_c": true, "a:b": false, "a,b": false, "!x": false, "*": false, "-x": false, "a b": false} {
		if hostRe.MatchString(h) != ok {
			t.Errorf("hostRe(%q) = %v, want %v", h, !ok, ok)
		}
	}
}
