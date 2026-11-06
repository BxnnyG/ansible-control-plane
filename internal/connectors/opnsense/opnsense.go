// Package opnsense is the read-only OPNsense connector (ADR-0009, ADR-0018).
// It reads networks (interfaces, VLANs) and host observations (DHCP leases,
// static mappings and reservations, ARP and NDP) through the GET API.
package opnsense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/httpx"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Type is the registered connector type name.
const Type = "opnsense"

// DefaultTimeout is the per-request timeout; OPNsense answers slowly.
const DefaultTimeout = 40 * time.Second

// DHCP backends.
const (
	BackendKea     = "kea"
	BackendDnsmasq = "dnsmasq"
	BackendISC     = "isc"
	BackendNone    = "none"
)

// Spec returns the registry spec of the type.
func Spec() connectors.Spec {
	return connectors.Spec{
		Type: Type, Label: "OPNsense",
		SecretTypes: []string{secrets.TypeAPIKeyPair}, SecretRequired: true, BaseURL: true,
		Fields: connectors.HTTPFields(),
	}
}

// Register adds the OPNsense type to reg.
func Register(reg *connectors.Registry) { reg.Register(Spec(), New) }

type conn struct {
	hc *httpx.Client
}

// New builds a connector from an instance and its api_key_pair credential.
func New(inst connectors.Instance, cred connectors.Credential) (connectors.Connector, error) {
	if cred.Type != secrets.TypeAPIKeyPair {
		return nil, errors.New("opnsense: an api_key_pair secret is required")
	}
	var kp struct{ Key, Secret string }
	if err := json.Unmarshal(cred.Value.Reveal(), &kp); err != nil || kp.Key == "" || kp.Secret == "" {
		return nil, errors.New("opnsense: invalid api_key_pair secret")
	}
	o := inst.HTTPOptions(httpx.Auth{BasicUser: kp.Key, BasicPwd: kp.Secret})
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	hc, err := httpx.New(o)
	if err != nil {
		return nil, fmt.Errorf("opnsense: %w", err)
	}
	return &conn{hc: hc}, nil
}

func (*conn) Type() string { return Type }

// Test probes the firmware status and requires a product version.
func (c *conn) Test(ctx context.Context) error {
	_, err := c.version(ctx)
	return err
}

func (c *conn) version(ctx context.Context) (string, error) {
	var fw struct {
		ProductVersion string `json:"product_version"`
	}
	if err := c.get(ctx, "core/firmware/status", &fw); err != nil {
		return "", fmt.Errorf("firmware status: %w", err)
	}
	if fw.ProductVersion == "" {
		return "", errors.New("firmware status: no product_version in response (not an OPNsense API?)")
	}
	return fw.ProductVersion, nil
}

// get fetches an API endpoint and retries once on transport errors and 5xx.
func (c *conn) get(ctx context.Context, endpoint string, out any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.hc.GetJSON(ctx, "api/"+endpoint, nil, out)
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

func statusCode(err error) int {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// optional runs an optional fetch. 403/404 become a warning (partial sync);
// 401 and every other error fail the sync.
func (c *conn) optional(ctx context.Context, res *connectors.Result, endpoint string, out any) (bool, error) {
	err := c.get(ctx, endpoint, out)
	switch code := statusCode(err); {
	case err == nil:
		return true, nil
	case code == 403 || code == 404:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: HTTP %d (skipped; check API key privileges)", endpoint, code))
		return false, nil
	default:
		return false, fmt.Errorf("%s: %w", endpoint, err)
	}
}

// Sync reads the current state.
func (c *conn) Sync(ctx context.Context) (connectors.Result, error) {
	var res connectors.Result
	if _, err := c.version(ctx); err != nil {
		return res, err
	}
	var sys json.RawMessage
	if err := c.get(ctx, "core/system/status", &sys); err != nil {
		return res, fmt.Errorf("system status: %w", err)
	}
	if err := c.networks(ctx, &res); err != nil {
		return res, err
	}
	backend, err := c.detectBackend(ctx, &res)
	if err != nil {
		return res, err
	}
	b := newBuilder(&res)
	switch backend {
	case BackendKea:
		err = c.kea(ctx, &res, b)
	case BackendDnsmasq:
		err = c.dnsmasq(ctx, &res, b)
	case BackendISC:
		err = c.isc(ctx, &res, b)
	default:
		res.Warnings = append(res.Warnings, "no DHCP backend detected (kea and dnsmasq not running, ISC leases unavailable)")
	}
	if err != nil {
		return res, err
	}
	if err := c.neighbours(ctx, &res, b); err != nil {
		return res, err
	}
	return res, nil
}

// detectBackend picks the running DHCP backend: Kea, then dnsmasq, then ISC
// when its leases endpoint answers. Nothing is assumed.
func (c *conn) detectBackend(ctx context.Context, res *connectors.Result) (string, error) {
	running := func(endpoint string) (bool, error) {
		var st struct {
			Status string `json:"status"`
		}
		ok, err := c.optional(ctx, res, endpoint, &st)
		return ok && strings.EqualFold(st.Status, "running"), err
	}
	for _, p := range []struct{ backend, endpoint string }{
		{BackendKea, "kea/service/status"}, {BackendDnsmasq, "dnsmasq/service/status"},
	} {
		r, err := running(p.endpoint)
		if err != nil {
			return "", err
		}
		if r {
			return p.backend, nil
		}
	}
	// ISC legacy: its status endpoint may be forbidden, so a missing status is
	// tolerated; an explicit non-running status rules ISC out. The leases
	// endpoint decides otherwise.
	var st struct {
		Status string `json:"status"`
	}
	if ok, err := c.optional(ctx, res, "dhcpv4/service/status", &st); err != nil {
		return "", err
	} else if ok && st.Status != "" && !strings.EqualFold(st.Status, "running") {
		return BackendNone, nil
	}
	return BackendISC, nil
}
