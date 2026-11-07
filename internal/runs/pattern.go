package runs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/policy"
)

// subscriptRe matches Ansible group subscripts such as web[0] or web[1:3].
var subscriptRe = regexp.MustCompile(`\[-?\d*(:-?\d*)?\]$`)

// rangeSubscriptRe matches range subscripts such as web[1:3]; checked before
// splitting because the range colon would otherwise act as a separator.
var rangeSubscriptRe = regexp.MustCompile(`\[-?\d*:-?\d*\]`)

type patternKind int

const (
	patUnion patternKind = iota
	patIntersect
	patExclude
)

type patternToken struct {
	kind patternKind
	text string
	re   *regexp.Regexp // set for wildcard tokens
}

// parsePattern splits an Ansible host pattern into tokens. The supported
// subset is: host and group names, "all"/"*", glob wildcards (* ? [seq]),
// ":" or "," as union separators, "&" prefix for intersection and "!" prefix
// for exclusion. Regex patterns (~) and group subscripts are rejected.
func parsePattern(p string) ([]patternToken, error) {
	if rangeSubscriptRe.MatchString(p) {
		return nil, fmt.Errorf("%w: group subscripts are not supported in limits", ErrInvalid)
	}
	var out []patternToken
	for _, raw := range strings.FieldsFunc(p, func(r rune) bool { return r == ':' || r == ',' }) {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		t := patternToken{kind: patUnion}
		switch s[0] {
		case '&':
			t.kind, s = patIntersect, strings.TrimSpace(s[1:])
		case '!':
			t.kind, s = patExclude, strings.TrimSpace(s[1:])
		}
		if s == "" {
			return nil, fmt.Errorf("%w: empty name in limit pattern", ErrInvalid)
		}
		if strings.HasPrefix(s, "~") {
			return nil, fmt.Errorf("%w: regular expression patterns are not supported in limits", ErrInvalid)
		}
		if strings.HasPrefix(s, "@") {
			return nil, fmt.Errorf("%w: @file limits are not supported", ErrInvalid)
		}
		if subscriptRe.MatchString(s) {
			return nil, fmt.Errorf("%w: group subscripts are not supported in limits", ErrInvalid)
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f || r == ' ' {
				return nil, fmt.Errorf("%w: invalid character in limit pattern", ErrInvalid)
			}
		}
		t.text = s
		if strings.ContainsAny(s, "*?[") && s != "*" {
			re, err := globRegexp(s)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid wildcard in limit pattern", ErrInvalid)
			}
			t.re = re
		}
		out = append(out, t)
	}
	return out, nil
}

// ValidatePattern checks the syntax of a limit pattern.
func ValidatePattern(p string) error {
	_, err := parsePattern(p)
	return err
}

func globRegexp(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := strings.IndexByte(g[i+1:], ']')
			if end <= 0 {
				b.WriteString(`\[`)
				continue
			}
			set := g[i+1 : i+1+end]
			if strings.HasPrefix(set, "!") {
				set = "^" + set[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(set, `\`, `\\`) + "]")
			i += end + 1
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ResolveLimit expands an Ansible host pattern against an inventory graph and
// returns the sorted names of the matching hosts. An empty pattern selects
// every host. Groups include the hosts of all descendant groups. As in
// Ansible, positive terms are united first, then "&" terms intersect and "!"
// terms subtract; a pattern without positive terms starts from all hosts.
func ResolveLimit(g policy.Graph, pattern string) ([]string, error) {
	toks, err := parsePattern(pattern)
	if err != nil {
		return nil, err
	}
	all := map[string]bool{}
	for _, n := range g.Hosts {
		all[n] = true
	}
	if len(toks) == 0 {
		return sortedSet(all), nil
	}
	m := newMatcher(g, all)
	var pos, inter, excl []patternToken
	for _, t := range toks {
		switch t.kind {
		case patUnion:
			pos = append(pos, t)
		case patIntersect:
			inter = append(inter, t)
		default:
			excl = append(excl, t)
		}
	}
	cur := map[string]bool{}
	if len(pos) == 0 {
		for h := range all {
			cur[h] = true
		}
	}
	for _, t := range pos {
		for h := range m.match(t) {
			cur[h] = true
		}
	}
	for _, t := range inter {
		keep := m.match(t)
		for h := range cur {
			if !keep[h] {
				delete(cur, h)
			}
		}
	}
	for _, t := range excl {
		for h := range m.match(t) {
			delete(cur, h)
		}
	}
	return sortedSet(cur), nil
}

type matcher struct {
	all    map[string]bool
	hosts  map[string]bool
	groups map[string]map[string]bool // group name -> host names incl. descendants
}

func newMatcher(g policy.Graph, all map[string]bool) *matcher {
	children := map[uuid.UUID][]uuid.UUID{}
	for _, l := range g.Links {
		children[l.Parent] = append(children[l.Parent], l.Child)
	}
	direct := map[uuid.UUID][]string{}
	for _, mb := range g.Members {
		if n, ok := g.Hosts[mb.Host]; ok {
			direct[mb.Group] = append(direct[mb.Group], n)
		}
	}
	m := &matcher{all: all, hosts: all, groups: map[string]map[string]bool{}}
	for id, name := range g.Groups {
		set := map[string]bool{}
		seen := map[uuid.UUID]bool{}
		stack := []uuid.UUID{id}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[cur] {
				continue
			}
			seen[cur] = true
			for _, h := range direct[cur] {
				set[h] = true
			}
			stack = append(stack, children[cur]...)
		}
		m.groups[name] = set
	}
	// "ungrouped" is the implicit group of hosts without any group.
	if _, ok := m.groups["ungrouped"]; !ok {
		grouped := map[uuid.UUID]bool{}
		for _, mb := range g.Members {
			grouped[mb.Host] = true
		}
		set := map[string]bool{}
		for id, n := range g.Hosts {
			if !grouped[id] {
				set[n] = true
			}
		}
		m.groups["ungrouped"] = set
	}
	return m
}

func (m *matcher) match(t patternToken) map[string]bool {
	out := map[string]bool{}
	switch {
	case t.text == "all" || t.text == "*":
		for h := range m.all {
			out[h] = true
		}
	case t.re != nil:
		for h := range m.hosts {
			if t.re.MatchString(h) {
				out[h] = true
			}
		}
		for name, set := range m.groups {
			if t.re.MatchString(name) {
				for h := range set {
					out[h] = true
				}
			}
		}
	default:
		if m.hosts[t.text] {
			out[t.text] = true
		}
		for h := range m.groups[t.text] {
			out[h] = true
		}
	}
	return out
}

func sortedSet(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
