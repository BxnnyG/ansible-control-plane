package grouprules

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// Rule kinds. The set is open: a kind is a registered Factory, its parameters
// are JSON, and the database stores both as-is (new kinds need no migration).
const (
	KindSubnet     = "subnet"
	KindProxmoxTag = "proxmox_tag"
)

// ErrInvalid wraps validation errors of rules and their parameters.
var ErrInvalid = errors.New("invalid group rule")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Host is the view of a fleet host that rules match on and name from.
type Host struct {
	ID       uuid.UUID
	Name     string
	Hostname string
	FQDN     string
	IPs      []string
	Tags     []string
}

// Matcher decides whether a fleet host belongs to a rule.
type Matcher interface {
	Match(h Host) bool
}

// Factory validates raw parameters and returns the matcher together with the
// normalized parameters to store.
type Factory func(params json.RawMessage) (Matcher, json.RawMessage, error)

var factories = map[string]Factory{
	KindSubnet:     newSubnet,
	KindProxmoxTag: newProxmoxTag,
}

// Kinds returns the registered rule kinds, sorted.
func Kinds() []string {
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseMatcher builds the matcher of a rule and returns the normalized
// parameters.
func ParseMatcher(kind string, params json.RawMessage) (Matcher, json.RawMessage, error) {
	f, ok := factories[kind]
	if !ok {
		return nil, nil, invalid("unknown rule kind %q (known: %s)", kind, strings.Join(Kinds(), ", "))
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	return f(params)
}

func strictDecode(params json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(params)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("malformed parameters: %v", err)
	}
	return nil
}

type subnetMatcher struct{ prefix netip.Prefix }

func newSubnet(params json.RawMessage) (Matcher, json.RawMessage, error) {
	var p struct {
		CIDR string `json:"cidr"`
	}
	if err := strictDecode(params, &p); err != nil {
		return nil, nil, err
	}
	pfx, err := netip.ParsePrefix(strings.TrimSpace(p.CIDR))
	if err != nil {
		return nil, nil, invalid("cidr must be an IPv4 or IPv6 prefix such as 192.0.2.0/24")
	}
	pfx = netip.PrefixFrom(pfx.Addr().Unmap(), pfx.Bits()).Masked()
	norm, _ := json.Marshal(map[string]string{"cidr": pfx.String()})
	return subnetMatcher{prefix: pfx}, norm, nil
}

func (m subnetMatcher) Match(h Host) bool {
	for _, s := range h.IPs {
		if ip, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil && m.prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

type tagMatcher struct{ tag string }

func newProxmoxTag(params json.RawMessage) (Matcher, json.RawMessage, error) {
	var p struct {
		Tag string `json:"tag"`
	}
	if err := strictDecode(params, &p); err != nil {
		return nil, nil, err
	}
	tag := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Tag), "proxmox:")))
	if tag == "" || len(tag) > 64 || strings.ContainsAny(tag, " \t\r\n,;") {
		return nil, nil, invalid("tag must be a single Proxmox tag without whitespace")
	}
	norm, _ := json.Marshal(map[string]string{"tag": tag})
	return tagMatcher{tag: "proxmox:" + tag}, norm, nil
}

func (m tagMatcher) Match(h Host) bool {
	for _, t := range h.Tags {
		if strings.ToLower(strings.TrimSpace(t)) == m.tag {
			return true
		}
	}
	return false
}
