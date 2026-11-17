// Package reachability implements the reachability matrix (ADR-0022, NET-1):
// probe sources and targets, the interval scheduler, ingestion of probe
// results from built-in probe runs and the matrix read model.
package reachability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Check types and result statuses.
const (
	CheckICMP = "icmp"
	CheckTCP  = "tcp"
	CheckDNS  = "dns"

	StatusOK          = "ok"
	StatusFailed      = "failed"
	StatusUnreachable = "probe_unreachable"
)

// Limits of the probe payload of one host. The callback emits one event per
// source host, so the bound stays far below the worker's event size limit.
const (
	MaxPayloadBytes   = 64 << 10
	MaxResultsPerHost = 256
	MaxDetailBytes    = 200
	maxLatencyMS      = 3_600_000
)

// ErrPayload marks a probe payload that violates the schema.
var ErrPayload = errors.New("invalid probe payload")

// CheckResult is the result of one check of one source, as published by the
// probe playbook under the set_stats key acp_probe (keyed by target id).
type CheckResult struct {
	Status    string `json:"status"`
	LatencyMS *int   `json:"latency_ms"`
	Detail    string `json:"detail"`
}

// ParsePayload validates the acp_probe payload of one host: a JSON object of
// at most MaxResultsPerHost entries keyed by target uuid, with known fields
// only, a status of ok or failed and a bounded latency. Details are cut to
// MaxDetailBytes and stripped of control characters.
func ParsePayload(raw []byte) (map[uuid.UUID]CheckResult, error) {
	if len(raw) == 0 || len(raw) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: size %d", ErrPayload, len(raw))
	}
	var in map[string]CheckResult
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPayload, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrPayload)
	}
	if len(in) > MaxResultsPerHost {
		return nil, fmt.Errorf("%w: %d results", ErrPayload, len(in))
	}
	out := make(map[uuid.UUID]CheckResult, len(in))
	for k, r := range in {
		id, err := uuid.Parse(k)
		if err != nil {
			return nil, fmt.Errorf("%w: key is not a target id", ErrPayload)
		}
		if r.Status != StatusOK && r.Status != StatusFailed {
			return nil, fmt.Errorf("%w: status %q", ErrPayload, r.Status)
		}
		if r.LatencyMS != nil && (*r.LatencyMS < 0 || *r.LatencyMS > maxLatencyMS) {
			return nil, fmt.Errorf("%w: latency out of range", ErrPayload)
		}
		r.Detail = cleanDetail(r.Detail)
		out[id] = r
	}
	return out, nil
}

func cleanDetail(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	s = strings.TrimSpace(s)
	if len(s) > MaxDetailBytes {
		s = s[:MaxDetailBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

var (
	addrRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,252}$`)
	dnsNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,252}$`)
	// hostRe limits source host names: they are joined into the --limit
	// pattern, where ':', '!', '&' and '*' have a meaning.
	hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
)

// TargetInput is the writable part of a probe target.
type TargetInput struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	Check   string `json:"check"`
	Port    *int   `json:"port"`
	DNSName string `json:"dns_name"`
	Enabled *bool  `json:"enabled"`
}

// validate checks the input and normalizes the check-specific fields. Values
// reach commands on the source hosts (as argv entries, never a shell), so the
// character sets are restrictive.
func (in *TargetInput) validate() error {
	in.Name, in.Address, in.DNSName = strings.TrimSpace(in.Name), strings.TrimSpace(in.Address), strings.TrimSpace(in.DNSName)
	if in.Name == "" || len(in.Name) > 120 {
		return fmt.Errorf("%w: name must be 1-120 characters", ErrInvalid)
	}
	if !addrRe.MatchString(in.Address) {
		return fmt.Errorf("%w: address must be a host name or IP address", ErrInvalid)
	}
	switch in.Check {
	case CheckICMP:
		in.Port, in.DNSName = nil, ""
	case CheckTCP:
		if in.Port == nil || *in.Port < 1 || *in.Port > 65535 {
			return fmt.Errorf("%w: tcp checks need a port between 1 and 65535", ErrInvalid)
		}
		in.DNSName = ""
	case CheckDNS:
		if !dnsNameRe.MatchString(in.DNSName) {
			return fmt.Errorf("%w: dns checks need a valid dns_name", ErrInvalid)
		}
		in.Port = nil
	default:
		return fmt.Errorf("%w: check must be icmp, tcp or dns", ErrInvalid)
	}
	return nil
}
