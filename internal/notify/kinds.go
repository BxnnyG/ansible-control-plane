// Package notify delivers run notifications (ADR-0021): an outbox filled by a
// database trigger, a dispatcher that derives event kinds and matches rules,
// and one sender per channel type.
package notify

import (
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// Event kinds a rule can subscribe to.
const (
	KindFailed    = "run.failed"
	KindRecovered = "run.recovered"
	KindDrift     = "run.drift"
	KindFinished  = "run.finished"
)

// kindOrder is the precedence when several kinds match one channel: the first
// one wins (failed > recovered > drift > finished).
var kindOrder = []string{KindFailed, KindRecovered, KindDrift, KindFinished}

// ValidKind reports whether k is a known event kind.
func ValidKind(k string) bool {
	for _, v := range kindOrder {
		if v == k {
			return true
		}
	}
	return false
}

// RunFacts are the run attributes event derivation needs.
type RunFacts struct {
	Status       string
	Check        bool
	HostsChanged int
	// PrevStatus is the status of the previous terminal run of the same
	// template (cancelled runs ignored); empty when there is none.
	PrevStatus string
}

// DeriveKinds returns every kind the run qualifies for, most specific first.
// run.finished always matches because only terminal runs reach the outbox.
func DeriveKinds(f RunFacts) []string {
	var out []string
	failed := f.Status == runs.StatusFailed || f.Status == runs.StatusLost
	if failed {
		out = append(out, KindFailed)
	}
	if f.Status == runs.StatusSucceeded && (f.PrevStatus == runs.StatusFailed || f.PrevStatus == runs.StatusLost) {
		out = append(out, KindRecovered)
	}
	if f.Check && f.Status == runs.StatusSucceeded && f.HostsChanged > 0 {
		out = append(out, KindDrift)
	}
	return append(out, KindFinished)
}

// PickKind returns the most specific derived kind that the subscribed set
// contains, or "" when the channel is not interested.
func PickKind(derived []string, subscribed map[string]bool) string {
	for _, k := range kindOrder {
		if !subscribed[k] {
			continue
		}
		for _, d := range derived {
			if d == k {
				return k
			}
		}
	}
	return ""
}
