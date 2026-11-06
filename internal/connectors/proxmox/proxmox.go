// Package proxmox is the read-only Proxmox VE connector (ADR-0009, ADR-0018).
// It reads nodes, QEMU VMs and LXC containers (status, tags, notes, MACs, IPs
// via guest agent / LXC interfaces) and the node networks through the GET API.
package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors/httpx"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Type is the registered connector type name.
const Type = "proxmox"

// DefaultTimeout is the per-request timeout.
const DefaultTimeout = 40 * time.Second

// Spec returns the registry spec of the type. The base URL is the API root
// without path, e.g. https://pve.example.test:8006.
func Spec() connectors.Spec {
	return connectors.Spec{
		Type: Type, Label: "Proxmox VE",
		SecretTypes: []string{secrets.TypeAPIToken}, SecretRequired: true, BaseURL: true,
		Fields: connectors.HTTPFields(),
		Help: "Base URL: https://host:8006. Secret (api_token): the full token as one string " +
			"\"user@realm!tokenname=secret-uuid\". The token needs PVEAuditor (Sys.Audit, VM.Audit, " +
			"VM.Monitor for guest agent data) on path / with propagation; a privilege-separated token needs the role itself.",
	}
}

// Register adds the Proxmox type to reg.
func Register(reg *connectors.Registry) { reg.Register(Spec(), New) }

type conn struct {
	hc *httpx.Client
}

// New builds a connector from an instance and its api_token credential
// ("user@realm!tokenname=secret").
func New(inst connectors.Instance, cred connectors.Credential) (connectors.Connector, error) {
	if cred.Type != secrets.TypeAPIToken {
		return nil, errors.New("proxmox: an api_token secret is required")
	}
	tok := strings.TrimSpace(string(cred.Value.Reveal()))
	id, secret, ok := strings.Cut(tok, "=")
	if !ok || secret == "" || !strings.Contains(id, "@") || !strings.Contains(id, "!") {
		return nil, errors.New("proxmox: invalid api_token secret (want user@realm!tokenname=secret)")
	}
	o := inst.HTTPOptions(httpx.Auth{HeaderName: "Authorization", HeaderV: "PVEAPIToken=" + tok})
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	hc, err := httpx.New(o)
	if err != nil {
		return nil, fmt.Errorf("proxmox: %w", err)
	}
	return &conn{hc: hc}, nil
}

func (*conn) Type() string { return Type }

// Test probes /version and requires a version string.
func (c *conn) Test(ctx context.Context) error {
	_, err := c.version(ctx)
	return err
}

func (c *conn) version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "version", nil, &v, true); err != nil {
		return "", fmt.Errorf("version: %w", err)
	}
	if v.Version == "" {
		return "", errors.New("version: no version in response (not a Proxmox VE API?)")
	}
	return v.Version, nil
}

// get fetches an endpoint below api2/json and unwraps the {"data": ...}
// envelope. With retry it repeats once on transport errors and 5xx.
func (c *conn) get(ctx context.Context, endpoint string, q url.Values, out any, retry bool) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.hc.GetJSON(ctx, "api2/json/"+endpoint, q, &env)
		if err == nil || ctx.Err() != nil || !retry {
			break
		}
		var se *httpx.StatusError
		if errors.As(err, &se) && se.Code < 500 {
			break
		}
		if strings.HasPrefix(err.Error(), "decode response") {
			break
		}
	}
	if err != nil {
		return err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return errors.New("response has no data")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
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
func (c *conn) optional(ctx context.Context, res *connectors.Result, endpoint string, q url.Values, out any) (bool, error) {
	err := c.get(ctx, endpoint, q, out, true)
	switch code := statusCode(err); {
	case err == nil:
		return true, nil
	case code == 403 || code == 404:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: HTTP %d (skipped; check API token privileges)", endpoint, code))
		return false, nil
	default:
		return false, fmt.Errorf("%s: %w", endpoint, err)
	}
}

// bestEffort runs a fetch whose failure (except 401, ctx cancel) is only a
// warning, e.g. guest agent not running. It does not retry.
func (c *conn) bestEffort(ctx context.Context, res *connectors.Result, endpoint, why string, out any) (bool, error) {
	err := c.get(ctx, endpoint, nil, out, false)
	switch {
	case err == nil:
		return true, nil
	case statusCode(err) == 401 || ctx.Err() != nil:
		return false, fmt.Errorf("%s: %w", endpoint, err)
	default:
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %s (%v)", endpoint, why, err))
		return false, nil
	}
}
