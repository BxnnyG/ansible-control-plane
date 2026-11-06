// Package netbox is the read-only NetBox connector (ADR-0009, ADR-0019). It
// reads IPAM prefixes, devices, virtual machines, their interfaces and IP
// addresses through the REST API (GET only) and reports them as networks and
// "device" / "vm" observations.
package netbox

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
const Type = "netbox"

// DefaultTimeout is the per-request timeout.
const DefaultTimeout = 40 * time.Second

const (
	pageSize = 200
	// maxPages bounds pagination per endpoint so a misbehaving server cannot
	// keep a sync running forever (pageSize * maxPages objects).
	maxPages = 500
)

// Spec returns the registry spec of the type. The base URL is the NetBox root
// without path, e.g. http://netbox.example.test:8080.
func Spec() connectors.Spec {
	return connectors.Spec{
		Type: Type, Label: "NetBox",
		SecretTypes: []string{secrets.TypeAPIToken}, SecretRequired: true, BaseURL: true,
		Fields: connectors.HTTPFields(),
		Help: "Base URL: http(s)://host[:port] (no /api suffix). Secret (api_token): a NetBox API token " +
			"(sent as \"Authorization: Token <token>\"). Use a read-only token (\"Write enabled\" off) of a user " +
			"with view permission on ipam.prefix, ipam.ipaddress, dcim.device, dcim.interface, " +
			"virtualization.virtualmachine and virtualization.vminterface.",
	}
}

// Register adds the NetBox type to reg.
func Register(reg *connectors.Registry) { reg.Register(Spec(), New) }

type conn struct {
	hc *httpx.Client
}

// New builds a connector from an instance and its api_token credential.
func New(inst connectors.Instance, cred connectors.Credential) (connectors.Connector, error) {
	if cred.Type != secrets.TypeAPIToken {
		return nil, errors.New("netbox: an api_token secret is required")
	}
	tok := strings.TrimSpace(string(cred.Value.Reveal()))
	if tok == "" {
		return nil, errors.New("netbox: empty api_token secret")
	}
	o := inst.HTTPOptions(httpx.Auth{HeaderName: "Authorization", HeaderV: "Token " + tok})
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	hc, err := httpx.New(o)
	if err != nil {
		return nil, fmt.Errorf("netbox: %w", err)
	}
	return &conn{hc: hc}, nil
}

func (*conn) Type() string { return Type }

// Test probes /api/status/ and requires a NetBox version.
func (c *conn) Test(ctx context.Context) error {
	var st struct {
		Version string `json:"netbox-version"`
	}
	if err := c.hc.GetJSON(ctx, "api/status/", nil, &st); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if st.Version == "" {
		return errors.New("status: no netbox-version in response (not a NetBox API?)")
	}
	return nil
}

type page struct {
	Next    *string           `json:"next"`
	Results []json.RawMessage `json:"results"`
}

// list fetches all objects of a list endpoint with limit/offset pagination.
func (c *conn) list(ctx context.Context, endpoint string, fn func(json.RawMessage) error) error {
	for i := 0; i < maxPages; i++ {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}, "offset": {strconv.Itoa(i * pageSize)}}
		var pg page
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			err = c.hc.GetJSON(ctx, endpoint, q, &pg)
			var se *httpx.StatusError
			if err == nil || ctx.Err() != nil || (errors.As(err, &se) && se.Code < 500) ||
				strings.HasPrefix(err.Error(), "decode response") {
				break
			}
		}
		if err != nil {
			return err
		}
		for _, r := range pg.Results {
			if err := fn(r); err != nil {
				return err
			}
		}
		if pg.Next == nil || *pg.Next == "" || len(pg.Results) == 0 {
			return nil
		}
	}
	return fmt.Errorf("more than %d pages, giving up", maxPages)
}

func statusCode(err error) int {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// optional runs list; 403/404 only produce a warning (partial sync), any other
// error (incl. 401) fails the sync.
func (c *conn) optional(ctx context.Context, res *connectors.Result, endpoint string, fn func(json.RawMessage) error) error {
	err := c.list(ctx, endpoint, fn)
	switch code := statusCode(err); {
	case err == nil:
		return nil
	case code == 403 || code == 404:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: HTTP %d (skipped; check API token permissions)", endpoint, code))
		return nil
	default:
		return fmt.Errorf("%s: %w", endpoint, err)
	}
}
