package inventory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	sectionRe = regexp.MustCompile(`^\[([^:\[\]]+)(?::(vars|children))?\]$`)
	rangeRe   = regexp.MustCompile(`\[([A-Za-z0-9]+):([A-Za-z0-9]+)(?::([0-9]+))?\]`)
	intRe     = regexp.MustCompile(`^[-+]?[0-9]+$`)
	floatRe   = regexp.MustCompile(`^[-+]?[0-9]*\.[0-9]+([eE][-+]?[0-9]+)?$`)
	portRe    = regexp.MustCompile(`^(.+):([0-9]{1,5})$`)
)

// ParseINI parses an inventory in Ansible's INI format.
func ParseINI(data []byte) (*Doc, error) {
	b := newBuilder()
	section, kind := "ungrouped", "hosts"
	for n, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		wrap := func(err error) error { return fmt.Errorf("line %d: %w", n+1, err) }
		if line[0] == '[' {
			m := sectionRe.FindStringSubmatch(line)
			if m == nil {
				return nil, wrap(invalid("malformed section header %q", line))
			}
			section, kind = m[1], m[2]
			if kind == "" {
				kind = "hosts"
			}
			if _, err := b.group(section); err != nil && section != GroupUngrouped {
				return nil, wrap(err)
			}
			continue
		}
		var err error
		switch kind {
		case "hosts":
			err = b.iniHostLine(section, line)
		case "children":
			err = b.link(section, strings.Fields(line)[0])
		case "vars":
			var g *Group
			if g, err = b.group(section); err == nil {
				k, v, perr := parseKeyValue(line)
				if perr != nil {
					err = perr
					break
				}
				g.Vars[k] = v
			}
		}
		if err != nil {
			return nil, wrap(err)
		}
	}
	return b.doc()
}

func (b *builder) iniHostLine(group, line string) error {
	tokens, err := splitArgs(line)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		return nil
	}
	pattern, port := tokens[0], ""
	if m := portRe.FindStringSubmatch(pattern); m != nil && !strings.Contains(m[1], ":") {
		pattern, port = m[1], m[2]
	}
	vars := map[string]any{}
	for _, t := range tokens[1:] {
		k, v, err := parseKeyValue(t)
		if err != nil {
			return err
		}
		vars[k] = v
	}
	if port != "" {
		p, _ := strconv.Atoi(port)
		vars["ansible_port"] = p
	}
	names, err := ExpandHostPattern(pattern)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := b.member(group, name); err != nil {
			return err
		}
		h := b.hosts[name]
		for k, v := range vars {
			h.Vars[k] = v
		}
	}
	return nil
}

// splitArgs splits a line on unquoted whitespace, keeping quotes in the
// tokens, and drops a trailing comment that starts with an unquoted '#'.
func splitArgs(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0:
			cur.WriteRune(c)
			if c == '\\' && quote == '"' && i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote, started = c, true
			cur.WriteRune(c)
		case c == '#' && !started:
			flush()
			return out, nil
		case c == ' ' || c == '\t':
			flush()
		default:
			started = true
			cur.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, invalid("unterminated quote in %q", line)
	}
	flush()
	return out, nil
}

func parseKeyValue(s string) (string, any, error) {
	k, v, ok := strings.Cut(s, "=")
	k = strings.TrimSpace(k)
	if !ok || k == "" || strings.ContainsAny(k, " \t") {
		return "", nil, invalid("expected key=value, got %q", s)
	}
	return k, parseINIValue(strings.TrimSpace(v)), nil
}

// parseINIValue interprets a value like Ansible's literal evaluation of INI
// values: quoted strings, numbers, True/False/None, JSON lists and dicts;
// anything else stays a string.
func parseINIValue(v string) any {
	switch {
	case len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\''):
		inner := v[1 : len(v)-1]
		if v[0] == '"' {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
		}
		return inner
	case v == "True":
		return true
	case v == "False":
		return false
	case v == "None":
		return nil
	case intRe.MatchString(v):
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	case floatRe.MatchString(v):
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	case strings.HasPrefix(v, "[") || strings.HasPrefix(v, "{"):
		var x any
		if json.Unmarshal([]byte(v), &x) == nil {
			if n, err := normalize(x); err == nil {
				return n
			}
		}
	}
	return v
}

// ExpandHostPattern expands Ansible host ranges such as web[01:03] or
// db-[a:c] into individual names. Zero padding of the start is kept.
func ExpandHostPattern(pattern string) ([]string, error) {
	out := []string{pattern}
	for {
		var next []string
		changed := false
		for _, p := range out {
			loc := rangeRe.FindStringSubmatchIndex(p)
			if loc == nil {
				next = append(next, p)
				continue
			}
			changed = true
			items, err := rangeItems(p[loc[2]:loc[3]], p[loc[4]:loc[5]], optGroup(p, loc, 6))
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				next = append(next, p[:loc[0]]+it+p[loc[1]:])
			}
			if len(next) > MaxRangeItems {
				return nil, invalid("host range %q expands to more than %d hosts", pattern, MaxRangeItems)
			}
		}
		out = next
		if !changed {
			return out, nil
		}
	}
}

func optGroup(s string, loc []int, i int) string {
	if loc[i] < 0 {
		return ""
	}
	return s[loc[i]:loc[i+1]]
}

func rangeItems(start, end, step string) ([]string, error) {
	st := 1
	if step != "" {
		var err error
		if st, err = strconv.Atoi(step); err != nil || st < 1 {
			return nil, invalid("invalid range step %q", step)
		}
	}
	if intRe.MatchString(start) && intRe.MatchString(end) {
		a, _ := strconv.Atoi(start)
		z, _ := strconv.Atoi(end)
		if z < a {
			return nil, invalid("invalid range [%s:%s]", start, end)
		}
		if (z-a)/st+1 > MaxRangeItems {
			return nil, invalid("range [%s:%s] is too large", start, end)
		}
		width := 0
		if len(start) > 1 && start[0] == '0' {
			width = len(start)
		}
		var out []string
		for i := a; i <= z; i += st {
			out = append(out, fmt.Sprintf("%0*d", width, i))
		}
		return out, nil
	}
	if len(start) == 1 && len(end) == 1 && start[0] <= end[0] {
		var out []string
		for c := start[0]; c <= end[0]; c += byte(st) { //nolint:gosec // st is bounded by the 1-char range
			out = append(out, string(rune(c)))
			if int(c)+st > 255 {
				break
			}
		}
		return out, nil
	}
	return nil, invalid("invalid range [%s:%s]", start, end)
}
