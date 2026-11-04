package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Sync triggers recorded with the sync result and in the audit log.
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
)

// Defaults for the scheduler; concurrency stays low for small hosts.
const (
	DefaultScheduleTick        = 30 * time.Second
	DefaultScheduleConcurrency = 2
)

// Due identifies a project claimed for a scheduled sync.
type Due struct{ Workspace, ID uuid.UUID }

// ClaimDue leases up to limit projects whose sync interval has elapsed. The
// lease is a single UPDATE ... RETURNING, so concurrent app instances never
// receive the same project for the same interval.
func (s *Service) ClaimDue(ctx context.Context, limit int) ([]Due, error) {
	rows, err := s.o.Store.ClaimDueProjects(ctx, int32(limit)) //nolint:gosec // small bounded value
	if err != nil {
		return nil, fmt.Errorf("claim due projects: %w", err)
	}
	out := make([]Due, 0, len(rows))
	for _, r := range rows {
		out = append(out, Due{Workspace: r.WorkspaceID, ID: r.ID})
	}
	return out, nil
}

// syncTriggered runs a sync that was not started by a user and audits it.
// actor is the audit label (for example "schedule" or "webhook:github").
func (s *Service) syncTriggered(ctx context.Context, ws, id uuid.UUID, trigger, actor string) error {
	rev, err := s.syncAs(ctx, ws, id, trigger)
	action, details := "project.sync", map[string]any{"trigger": trigger, "sha": rev.SHA}
	if err != nil {
		action = "project.sync_failed"
		details = map[string]any{"trigger": trigger}
		var re *RemoteError
		if errors.As(err, &re) {
			details["error"] = re.Msg
		} else {
			details["error"] = err.Error()
		}
	}
	b, _ := json.Marshal(details)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, aerr := s.o.Store.InsertAudit(actx, dbgen.InsertAuditParams{
		ActorLabel: actor, Action: action, TargetType: "project", TargetID: id.String(),
		WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: b,
	})
	if aerr != nil {
		slog.Error("audit write failed", "action", action, "error", aerr)
	}
	return err
}

// RunScheduler syncs due projects until ctx is cancelled. Zero tick or
// concurrency select the defaults.
func (s *Service) RunScheduler(ctx context.Context, tick time.Duration, concurrency int, log *slog.Logger) {
	if tick <= 0 {
		tick = DefaultScheduleTick
	}
	if concurrency <= 0 {
		concurrency = DefaultScheduleConcurrency
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		s.RunDue(ctx, concurrency, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunDue claims and syncs due projects once and waits for them to finish.
func (s *Service) RunDue(ctx context.Context, concurrency int, log *slog.Logger) {
	due, err := s.ClaimDue(ctx, concurrency)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("scheduled sync: claim failed", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, d := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.syncTriggered(ctx, d.Workspace, d.ID, TriggerSchedule, "schedule")
			if err != nil && !errors.Is(err, ErrSyncInProgress) && ctx.Err() == nil {
				log.Warn("scheduled sync failed", "project", d.ID, "error", err)
			}
		}()
	}
	wg.Wait()
}
