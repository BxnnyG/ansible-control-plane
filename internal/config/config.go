// Package config parses configuration from ACP_* environment variables.
package config

import (
	"fmt"
	"net/url"
	"slices"
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
	// DataDir holds app state outside the database, e.g. Git clones in <DataDir>/projects.
	DataDir       string
	ShutdownGrace time.Duration
	// PublicURL is the external base URL users reach the app on
	// (e.g. https://acp.example.com:8443). Used for HTTPS redirects and OIDC.
	PublicURL string

	// OIDC (ADR-0006). Enabled when both OIDCIssuer and OIDCClientID are set.
	OIDCIssuer     string
	OIDCClientID   string
	OIDCScopes     []string
	OIDCRolesClaim string
	// BreakGlassEnabled turns the local break-glass login on (default) or off.
	BreakGlassEnabled bool
	SessionIdle       time.Duration
	SessionAbsolute   time.Duration
}

// OIDCEnabled reports whether OIDC login is configured.
func (c Config) OIDCEnabled() bool { return c.OIDCIssuer != "" && c.OIDCClientID != "" }

// TLSEnabled reports whether both certificate and key are configured.
func (c Config) TLSEnabled() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

// DefaultMasterKeyFile is the in-container path of the compose master key secret.
const DefaultMasterKeyFile = "/run/secrets/master_key"

// DefaultDataDir is the in-container data volume of the app.
const DefaultDataDir = "/var/lib/acp"

// Load reads configuration using the given lookup function (e.g. os.LookupEnv).
func Load(lookup func(string) (string, bool)) (Config, error) {
	get := func(k, def string) string {
		if v, ok := lookup("ACP_" + k); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	// The file default matches the compose secret; an explicit ACP_MASTER_KEY
	// (without a file) switches to the environment fallback.
	keyFile := get("MASTER_KEY_FILE", "")
	if v, _ := lookup("ACP_MASTER_KEY"); keyFile == "" && strings.TrimSpace(v) == "" {
		keyFile = DefaultMasterKeyFile
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
		MasterKeyFile: keyFile,
		DataDir:       strings.TrimRight(get("DATA_DIR", DefaultDataDir), "/"),
		PublicURL:     strings.TrimRight(get("PUBLIC_URL", ""), "/"),

		OIDCIssuer:     strings.TrimRight(get("OIDC_ISSUER", ""), "/"),
		OIDCClientID:   get("OIDC_CLIENT_ID", ""),
		OIDCScopes:     strings.Fields(get("OIDC_SCOPES", "openid profile email")),
		OIDCRolesClaim: get("OIDC_ROLES_CLAIM", "urn:zitadel:iam:org:project:roles"),
	}
	var err error
	if c.BreakGlassEnabled, err = parseBool(get("BREAKGLASS_ENABLED", "true")); err != nil {
		return c, fmt.Errorf("ACP_BREAKGLASS_ENABLED: %w", err)
	}
	if c.WorkerPoll, err = parseDur(get("WORKER_POLL_INTERVAL", "15s")); err != nil {
		return c, fmt.Errorf("ACP_WORKER_POLL_INTERVAL: %w", err)
	}
	if c.ShutdownGrace, err = parseDur(get("SHUTDOWN_GRACE", "15s")); err != nil {
		return c, fmt.Errorf("ACP_SHUTDOWN_GRACE: %w", err)
	}
	if c.SessionIdle, err = parseDur(get("SESSION_IDLE_TIMEOUT", "8h")); err != nil {
		return c, fmt.Errorf("ACP_SESSION_IDLE_TIMEOUT: %w", err)
	}
	if c.SessionAbsolute, err = parseDur(get("SESSION_ABSOLUTE_TIMEOUT", "24h")); err != nil {
		return c, fmt.Errorf("ACP_SESSION_ABSOLUTE_TIMEOUT: %w", err)
	}
	return c, c.validate()
}

func parseBool(s string) (bool, error) {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("must be true or false")
	}
	return b, nil
}

// AuthEnabled reports whether any login method is configured.
func (c Config) AuthEnabled() bool { return c.OIDCEnabled() || c.BreakGlassEnabled }

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
	if (c.OIDCIssuer == "") != (c.OIDCClientID == "") {
		return fmt.Errorf("ACP_OIDC_ISSUER and ACP_OIDC_CLIENT_ID must be set together")
	}
	if c.OIDCEnabled() {
		if c.PublicURL == "" {
			return fmt.Errorf("ACP_PUBLIC_URL is required when OIDC is enabled")
		}
		if u, err := url.Parse(c.OIDCIssuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("ACP_OIDC_ISSUER: must be an absolute http(s) URL")
		}
		if !slices.Contains(c.OIDCScopes, "openid") {
			return fmt.Errorf("ACP_OIDC_SCOPES: must include openid")
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
