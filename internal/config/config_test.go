package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(*testing.T, Config)
	}{
		{"defaults", nil, false, func(t *testing.T, c Config) {
			if c.HTTPAddr != ":8080" || c.HTTPSAddr != ":8443" || c.LogLevel != "info" || c.TLSEnabled() {
				t.Errorf("unexpected defaults: %+v", c)
			}
			if c.WorkerPoll != 15*time.Second {
				t.Errorf("poll = %v", c.WorkerPoll)
			}
		}},
		{"overrides", map[string]string{"ACP_HTTP_ADDR": ":9000", "ACP_LOG_LEVEL": "DEBUG", "ACP_WORKER_POLL_INTERVAL": "2s"}, false,
			func(t *testing.T, c Config) {
				if c.HTTPAddr != ":9000" || c.LogLevel != "debug" || c.WorkerPoll != 2*time.Second {
					t.Errorf("got %+v", c)
				}
			}},
		{"tls both", map[string]string{"ACP_TLS_CERT_FILE": "/a", "ACP_TLS_KEY_FILE": "/b"}, false,
			func(t *testing.T, c Config) {
				if !c.TLSEnabled() {
					t.Error("tls should be enabled")
				}
			}},
		{"tls only cert", map[string]string{"ACP_TLS_CERT_FILE": "/a"}, true, nil},
		{"bad level", map[string]string{"ACP_LOG_LEVEL": "loud"}, true, nil},
		{"bad duration", map[string]string{"ACP_WORKER_POLL_INTERVAL": "x"}, true, nil},
		{"negative duration", map[string]string{"ACP_SHUTDOWN_GRACE": "-1s"}, true, nil},
		{"bad addr", map[string]string{"ACP_HTTP_ADDR": "8080"}, true, nil},
		{"blank is default", map[string]string{"ACP_HTTP_ADDR": "  "}, false,
			func(t *testing.T, c Config) {
				if c.HTTPAddr != ":8080" {
					t.Errorf("got %q", c.HTTPAddr)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(env(tt.env))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
