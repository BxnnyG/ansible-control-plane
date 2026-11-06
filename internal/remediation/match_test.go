package remediation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

func str(s string) *string { return &s }

func TestMatcher(t *testing.T) {
	now := time.Now()
	alert := func(typ, service, summary, sev string) fleet.AlertView {
		return fleet.AlertView{ConnectorType: typ, Service: service, Summary: summary, Severity: sev}
	}
	resolved := alert("checkmk", "Disk /", "full", "critical")
	resolved.ResolvedAt = &now
	for _, tt := range []struct {
		name string
		c    Criteria
		a    fleet.AlertView
		want bool
	}{
		{"empty criteria match every open alert", Criteria{}, alert("checkmk", "x", "y", "info"), true},
		{"resolved alerts never match", Criteria{}, resolved, false},
		{"connector type", Criteria{ConnectorType: str("alert_webhook")}, alert("checkmk", "x", "y", "critical"), false},
		{"connector type hit", Criteria{ConnectorType: str("checkmk")}, alert("checkmk", "x", "y", "critical"), true},
		{"blank connector type is any", Criteria{ConnectorType: str(" ")}, alert("checkmk", "x", "y", "info"), true},
		{"min severity below", Criteria{MinSeverity: "warning"}, alert("checkmk", "x", "y", "unknown"), false},
		{"min severity equal", Criteria{MinSeverity: "warning"}, alert("checkmk", "x", "y", "warning"), true},
		{"min severity above", Criteria{MinSeverity: "warning"}, alert("checkmk", "x", "y", "critical"), true},
		{"service regex is unanchored", Criteria{ServiceRegex: `^Filesystem `}, alert("checkmk", "Filesystem /var", "y", "info"), true},
		{"service regex miss", Criteria{ServiceRegex: `^Filesystem `}, alert("checkmk", "Memory", "y", "info"), false},
		{"summary regex", Criteria{SummaryRegex: `(?i)crit.*9\d%`}, alert("checkmk", "Disk", "CRIT - 97% used", "info"), true},
		{"both regexes must match", Criteria{ServiceRegex: "Disk", SummaryRegex: "full"}, alert("checkmk", "Disk", "ok", "info"), false},
	} {
		m, err := NewMatcher(tt.c)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := m.Matches(tt.a); got != tt.want {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestMatcherValidation(t *testing.T) {
	for name, c := range map[string]Criteria{
		"bad service regex":   {ServiceRegex: `(`},
		"bad summary regex":   {SummaryRegex: `[a-`},
		"lookahead is RE2-no": {SummaryRegex: `(?=x)`},
		"bad severity":        {MinSeverity: "fatal"},
		"regex too long":      {ServiceRegex: strings.Repeat("a", MaxRegexLen+1)},
	} {
		if _, err := NewMatcher(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", name, err)
		}
	}
}
