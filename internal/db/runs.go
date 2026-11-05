package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// ErrNoRun is returned by ClaimRun when no queued run is currently eligible.
var ErrNoRun = errors.New("no claimable run")

// ClaimRun atomically claims the oldest eligible queued run for a worker. It
// serializes claims with a transaction-scoped advisory lock so that the
// per-template max_concurrent limit cannot be exceeded by racing workers.
func ClaimRun(ctx context.Context, pool *pgxpool.Pool, workerID string) (dbgen.Run, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbgen.Run{}, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	if err := q.LockRunClaim(ctx); err != nil {
		return dbgen.Run{}, fmt.Errorf("lock claim: %w", err)
	}
	run, err := q.ClaimRun(ctx, workerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Run{}, ErrNoRun
	}
	if err != nil {
		return dbgen.Run{}, fmt.Errorf("claim run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbgen.Run{}, fmt.Errorf("commit claim: %w", err)
	}
	return run, nil
}
