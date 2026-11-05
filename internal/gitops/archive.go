package gitops

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Archive streams the tree of the commit rev (a full SHA) in dir as a tar
// archive to w; every path gets the prefix (e.g. "project/"). The clone is not
// modified and needs no checkout of rev.
func (c *Client) Archive(ctx context.Context, dir, rev, prefix string, w io.Writer) error {
	if !shaRe.MatchString(rev) {
		return invalid("revision")
	}
	if strings.ContainsAny(prefix, "\x00\n") || strings.HasPrefix(prefix, "-") {
		return invalid("prefix")
	}
	inv, err := c.prepare(nil)
	if err != nil {
		return err
	}
	defer inv.cleanup()
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, c.gitBin(), "archive", "--format=tar", "--prefix="+prefix, rev) //nolint:gosec // rev is a validated SHA
	cmd.Dir = dir
	cmd.Env = inv.env
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("git archive: %w", ctx.Err())
		}
		return &GitError{Op: "archive", Stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return nil
}
