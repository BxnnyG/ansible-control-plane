package semaphore

import (
	"net/url"
	"strings"
)

// NormalizeGitURL reduces a Git URL to "host/path" (lower-case host, no user,
// no default port, no ".git", no trailing slash) so ssh and https forms of the
// same repository compare equal. Local paths return ok == false.
func NormalizeGitURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "file" || u.Hostname() == "" {
			return "", false
		}
		host = strings.ToLower(u.Hostname())
		if p := u.Port(); p != "" && p != "22" && p != "443" && p != "80" {
			host += ":" + p
		}
		path = u.Path
	case strings.HasPrefix(s, "/") || strings.HasPrefix(s, "."):
		return "", false
	default: // scp-like user@host:path
		hp, p, ok := strings.Cut(s, ":")
		if !ok {
			return "", false
		}
		if i := strings.LastIndex(hp, "@"); i >= 0 {
			hp = hp[i+1:]
		}
		host, path = strings.ToLower(hp), p
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	if host == "" || path == "" {
		return "", false
	}
	return host + "/" + path, true
}
