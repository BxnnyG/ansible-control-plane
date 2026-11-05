package runs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// RetentionInterval is how often old log chunks are purged.
const RetentionInterval = 24 * time.Hour

// LogPurger deletes log chunks of finished runs created before the cutoff
// and returns the number removed; *dbgen.Queries implements it.
type LogPurger interface {
	DeleteOldRunLogChunks(ctx context.Context, createdAt pgtype.Timestamptz) (int64, error)
}

// PurgeLogs runs one retention pass. days <= 0 keeps logs forever. Run rows
// and host results are never deleted (RUN-5).
func PurgeLogs(ctx context.Context, p LogPurger, days int, now time.Time) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	return p.DeleteOldRunLogChunks(ctx, pgtype.Timestamptz{Time: now.AddDate(0, 0, -days), Valid: true})
}

// RunLogRetention purges once at start and then every interval until ctx is
// cancelled. It logs only counts.
func RunLogRetention(ctx context.Context, p LogPurger, days int, interval time.Duration, log *slog.Logger) {
	if days <= 0 {
		log.Info("run log retention disabled: logs are kept forever")
		return
	}
	if interval <= 0 {
		interval = RetentionInterval
	}
	pass := func() {
		n, err := PurgeLogs(ctx, p, days, time.Now())
		switch {
		case ctx.Err() != nil:
		case err != nil:
			log.Error("run log retention failed", "error", err)
		case n > 0:
			log.Info("old run log chunks removed", "count", n, "retention_days", days)
		}
	}
	pass()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
