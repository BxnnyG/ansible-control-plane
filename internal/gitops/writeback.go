package gitops

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var revRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func validRev(rev string) error {
	if !revRe.MatchString(rev) {
		return invalid("revision %q", rev)
	}
	return nil
}

// FetchBranch fetches refs/heads/<branch> from the remote into dir without
// creating a local ref. found is false when the remote has no such branch.
func (c *Client) FetchBranch(ctx context.Context, dir string, r Remote, branch string) (sha string, found bool, err error) {
	if err := c.validateURL(r.URL); err != nil {
		return "", false, err
	}
	if err := c.validateBranch(ctx, branch); err != nil {
		return "", false, err
	}
	if _, err := c.run(ctx, dir, nil, nil, "remote", "set-url", "origin", r.URL); err != nil {
		return "", false, err
	}
	_, err = c.run(ctx, dir, &r, nil, "fetch", "--force", "origin", "refs/heads/"+branch)
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) && strings.Contains(ge.Stderr, "couldn't find remote ref") {
			return "", false, nil
		}
		return "", false, err
	}
	out, err := c.run(ctx, dir, nil, nil, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// HasCommit reports whether the commit rev exists in the clone in dir.
func (c *Client) HasCommit(ctx context.Context, dir, rev string) bool {
	if validRev(rev) != nil {
		return false
	}
	_, err := c.run(ctx, dir, nil, nil, "cat-file", "-e", rev+"^{commit}")
	return err == nil
}

// CheckoutDetached forces the working tree to rev (detached HEAD) and removes
// untracked files.
func (c *Client) CheckoutDetached(ctx context.Context, dir, rev string) error {
	if err := validRev(rev); err != nil {
		return err
	}
	if _, err := c.run(ctx, dir, nil, nil, "checkout", "-q", "-f", "--detach", rev); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, nil, "clean", "-fdxq")
	return err
}

// Reset puts the sync branch of the clone back on rev without network access.
func (c *Client) Reset(ctx context.Context, dir, rev string) error {
	if err := validRev(rev); err != nil {
		return err
	}
	if _, err := c.run(ctx, dir, nil, nil, "checkout", "-q", "-f", "-B", "acp-sync", rev); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, nil, "clean", "-fdxq")
	return err
}

// ChangedPaths lists the files below paths that differ between the commits
// from and to.
func (c *Client) ChangedPaths(ctx context.Context, dir, from, to string, paths []string) ([]string, error) {
	if err := validRev(from); err != nil {
		return nil, err
	}
	if err := validRev(to); err != nil {
		return nil, err
	}
	if err := validatePaths(paths); err != nil {
		return nil, err
	}
	out, err := c.run(ctx, dir, nil, nil, append([]string{"diff", "--name-only", "-z", "--no-renames", from, to, "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	var res []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			res = append(res, p)
		}
	}
	return res, nil
}

// StagedDiff stages paths and returns the unified diff of the staged changes
// against HEAD.
func (c *Client) StagedDiff(ctx context.Context, dir string, paths []string) (string, error) {
	if err := validatePaths(paths); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dir, nil, nil, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return "", err
	}
	return c.run(ctx, dir, nil, nil, append([]string{"diff", "--cached", "--no-color", "--no-ext-diff", "--no-renames", "--"}, paths...)...)
}
