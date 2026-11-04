package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LsRemote verifies connectivity and credentials by listing remote refs.
func (c *Client) LsRemote(ctx context.Context, r Remote) error {
	if err := c.validateURL(r.URL); err != nil {
		return err
	}
	_, err := c.run(ctx, "", &r, nil, "ls-remote", "--heads", r.URL)
	return err
}

// Sync clones dir if missing, otherwise updates it, and forces the working tree
// to the remote ref (discarding local changes and untracked files). It returns the commit SHA.
func (c *Client) Sync(ctx context.Context, dir string, r Remote, ref string) (string, error) {
	if err := c.validateURL(r.URL); err != nil {
		return "", err
	}
	if ref == "" {
		ref = "HEAD"
	}
	if err := c.validateRef(ctx, ref); err != nil {
		return "", err
	}
	_, statErr := os.Stat(filepath.Join(dir, ".git"))
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("create dir: %w", err)
		}
		if _, err := c.run(ctx, dir, nil, nil, "init", "-q"); err != nil {
			return "", err
		}
		if _, err := c.run(ctx, dir, nil, nil, "remote", "add", "origin", r.URL); err != nil {
			return "", err
		}
	case statErr != nil:
		return "", fmt.Errorf("stat repo: %w", statErr)
	default:
		if _, err := c.run(ctx, dir, nil, nil, "remote", "set-url", "origin", r.URL); err != nil {
			return "", err
		}
	}
	if _, err := c.run(ctx, dir, &r, nil, "fetch", "--force", "--prune", "--tags", "origin", ref); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dir, nil, nil, "checkout", "-q", "-f", "-B", "acp-sync", "FETCH_HEAD"); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dir, nil, nil, "clean", "-fdxq"); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

// Head returns the current commit SHA of dir.
func (c *Client) Head(ctx context.Context, dir string) (string, error) {
	out, err := c.run(ctx, dir, nil, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// CreateOrCheckoutBranch checks out branch, creating it from the current HEAD if it does not exist.
func (c *Client) CreateOrCheckoutBranch(ctx context.Context, dir, branch string) error {
	if err := c.validateBranch(ctx, branch); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, nil, nil, "checkout", "-q", "-B", branch)
	return err
}

// Commit stages files (relative to dir) and commits them, returning the new SHA.
func (c *Client) Commit(ctx context.Context, dir string, files []string, name, email, message string, signoff bool) (string, error) {
	if err := validatePaths(files); err != nil {
		return "", err
	}
	if name == "" || email == "" || message == "" || strings.ContainsAny(name+email, "\n\x00<>") {
		return "", invalid("author or message")
	}
	if _, err := c.run(ctx, dir, nil, nil, append([]string{"add", "-A", "--"}, files...)...); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dir, nil, nil, append([]string{"diff", "--cached", "--quiet", "--"}, files...)...); err == nil {
		return "", ErrNothingToCommit
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
	args := []string{"commit", "-q", "-m", message}
	if signoff {
		args = append(args, "-s")
	}
	args = append(args, "--")
	args = append(args, files...)
	if _, err := c.run(ctx, dir, nil, env, args...); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

// Push pushes HEAD to branch on the remote without force.
func (c *Client) Push(ctx context.Context, dir string, r Remote, branch string) error {
	if err := c.validateURL(r.URL); err != nil {
		return err
	}
	if err := c.validateBranch(ctx, branch); err != nil {
		return err
	}
	_, err := c.run(ctx, dir, &r, nil, "push", r.URL, "HEAD:refs/heads/"+branch)
	if err == nil {
		return nil
	}
	var ge *GitError
	if errors.As(err, &ge) {
		s := ge.Stderr
		switch {
		case strings.Contains(s, "non-fast-forward") || strings.Contains(s, "fetch first") || strings.Contains(s, "remote contains work"):
			return fmt.Errorf("%w: %s", ErrNonFastForward, s)
		case strings.Contains(s, "rejected") || strings.Contains(s, "cannot lock ref"):
			return fmt.Errorf("%w: %s", ErrConflict, s)
		}
	}
	return err
}
