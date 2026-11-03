// Package config parses configuration from ACP_* environment variables.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration.
type Config struct {
	HTTPAddr      string
	HTTPSAddr     string
	TLSCertFile   string
	TLSKeyFile    string
	DatabaseURL   string
	LogLevel      string
	AppURL        string // worker: base URL of the app
	WorkerAddr    string // worker: health listen address
	WorkerPoll    time.Duration
	MasterKeyFile string
	ShutdownGrace time.Duration
	// PublicURL is the external base URL users reach the app on
	// (e.g. https://acp.example.com:8443). Used for HTTPS redirects and OIDC.
	PublicURL string
}

// TLSEnabled reports whether both certificate and key are configured.
func (c Config) TLSEnabled() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

// Load reads configuration using the given lookup function (e.g. os.LookupEnv).
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(k, def string) string {
		if v, ok := lookup("ACP_" + k); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	c := Config{
		HTTPAddr:      get("HTTP_ADDR", ":8080"),
		HTTPSAddr:     get("HTTPS_ADDR", ":8443"),
		TLSCertFile:   get("TLS_CERT_FILE", ""),
		TLSKeyFile:    get("TLS_KEY_FILE", ""),
		DatabaseURL:   get("DATABASE_URL", ""),
		LogLevel:      strings.ToLower(get("LOG_LEVEL", "info")),
		AppURL:        get("APP_URL", "http://app:8080"),
		WorkerAddr:    get("WORKER_ADDR", ":8081"),
		MasterKeyFile: get("MASTER_KEY_FILE", ""),
		PublicURL:     strings.TrimRight(get("PUBLIC_URL", ""), "/"),
	}
	var err error
	if c.WorkerPoll, err = parseDur(get("WORKER_POLL_INTERVAL", "15s")); err != nil {
		return c, fmt.Errorf("ACP_WORKER_POLL_INTERVAL: %w", err)
	}
	if c.ShutdownGrace, err = parseDur(get("SHUTDOWN_GRACE", "15s")); err != nil {
		return c, fmt.Errorf("ACP_SHUTDOWN_GRACE: %w", err)
	}
	return c, c.validate()
}

func parseDur(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}

func (c Config) validate() error {
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("ACP_LOG_LEVEL: invalid level %q", c.LogLevel)
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return fmt.Errorf("ACP_PUBLIC_URL: must be an absolute http(s) URL without path, query or credentials")
		}
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("ACP_TLS_CERT_FILE and ACP_TLS_KEY_FILE must be set together")
	}
	for _, a := range []string{c.HTTPAddr, c.HTTPSAddr, c.WorkerAddr} {
		if i := strings.LastIndex(a, ":"); i < 0 {
			return fmt.Errorf("invalid listen address %q", a)
		} else if _, err := strconv.Atoi(a[i+1:]); err != nil {
			return fmt.Errorf("invalid listen address %q", a)
		}
	}
	return nil
}
