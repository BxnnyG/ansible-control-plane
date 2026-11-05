package runs

import (
	"context"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runstream"
)

// notifyList wakes the run list and run streams after a status change made by
// the app (enqueue, cancel). Best effort: streams also re-read periodically.
func (s *Service) notifyList(ctx context.Context, id uuid.UUID) {
	ctx = context.WithoutCancel(ctx)
	_ = s.q.NotifyRun(ctx, dbgen.NotifyRunParams{Channel: runstream.ListChannel, Payload: id.String()})
	_ = s.q.NotifyRun(ctx, dbgen.NotifyRunParams{Channel: runstream.RunChannel(id), Payload: "status"})
}
