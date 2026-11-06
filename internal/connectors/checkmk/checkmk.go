// Package checkmk is the read-only Checkmk connector (ADR-0009, ADR-0019). It
// reads host and service problems through the Checkmk REST API (GET only) and
// reports them as alerts. Every sync replaces the connector's open alert set.
package checkmk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/httpx"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Type is the registered connector type name.
const Type = "checkmk"

// DefaultTimeout is the per-request timeout.
const DefaultTimeout = 40 * time.Second

// apiSuffix is appended to a site URL that does not already contain the REST
// API path.
const apiSuffix = "/check_mk/api/1.0"

// Spec returns the registry spec of the type.
func Spec() connectors.Spec {
	return connectors.Spec{
		Type: Type, Label: "Checkmk",
		SecretTypes: []string{secrets.TypeAPIKeyPair}, SecretRequired: true, BaseURL: true,
		Fields: connectors.HTTPFields(),
		Help: "Base URL: the REST API root https://host/<site>/check_mk/api/1.0 (the site URL https://host/<site> " +
			"is accepted, the API path is appended). Secret (api_key_pair): key = automation user name, secret = " +
			"its automation secret (sent as \"Authorization: Bearer <user> <secret>\"). Use an automation user with " +
			"a read-only role (for example the built-in \"guest\" role) that may see the hosts and services.",
	}
}

// Register adds the Checkmk type to reg.
func Register(reg *connectors.Registry) { reg.Register(Spec(), New) }

type conn struct {
	hc *httpx.Client
}

// apiBase returns the REST API root for a site or API URL.
func apiBase(raw string) string {
	raw = strings.TrimRight(raw, "/")
	if strings.Contains(raw, "/check_mk/api/") {
		return raw
	}
	return raw + apiSuffix
}

// New builds a connector from an instance and its api_key_pair credential.
func New(inst connectors.Instance, cred connectors.Credential) (connectors.Connector, error) {
	if cred.Type != secrets.TypeAPIKeyPair {
		return nil, errors.New("checkmk: an api_key_pair secret is required")
	}
	var kp struct{ Key, Secret string }
	if err := json.Unmarshal(cred.Value.Reveal(), &kp); err != nil || kp.Key == "" || kp.Secret == "" {
		return nil, errors.New("checkmk: invalid api_key_pair secret")
	}
	if strings.ContainsAny(kp.Key, " \r\n") || strings.ContainsAny(kp.Secret, " \r\n") {
		return nil, errors.New("checkmk: user and secret must not contain whitespace")
	}
	inst.BaseURL = apiBase(inst.BaseURL)
	o := inst.HTTPOptions(httpx.Auth{HeaderName: "Authorization", HeaderV: "Bearer " + kp.Key + " " + kp.Secret})
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	hc, err := httpx.New(o)
	if err != nil {
		return nil, fmt.Errorf("checkmk: %w", err)
	}
	return &conn{hc: hc}, nil
}

func (*conn) Type() string { return Type }

// Test probes /version and requires the version block of a Checkmk site.
func (c *conn) Test(ctx context.Context) error {
	var v struct {
		Site     string         `json:"site"`
		Versions map[string]any `json:"versions"`
	}
	if err := c.get(ctx, "version", nil, &v); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if v.Site == "" && len(v.Versions) == 0 {
		return errors.New("version: no site or versions in response (not a Checkmk API?)")
	}
	return nil
}

// get fetches an endpoint and retries once on transport errors and 5xx.
func (c *conn) get(ctx context.Context, endpoint string, q url.Values, out any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.hc.GetJSON(ctx, endpoint, q, out)
		if err == nil || ctx.Err() != nil {
			return err
		}
		var se *httpx.StatusError
		if errors.As(err, &se) && se.Code < 500 {
			return err
		}
		if strings.HasPrefix(err.Error(), "decode response") {
			return err
		}
	}
	return err
}

// problemQuery is the Livestatus filter for "state != 0".
const problemQuery = `{"op": "!=", "left": "state", "right": "0"}`

type collection struct {
	Value []struct {
		Extensions problemExt `json:"extensions"`
	} `json:"value"`
}

// problemExt holds the Livestatus columns of host and service problems.
type problemExt struct {
	Name            string  `json:"name"`      // host
	HostName        string  `json:"host_name"` // service
	Address         string  `json:"address"`
	HostAddress     string  `json:"host_address"`
	Description     string  `json:"description"`
	State           *int    `json:"state"`
	PluginOutput    string  `json:"plugin_output"`
	LastStateChange float64 `json:"last_state_change"`
	Acknowledged    int     `json:"acknowledged"`
	Downtime        int     `json:"scheduled_downtime_depth"`
}

func columns(cols ...string) url.Values {
	return url.Values{"columns": cols, "query": {problemQuery}}
}

// Sync reads the current host and service problems. A failing request fails
// the whole sync: the alert set is replaced, so a partial result would
// wrongly resolve alerts.
func (c *conn) Sync(ctx context.Context) (connectors.Result, error) {
	res := connectors.Result{ReplaceAlerts: true}
	var hosts, services collection
	if err := c.get(ctx, "domain-types/host/collections/all", columns("name", "address", "state", "plugin_output",
		"last_state_change", "acknowledged", "scheduled_downtime_depth"), &hosts); err != nil {
		return connectors.Result{}, fmt.Errorf("host problems: %w", err)
	}
	if err := c.get(ctx, "domain-types/service/collections/all", columns("host_name", "host_address", "description", "state",
		"plugin_output", "last_state_change", "acknowledged", "scheduled_downtime_depth"), &services); err != nil {
		return connectors.Result{}, fmt.Errorf("service problems: %w", err)
	}
	for _, h := range hosts.Value {
		if a, ok := hostAlert(h.Extensions); ok {
			res.Alerts = append(res.Alerts, a)
		}
	}
	for _, s := range services.Value {
		if a, ok := serviceAlert(s.Extensions); ok {
			res.Alerts = append(res.Alerts, a)
		}
	}
	return res, nil
}

func stateTime(unix float64) time.Time {
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(unix), 0).UTC()
}

func labels(e problemExt, state string) map[string]string {
	return map[string]string{
		"state":        state,
		"acknowledged": strconv.FormatBool(e.Acknowledged > 0),
		"in_downtime":  strconv.FormatBool(e.Downtime > 0),
	}
}

// hostState maps a Checkmk host state: DOWN and UNREACH are critical.
func hostState(s int) (string, string, bool) {
	switch s {
	case 0:
		return "", "", false
	case 1:
		return "DOWN", connectors.SeverityCritical, true
	case 2:
		return "UNREACH", connectors.SeverityCritical, true
	}
	return "UNKNOWN", connectors.SeverityUnknown, true
}

// serviceState maps a Checkmk service state.
func serviceState(s int) (string, string, bool) {
	switch s {
	case 0:
		return "", "", false
	case 1:
		return "WARN", connectors.SeverityWarning, true
	case 2:
		return "CRIT", connectors.SeverityCritical, true
	}
	return "UNKNOWN", connectors.SeverityUnknown, true
}

func hostAlert(e problemExt) (connectors.Alert, bool) {
	if e.State == nil || e.Name == "" {
		return connectors.Alert{}, false
	}
	name, sev, ok := hostState(*e.State)
	if !ok {
		return connectors.Alert{}, false
	}
	return connectors.Alert{ExternalKey: "host:" + e.Name, Host: e.Name, IP: e.Address, Severity: sev,
		Summary: summary(name, e.PluginOutput), Labels: labels(e, name), StartedAt: stateTime(e.LastStateChange)}, true
}

func serviceAlert(e problemExt) (connectors.Alert, bool) {
	if e.State == nil || e.HostName == "" || e.Description == "" {
		return connectors.Alert{}, false
	}
	name, sev, ok := serviceState(*e.State)
	if !ok {
		return connectors.Alert{}, false
	}
	return connectors.Alert{ExternalKey: "svc:" + e.HostName + "/" + e.Description, Host: e.HostName, IP: e.HostAddress,
		Service: e.Description, Severity: sev, Summary: summary(name, e.PluginOutput), Labels: labels(e, name),
		StartedAt: stateTime(e.LastStateChange)}, true
}

func summary(state, output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return state
	}
	return state + ": " + output
}
