// Package connectors is the framework for read-only integrations (ADR-0009,
// ADR-0018): the Connector interface, a registry with a config schema per
// type, and the sync runtime that persists networks and host observations.
package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors/httpx"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Observation kinds (host_observations.kind).
const (
	KindLease  = "lease"
	KindARP    = "arp"
	KindNDP    = "ndp"
	KindStatic = "static"
	KindVM     = "vm"
	KindLXC    = "lxc"
	KindNode   = "node"
)

var validKinds = map[string]bool{KindLease: true, KindARP: true, KindNDP: true, KindStatic: true, KindVM: true, KindLXC: true, KindNode: true}

// Connector is one configured instance of an integration. Implementations
// must only read from the remote system (use httpx for HTTP).
type Connector interface {
	// Type returns the registered type name.
	Type() string
	// Test checks reachability and credentials without changing anything.
	Test(ctx context.Context) error
	// Sync fetches the current state of the remote system.
	Sync(ctx context.Context) (Result, error)
}

// Network is a normalized network (subnet) reported by a connector.
type Network struct {
	ExternalKey string // stable per connector
	CIDR        netip.Prefix
	VLAN        *int
	Interface   string
	Description string
	Source      string
}

// Fields are the normalized attributes of a host observation (ADR-0018).
type Fields struct {
	MAC      string   `json:"mac,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	FQDN     string   `json:"fqdn,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Notes    string   `json:"notes,omitempty"`
	Status   string   `json:"status,omitempty"`
}

// HostObservation is one observed entity of a connector.
type HostObservation struct {
	ExternalKey string // stable per connector
	Kind        string
	Fields      Fields
}

// Alert is a monitoring alert reported by a connector. Alerts are counted but
// not yet stored (M6).
type Alert struct {
	ExternalKey string
	Host        string
	Severity    string
	Message     string
	At          time.Time
}

// Result is the normalized outcome of a sync.
type Result struct {
	Networks     []Network
	Observations []HostObservation
	Alerts       []Alert
	// Warnings lists optional data that could not be fetched (partial sync).
	Warnings []string
}

// Instance is the stored configuration of one connector.
type Instance struct {
	ID        uuid.UUID
	Workspace uuid.UUID
	Name      string
	BaseURL   string
	Config    map[string]any
}

// Credential is the decrypted secret of an instance; it is zeroed after the
// sync. Type is empty when the instance has no secret.
type Credential struct {
	Type  string
	Value secrets.Value
}

// Factory builds a connector for an instance.
type Factory func(inst Instance, cred Credential) (Connector, error)

// Field kinds of the config schema.
const (
	FieldString = "string"
	FieldBool   = "bool"
	FieldInt    = "int"
)

// Field describes one config key; the UI and setup wizard render it.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	Help     string `json:"help,omitempty"`
}

// Config keys shared by all HTTP connectors (see HTTPOptions).
const (
	CfgCAPEM         = "ca_pem"
	CfgTLSSkipVerify = "tls_skip_verify"
	CfgTimeoutSecs   = "timeout_seconds"
)

// HTTPFields is the schema fragment for the shared HTTP/TLS options.
func HTTPFields() []Field {
	return []Field{
		{Key: CfgCAPEM, Label: "Custom CA certificate (PEM)", Kind: FieldString, Help: "Additional trust anchor for HTTPS."},
		{Key: CfgTLSSkipVerify, Label: "Skip TLS verification", Kind: FieldBool, Help: "Insecure; only for lab systems."},
		{Key: CfgTimeoutSecs, Label: "Request timeout (seconds)", Kind: FieldInt},
	}
}

// Spec is the registered description of a connector type.
type Spec struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	// SecretTypes lists the accepted secret types; empty means the type
	// needs no credentials.
	SecretTypes    []string `json:"secret_types"`
	SecretRequired bool     `json:"secret_required"`
	BaseURL        bool     `json:"base_url"` // instance needs a base URL
	Fields         []Field  `json:"fields"`
}

// HTTPOptions derives httpx options from the instance (base URL and shared
// TLS/timeout keys) and the credential applied by auth.
func (i Instance) HTTPOptions(auth httpx.Auth) httpx.Options {
	o := httpx.Options{BaseURL: i.BaseURL, Auth: auth}
	if s, ok := i.Config[CfgCAPEM].(string); ok && s != "" {
		o.CAPEM = []byte(s)
	}
	if b, ok := i.Config[CfgTLSSkipVerify].(bool); ok {
		o.SkipTLSVerify = b
	}
	if n, ok := asInt(i.Config[CfgTimeoutSecs]); ok && n > 0 {
		o.Timeout = time.Duration(n) * time.Second
	}
	return o
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// ErrUnknownType is returned for unregistered connector types.
var ErrUnknownType = errors.New("unknown connector type")

// ErrInvalidConfig wraps config validation failures (safe to show to clients).
var ErrInvalidConfig = errors.New("invalid connector configuration")

type entry struct {
	spec    Spec
	factory Factory
}

// Registry maps connector types to their spec and factory.
type Registry struct{ m map[string]entry }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]entry{}} }

// Register adds a type; it panics on duplicates or an empty type (programming error).
func (r *Registry) Register(spec Spec, f Factory) {
	if spec.Type == "" || f == nil {
		panic("connectors: invalid registration")
	}
	if _, dup := r.m[spec.Type]; dup {
		panic("connectors: duplicate type " + spec.Type)
	}
	r.m[spec.Type] = entry{spec: spec, factory: f}
}

// Spec returns the spec of a type.
func (r *Registry) Spec(t string) (Spec, bool) {
	e, ok := r.m[t]
	return e.spec, ok
}

// Specs returns all registered specs sorted by type.
func (r *Registry) Specs() []Spec {
	out := make([]Spec, 0, len(r.m))
	for _, e := range r.m {
		out = append(out, e.spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// New builds a connector.
func (r *Registry) New(t string, inst Instance, cred Credential) (Connector, error) {
	e, ok := r.m[t]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, t)
	}
	return e.factory(inst, cred)
}

// Validate checks base URL, config and secret type of an instance against the
// type's spec.
func (r *Registry) Validate(t, baseURL string, cfg map[string]any, secretType string, hasSecret bool) error {
	spec, ok := r.Spec(t)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownType, t)
	}
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(f, a...)) }
	if spec.BaseURL && baseURL == "" {
		return bad("base URL is required")
	}
	if !spec.BaseURL && baseURL != "" {
		return bad("type %s takes no base URL", t)
	}
	if spec.BaseURL {
		if _, err := httpx.New(httpx.Options{BaseURL: baseURL}); err != nil {
			return bad("%v", err)
		}
	}
	known := map[string]Field{}
	for _, f := range spec.Fields {
		known[f.Key] = f
	}
	for k, v := range cfg {
		f, ok := known[k]
		if !ok {
			return bad("unknown option %q", k)
		}
		okType := false
		switch f.Kind {
		case FieldString:
			_, okType = v.(string)
		case FieldBool:
			_, okType = v.(bool)
		case FieldInt:
			_, okType = asInt(v)
		}
		if !okType {
			return bad("option %q must be of kind %s", k, f.Kind)
		}
	}
	for _, f := range spec.Fields {
		if _, ok := cfg[f.Key]; f.Required && !ok {
			return bad("option %q is required", f.Key)
		}
	}
	if s, ok := cfg[CfgCAPEM].(string); ok && s != "" && spec.BaseURL {
		if _, err := httpx.New(httpx.Options{BaseURL: baseURL, CAPEM: []byte(s)}); err != nil {
			return bad("%v", err)
		}
	}
	switch {
	case hasSecret && len(spec.SecretTypes) == 0:
		return bad("type %s takes no secret", t)
	case hasSecret:
		for _, st := range spec.SecretTypes {
			if st == secretType {
				return nil
			}
		}
		return bad("secret type %q not accepted (want one of %v)", secretType, spec.SecretTypes)
	case spec.SecretRequired:
		return bad("a secret is required")
	}
	return nil
}
