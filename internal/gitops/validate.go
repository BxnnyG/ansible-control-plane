package gitops

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^:]`)
	hostRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, a...))
}

// validateURL rejects dangerous transports and option-looking values.
func (c *Client) validateURL(u string) error {
	if u == "" || strings.HasPrefix(u, "-") {
		return invalid("url")
	}
	if strings.ContainsAny(u, " \t\r\n\x00") {
		return invalid("url contains whitespace")
	}
	l := strings.ToLower(u)
	if strings.Contains(l, "::") {
		return invalid("transport not allowed")
	}
	for _, p := range []string{"https://", "http://", "ssh://"} {
		if strings.HasPrefix(l, p) {
			return nil
		}
	}
	if strings.HasPrefix(l, "file://") || filepath.IsAbs(u) {
		if c.AllowFileTransport {
			return nil
		}
		return invalid("file transport not allowed")
	}
	if scpLikeRe.MatchString(u) {
		return nil
	}
	return invalid("unsupported url scheme")
}

// validateRef checks a ref/branch/tag/sha name via git check-ref-format.
func (c *Client) validateRef(ctx context.Context, ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, " \t\r\n\x00") {
		return invalid("ref %q", ref)
	}
	if _, err := c.run(ctx, "", nil, nil, "check-ref-format", "--allow-onelevel", ref); err != nil {
		return invalid("ref %q", ref)
	}
	return nil
}

func (c *Client) validateBranch(ctx context.Context, b string) error {
	if b == "" || strings.HasPrefix(b, "-") {
		return invalid("branch %q", b)
	}
	if _, err := c.run(ctx, "", nil, nil, "check-ref-format", "refs/heads/"+b); err != nil {
		return invalid("branch %q", b)
	}
	return nil
}

func validatePaths(files []string) error {
	if len(files) == 0 {
		return invalid("no files")
	}
	for _, f := range files {
		cl := filepath.Clean(f)
		if f == "" || filepath.IsAbs(cl) || cl == ".." || strings.HasPrefix(cl, "../") || strings.HasPrefix(cl, "-") {
			return invalid("path %q", f)
		}
	}
	return nil
}

// ValidateURL reports whether u is an acceptable remote URL for this client.
func (c *Client) ValidateURL(u string) error { return c.validateURL(u) }
