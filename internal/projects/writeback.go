package projects

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
)

// ErrGitConflict is matched (errors.Is) by every ConflictError.
var ErrGitConflict = errors.New("git conflict")

// ConflictError reports that a write-back cannot be applied because the
// target branch moved. Nothing is merged automatically: the caller has to
// re-sync and re-apply its change.
type ConflictError struct {
	// Paths lists the files that changed on the branch since the base
	// revision (empty when the remote moved during the push).
	Paths  []string
	Reason string
}

func (e *ConflictError) Error() string {
	if len(e.Paths) == 0 {
		return "git conflict: " + e.Reason
	}
	return "git conflict: " + e.Reason + ": " + strings.Join(e.Paths, ", ")
}

// Is makes errors.Is(err, ErrGitConflict) work.
func (e *ConflictError) Is(target error) bool { return target == ErrGitConflict }

// FileChange is one file to write (or delete) in a write-back. Path is
// relative to the repository root.
type FileChange struct {
	Path    string
	Content []byte
	Delete  bool
}

// Plan is the outcome of a PlanFunc.
type Plan struct {
	Changes []FileChange
	// Watch lists extra files or directories whose modification on the write
	// branch since the base revision also counts as a conflict (for example
	// the vars directories that were merged into the edited model).
	Watch []string
}

// PlanFunc computes the files to write. It reads the repository at the base
// revision through base (confined to the checkout, read-only by convention).
type PlanFunc func(base *os.Root) (Plan, error)

// Author is the identity of a write-back commit.
type Author struct{ Name, Email string }

// WriteRequest describes one write-back (ADR-0014): commit the planned files
// on Branch and push without force.
type WriteRequest struct {
	Branch  string
	BaseSHA string // revision the user edited; the branch is created from it
	Message string
	Author  Author
	Plan    PlanFunc
}

// WriteResult is the outcome of a write-back.
type WriteResult struct {
	Branch  string   `json:"branch"`
	SHA     string   `json:"sha"`            // new head of the branch; the base revision again on NoOp
	Paths   []string `json:"paths"`          // files committed
	Created bool     `json:"branch_created"` // the branch did not exist on the remote
	NoOp    bool     `json:"no_op"`          // the plan did not change any file
}

const maxWriteFileSize = 8 << 20

// WriteBack commits the files of req.Plan to req.Branch of the project's
// remote. Under the project lock it fetches the branch, verifies that the
// files in play are unchanged since req.BaseSHA, writes them (confined to the
// checkout), commits as the author with a sign-off and pushes without force.
// A moved branch yields a *ConflictError. Afterwards the cache checkout is put
// back on the project's latest synced revision.
func (s *Service) WriteBack(ctx context.Context, ws, id uuid.UUID, req WriteRequest) (WriteResult, error) {
	var res WriteResult
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return res, err
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" || len(msg) > 4000 || strings.ContainsRune(msg, 0) {
		return res, invalid("commit message must be 1-4000 characters")
	}
	if req.Plan == nil || req.Branch == "" {
		return res, invalid("branch and plan are required")
	}
	r, wipe, err := s.remote(ctx, row)
	if err != nil {
		return res, err
	}
	defer wipe()

	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	rev, err := s.o.Store.GetLatestProjectRevision(ctx, id)
	if err != nil {
		return res, mapErr(err)
	}
	dir := s.cacheDir(id)
	if _, err := os.Stat(dir); err != nil {
		return res, ErrNotFound
	}
	rctx, cancel := context.WithTimeout(ctx, s.o.SyncTimeout)
	defer cancel()
	defer s.restore(ctx, dir, rev.Sha)

	head, exists, err := s.o.Git.FetchBranch(rctx, dir, r, req.Branch)
	if err != nil {
		return res, writeErr(err)
	}
	if !s.o.Git.HasCommit(rctx, dir, req.BaseSHA) {
		return res, &ConflictError{Reason: "the base revision is no longer available; re-import the inventory"}
	}
	plan, err := s.planAt(rctx, dir, req.BaseSHA, req.Plan)
	if err != nil {
		return res, err
	}
	paths, err := validateChanges(plan.Changes)
	if err != nil {
		return res, err
	}
	res.Branch, res.SHA, res.Created = req.Branch, req.BaseSHA, !exists
	if len(paths) == 0 {
		res.NoOp = true
		return res, nil
	}
	target := req.BaseSHA
	if exists {
		target = head
		check := append(append([]string{}, paths...), plan.Watch...)
		changed, err := s.o.Git.ChangedPaths(rctx, dir, req.BaseSHA, head, check)
		if err != nil {
			return res, writeErr(err)
		}
		if len(changed) > 0 {
			return res, &ConflictError{Paths: changed, Reason: "files changed on the write branch since the edited revision"}
		}
	}
	if err := s.o.Git.CheckoutDetached(rctx, dir, target); err != nil {
		return res, writeErr(err)
	}
	if err := applyChanges(dir, plan.Changes); err != nil {
		return res, err
	}
	sha, err := s.o.Git.Commit(rctx, dir, paths, req.Author.Name, req.Author.Email, msg, true)
	if errors.Is(err, gitops.ErrNothingToCommit) {
		res.NoOp, res.SHA, res.Created = true, target, false
		return res, nil
	}
	if err != nil {
		return res, writeErr(err)
	}
	if err := s.o.Git.Push(rctx, dir, r, req.Branch); err != nil {
		if errors.Is(err, gitops.ErrNonFastForward) || errors.Is(err, gitops.ErrConflict) {
			return res, &ConflictError{Reason: "the write branch moved on the remote; re-sync and apply the change again"}
		}
		return res, writeErr(err)
	}
	res.SHA, res.Paths = sha, paths
	return res, nil
}

// Preview returns the unified diff of what a write-back of plan would change
// relative to the base revision. Nothing is committed or pushed.
func (s *Service) Preview(ctx context.Context, ws, id uuid.UUID, baseSHA string, plan PlanFunc) (paths []string, diff string, err error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return nil, "", err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	rev, err := s.o.Store.GetLatestProjectRevision(ctx, id)
	if err != nil {
		return nil, "", mapErr(err)
	}
	dir := s.cacheDir(id)
	if _, err := os.Stat(dir); err != nil {
		return nil, "", ErrNotFound
	}
	rctx, cancel := context.WithTimeout(ctx, s.o.SyncTimeout)
	defer cancel()
	defer s.restore(ctx, dir, rev.Sha)
	if !s.o.Git.HasCommit(rctx, dir, baseSHA) {
		return nil, "", &ConflictError{Reason: "the base revision is no longer available; re-import the inventory"}
	}
	p, err := s.planAt(rctx, dir, baseSHA, plan)
	if err != nil {
		return nil, "", err
	}
	paths, err = validateChanges(p.Changes)
	if err != nil || len(paths) == 0 {
		return nil, "", err
	}
	if err := applyChanges(dir, p.Changes); err != nil {
		return nil, "", err
	}
	diff, err = s.o.Git.StagedDiff(rctx, dir, paths)
	if err != nil {
		return nil, "", writeErr(err)
	}
	return paths, diff, nil
}

// planAt checks out base and runs plan against it.
func (s *Service) planAt(ctx context.Context, dir, base string, plan PlanFunc) (Plan, error) {
	if err := s.o.Git.CheckoutDetached(ctx, dir, base); err != nil {
		return Plan{}, writeErr(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Plan{}, fmt.Errorf("open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	return plan(root)
}

// restore puts the cache clone back on the latest synced revision, so scans
// and imports are unaffected by a write-back. It needs no network.
func (s *Service) restore(ctx context.Context, dir, sha string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.o.Git.Reset(rctx, dir, sha); err != nil {
		// Without a restored tree the next sync repairs the clone.
		_ = os.RemoveAll(dir)
	}
}

func writeErr(err error) error {
	if errors.Is(err, gitops.ErrInvalidInput) {
		return invalid("%v", err)
	}
	return remoteErr(err)
}

// validateChanges checks and normalizes the paths of a plan and returns them
// sorted and unique.
func validateChanges(changes []FileChange) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	for _, c := range changes {
		p := path.Clean(c.Path)
		if c.Path == "" || p != c.Path || !fs.ValidPath(p) || p == "." {
			return nil, invalid("path %q is not acceptable", c.Path)
		}
		for _, part := range strings.Split(p, "/") {
			if strings.EqualFold(part, ".git") {
				return nil, invalid("path %q is not acceptable", c.Path)
			}
		}
		if len(c.Content) > maxWriteFileSize {
			return nil, invalid("%s is larger than %d MiB", p, maxWriteFileSize>>20)
		}
		if seen[p] {
			return nil, invalid("path %q is listed twice", p)
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// applyChanges writes the changes into dir. All access goes through an
// os.Root, and existing path components must not be symlinks, so a change can
// neither leave the checkout nor modify a file other than the committed one.
func applyChanges(dir string, changes []FileChange) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, c := range changes {
		if err := noSymlinks(root, c.Path); err != nil {
			return err
		}
		if c.Delete {
			if err := root.Remove(c.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", c.Path, err)
			}
			continue
		}
		if d := path.Dir(c.Path); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", c.Path, err)
			}
		}
		if err := root.WriteFile(c.Path, c.Content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", c.Path, err)
		}
	}
	return nil
}

func noSymlinks(root *os.Root, p string) error {
	parts := strings.Split(p, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(sub)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", sub, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 || (i == len(parts)-1 && info.IsDir()) {
			return invalid("%s is a symlink or directory and cannot be written", sub)
		}
	}
	return nil
}
