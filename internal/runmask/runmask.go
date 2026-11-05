// Package runmask removes known secret values from run output.
package runmask

import (
	"encoding/json"
	"sort"
	"strings"
)

// Replacement replaces a masked value.
const Replacement = "***"

// minLen is the shortest value that is masked; shorter ones would garble the
// log (and are not credible secrets).
const minLen = 4

// Masker replaces known secret values.
type Masker struct {
	r *strings.Replacer
}

// New builds a Masker. Besides each value, its lines (for multi-line values
// such as private keys, without PEM armor lines) and its JSON-escaped form
// are masked.
func New(values []string) *Masker {
	set := map[string]bool{}
	add := func(s string) {
		if len(s) >= minLen {
			set[s] = true
		}
	}
	for _, v := range values {
		v = strings.TrimRight(v, "\r\n")
		add(v)
		if strings.ContainsAny(v, "\r\n") {
			for _, l := range strings.FieldsFunc(v, func(r rune) bool { return r == '\n' || r == '\r' }) {
				if !strings.HasPrefix(l, "-----") {
					add(l)
				}
			}
		}
		if b, err := json.Marshal(v); err == nil && len(b) > 2 {
			if esc := string(b[1 : len(b)-1]); esc != v {
				add(esc)
			}
		}
	}
	list := make([]string, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	// Longest first so that a value containing another one is replaced whole.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	pairs := make([]string, 0, 2*len(list))
	for _, s := range list {
		pairs = append(pairs, s, Replacement)
	}
	return &Masker{r: strings.NewReplacer(pairs...)}
}

// Mask returns s with all known values replaced.
func (m *Masker) Mask(s string) string {
	if m == nil || m.r == nil {
		return s
	}
	return m.r.Replace(s)
}
