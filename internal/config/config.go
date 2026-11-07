// Package config parses configuration from ACP_* environment variables.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config is the runtime configuration.
type Config struct {
	HTTPAddr    string
	HTTPSAddr   string
	TLSCertFile string
	TLSKeyFile  string
	DatabaseURL string
	LogLevel    string
	AppURL      string // worker: base URL of the app
	WorkerAddr  string // worker: health listen address
	// WorkerAPIAddr is the app's internal-only listener for the worker API
	// (plain HTTP, never published, ADR-0016).
	WorkerAPIAddr string
	// RunLogRetentionDays deletes log chunks of finished runs older than this
	// many days; 0 keeps them forever.
	RunLogRetentionDays int
	WorkerPoll          time.Duration
	// WorkerTokenFile holds the shared bearer token of the internal worker API
	// (app and worker). Empty on the app disables the API.
	WorkerTokenFile   string
	WorkerConcurrency int
	WorkerCacheDir    string
	// WorkerJobUIDBase is the first uid of the per-slot job users (ADR-0020);
	// 0 runs jobs under the worker uid (development only).
	WorkerJobUIDBase int
	MasterKeyFile    string
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
	// AllowInsecureHTTP lets `serve` run with a plain http:// ACP_PUBLIC_URL
	// on a non-loopback host (cookies then travel unencrypted).
	AllowInsecureHTTP bool
	// MetricsToken, when set, exposes /metrics on the public listeners behind
	// a bearer token. /metrics is always served on the internal worker API listener.
	MetricsToken string
}

// CheckServe validates settings that only matter for the serve subcommand.
func (c Config) CheckServe() error {
	if c.AllowInsecureHTTP || c.PublicURL == "" {
		return nil
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "http" {
		return nil
	}
	switch h := u.Hostname(); {
	case h == "localhost", strings.HasSuffix(h, ".localhost"):
		return nil
	default:
		if ip, err := netip.ParseAddr(h); err == nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("ACP_PUBLIC_URL uses plain http on a non-loopback host; session cookies and credentials would travel unencrypted. " +
		"Use https (ACP_TLS_*) or a TLS-terminating proxy, or set ACP_ALLOW_INSECURE_HTTP=true to accept the risk")
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
		HTTPAddr:        get("HTTP_ADDR", ":8080"),
		HTTPSAddr:       get("HTTPS_ADDR", ":8443"),
		TLSCertFile:     get("TLS_CERT_FILE", ""),
		TLSKeyFile:      get("TLS_KEY_FILE", ""),
		DatabaseURL:     get("DATABASE_URL", ""),
		LogLevel:        strings.ToLower(get("LOG_LEVEL", "info")),
		AppURL:          get("APP_URL", "http://app:8080"),
		WorkerAddr:      get("WORKER_ADDR", ":8081"),
		WorkerAPIAddr:   get("WORKER_API_ADDR", ":8082"),
		MasterKeyFile:   keyFile,
		WorkerTokenFile: get("WORKER_TOKEN_FILE", ""),
		WorkerCacheDir:  get("WORKER_CACHE_DIR", "/var/cache/acp"),
		DataDir:         strings.TrimRight(get("DATA_DIR", DefaultDataDir), "/"),
		PublicURL:       strings.TrimRight(get("PUBLIC_URL", ""), "/"),

		OIDCIssuer:     strings.TrimRight(get("OIDC_ISSUER", ""), "/"),
		OIDCClientID:   get("OIDC_CLIENT_ID", ""),
		OIDCScopes:     strings.Fields(get("OIDC_SCOPES", "openid profile email")),
		OIDCRolesClaim: get("OIDC_ROLES_CLAIM", "urn:zitadel:iam:org:project:roles"),
	}
	var err error
	if c.BreakGlassEnabled, err = parseBool(get("BREAKGLASS_ENABLED", "true")); err != nil {
		return c, fmt.Errorf("ACP_BREAKGLASS_ENABLED: %w", err)
	}
	if c.AllowInsecureHTTP, err = parseBool(get("ALLOW_INSECURE_HTTP", "false")); err != nil {
		return c, fmt.Errorf("ACP_ALLOW_INSECURE_HTTP: %w", err)
	}
	c.MetricsToken = get("METRICS_TOKEN", "")
	if c.WorkerPoll, err = parseDur(get("WORKER_POLL_INTERVAL", "15s")); err != nil {
		return c, fmt.Errorf("ACP_WORKER_POLL_INTERVAL: %w", err)
	}
	if c.WorkerConcurrency, err = parseInt(get("WORKER_CONCURRENCY", "2"), 1, 16); err != nil {
		return c, fmt.Errorf("ACP_WORKER_CONCURRENCY: %w", err)
	}
	if c.WorkerJobUIDBase, err = parseInt(get("WORKER_JOB_UID_BASE", "0"), 0, 60000); err != nil {
		return c, fmt.Errorf("ACP_WORKER_JOB_UID_BASE: %w", err)
	}
	if c.WorkerJobUIDBase != 0 && c.WorkerJobUIDBase < 1000 {
		return c, errors.New("ACP_WORKER_JOB_UID_BASE must be 0 or at least 1000")
	}
	if c.RunLogRetentionDays, err = parseInt(get("RUN_LOG_RETENTION_DAYS", "90"), 0, 36500); err != nil {
		return c, fmt.Errorf("ACP_RUN_LOG_RETENTION_DAYS: %w", err)
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

func parseInt(s string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("must be an integer between %d and %d", lo, hi)
	}
	return n, nil
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
	for _, a := range []string{c.HTTPAddr, c.HTTPSAddr, c.WorkerAddr, c.WorkerAPIAddr} {
		if i := strings.LastIndex(a, ":"); i < 0 {
			return fmt.Errorf("invalid listen address %q", a)
		} else if _, err := strconv.Atoi(a[i+1:]); err != nil {
			return fmt.Errorf("invalid listen address %q", a)
		}
	}
	return nil
}
