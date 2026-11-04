package projects

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/google/uuid"
)

// WithCheckout calls fn with the cache clone of the project and the SHA of its
// latest recorded revision. The per-project lock is held for the duration, so
// a concurrent sync cannot change the tree under fn. fn gets a root that is
// confined to the checkout: paths cannot escape it, not even through symlinks.
// It returns ErrNotFound if the project does not exist or was never synced.
func (s *Service) WithCheckout(ctx context.Context, ws, id uuid.UUID, fn func(root *os.Root, sha string) error) error {
	if _, err := s.load(ctx, ws, id); err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	rev, err := s.o.Store.GetLatestProjectRevision(ctx, id) // read under the lock: sync may have finished meanwhile
	if err != nil {
		return mapErr(err)
	}
	root, err := os.OpenRoot(s.cacheDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("open checkout: %w", err)
	}
	defer func() { _ = root.Close() }()
	return fn(root, rev.Sha)
}
