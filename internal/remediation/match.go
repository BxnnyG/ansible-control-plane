// Package remediation implements remediation rules (FLEET-6, ADR-0019) and the
// fleet bulk run (FLEET-7).
//
// A rule maps an open monitoring alert (connector type, service and summary
// regex, minimum severity) to a template and a limit target. Matching and
// limit resolution are pure functions (match.go, resolve.go). The Service only
// ever acts on an explicit request of a user: there is no automatic
// remediation anywhere. Every run is queued through the runs service, so the
// effective host set is authorized against the caller's scopes before the run
// exists.
package remediation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

// Limit targets of a rule.
const (
	TargetHost    = "host"
	TargetGroup   = "group"
	TargetNetwork = "network"
)

// MaxRegexLen bounds one rule regex.
const MaxRegexLen = 512

var severityRank = map[string]int{
	connectors.SeverityInfo: 1, connectors.SeverityUnknown: 2, connectors.SeverityWarning: 3, connectors.SeverityCritical: 4,
}

// ValidSeverity reports whether s is a known alert severity.
func ValidSeverity(s string) bool { _, ok := severityRank[s]; return ok }

// compile validates an optional RE2 expression; empty matches everything.
func compile(what, expr string) (*regexp.Regexp, error) {
	if expr == "" {
		return nil, nil
	}
	if len(expr) > MaxRegexLen {
		return nil, fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, what, MaxRegexLen)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a valid regular expression", ErrInvalid, what)
	}
	return re, nil
}

// Criteria is the match part of a rule.
type Criteria struct {
	ConnectorType *string
	ServiceRegex  string
	SummaryRegex  string
	MinSeverity   string
}

// Matcher is a compiled Criteria.
type Matcher struct {
	connectorType *string
	service       *regexp.Regexp
	summary       *regexp.Regexp
	minRank       int
}

// NewMatcher validates and compiles the criteria (RE2 syntax, unanchored).
func NewMatcher(c Criteria) (Matcher, error) {
	m := Matcher{minRank: 1}
	if c.ConnectorType != nil {
		t := strings.TrimSpace(*c.ConnectorType)
		if t != "" {
			m.connectorType = &t
		}
	}
	if c.MinSeverity != "" {
		r, ok := severityRank[c.MinSeverity]
		if !ok {
			return Matcher{}, fmt.Errorf("%w: min_severity must be critical, warning, unknown or info", ErrInvalid)
		}
		m.minRank = r
	}
	var err error
	if m.service, err = compile("service_regex", c.ServiceRegex); err != nil {
		return Matcher{}, err
	}
	if m.summary, err = compile("summary_regex", c.SummaryRegex); err != nil {
		return Matcher{}, err
	}
	return m, nil
}

// Matches reports whether the alert is open and satisfies every criterion.
func (m Matcher) Matches(a fleet.AlertView) bool {
	if a.ResolvedAt != nil {
		return false
	}
	if m.connectorType != nil && *m.connectorType != a.ConnectorType {
		return false
	}
	if severityRank[a.Severity] < m.minRank {
		return false
	}
	if m.service != nil && !m.service.MatchString(a.Service) {
		return false
	}
	return m.summary == nil || m.summary.MatchString(a.Summary)
}
