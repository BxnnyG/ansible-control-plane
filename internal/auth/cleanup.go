package auth

import (
	"context"
	"log/slog"
	"time"
)

// DefaultCleanupInterval is how often expired sessions are purged.
const DefaultCleanupInterval = 15 * time.Minute

// SessionPurger deletes expired sessions and returns the number removed.
type SessionPurger interface {
	DeleteExpiredSessions(ctx context.Context) (int64, error)
}

// RunSessionCleanup purges expired sessions every interval until ctx is
// cancelled. It logs only counts, never session data.
func RunSessionCleanup(ctx context.Context, store SessionPurger, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		interval = DefaultCleanupInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := store.DeleteExpiredSessions(ctx)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				log.Error("session cleanup failed", "error", err)
			case n > 0:
				log.Info("expired sessions removed", "count", n)
			}
		}
	}
}
