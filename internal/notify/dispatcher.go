package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// Dispatcher defaults (ADR-0021).
const (
	DefaultTick   = 5 * time.Second
	PruneInterval = 24 * time.Hour
	MaxAttempts   = 5
	eventBatch    = 50
	deliveryBatch = 10
	deliveryLease = 2 * time.Minute
	baseBackoff   = 30 * time.Second
	maxBackoff    = 30 * time.Minute
)

// Backoff is the delay before the next attempt after attempt n (1-based) failed.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := baseBackoff
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 4
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

// Dispatcher turns outbox events into deliveries and sends them.
type Dispatcher struct {
	pool      *pgxpool.Pool
	q         *dbgen.Queries
	secrets   SecretResolver
	senders   func(string) (Sender, error)
	publicURL string
	retention int // days; <= 0 keeps outbox and delivery rows forever
	now       func() time.Time
}

// DispatcherOptions configures New.
type DispatcherOptions struct {
	DB        *pgxpool.Pool
	Secrets   SecretResolver
	PublicURL string
	// RetentionDays is the run log retention; outbox and delivery rows older
	// than this are pruned. <= 0 disables pruning.
	RetentionDays int
	// Senders overrides the sender lookup (tests).
	Senders func(string) (Sender, error)
	Now     func() time.Time
}

// New creates a Dispatcher.
func New(o DispatcherOptions) *Dispatcher {
	d := &Dispatcher{pool: o.DB, q: dbgen.New(o.DB), secrets: o.Secrets, senders: o.Senders,
		publicURL: o.PublicURL, retention: o.RetentionDays, now: o.Now}
	if d.senders == nil {
		d.senders = SenderFor
	}
	if d.now == nil {
		d.now = time.Now
	}
	return d
}

func pgTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// Run polls until ctx is cancelled. A zero tick selects DefaultTick.
func (d *Dispatcher) Run(ctx context.Context, tick time.Duration, log *slog.Logger) {
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	nextPrune := time.Time{}
	for {
		if _, err := d.ProcessEvents(ctx, log); err != nil && ctx.Err() == nil {
			log.Error("notification event processing failed", "error", err)
		}
		if _, err := d.SendDue(ctx, log); err != nil && ctx.Err() == nil {
			log.Error("notification delivery failed", "error", err)
		}
		if now := d.now(); !now.Before(nextPrune) {
			nextPrune = now.Add(PruneInterval)
			if n, err := d.Prune(ctx); err != nil && ctx.Err() == nil {
				log.Error("notification pruning failed", "error", err)
			} else if n > 0 {
				log.Info("old notification events removed", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ProcessEvents claims a batch of outbox events, derives the kinds, matches
// rules and creates deliveries, all in one transaction. It returns the number
// of events processed.
func (d *Dispatcher) ProcessEvents(ctx context.Context, log *slog.Logger) (int, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin notify tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := d.q.WithTx(tx)
	evs, err := q.ClaimNotificationEvents(ctx, eventBatch)
	if err != nil {
		return 0, fmt.Errorf("claim notification events: %w", err)
	}
	for _, ev := range evs {
		// A savepoint keeps one broken event from aborting the whole batch.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return 0, fmt.Errorf("begin savepoint: %w", err)
		}
		if err := d.expand(ctx, d.q.WithTx(sp), ev); err != nil {
			_ = sp.Rollback(ctx)
			log.Error("notification event skipped", "event", ev.ID, "error", err)
		} else if err := sp.Commit(ctx); err != nil {
			return 0, fmt.Errorf("release savepoint: %w", err)
		}
		if err := q.MarkNotificationEventProcessed(ctx, ev.ID); err != nil {
			return 0, fmt.Errorf("mark event processed: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit notify tx: %w", err)
	}
	return len(evs), nil
}

func (d *Dispatcher) expand(ctx context.Context, q *dbgen.Queries, ev dbgen.NotificationEvent) error {
	run, err := q.GetRunForNotification(ctx, ev.RunID)
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	facts := RunFacts{Status: run.Status, HostsChanged: int(run.HostsChanged)}
	if p, err := runs.DecodeParams(run.Params); err == nil {
		facts.Check = p.Check
	}
	if run.TemplateID.Valid {
		prev, err := q.GetPreviousTerminalRunStatus(ctx, run.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("previous run: %w", err)
		}
		facts.PrevStatus = prev
	}
	derived := DeriveKinds(facts)
	rules, err := q.ListMatchingNotificationRules(ctx, dbgen.ListMatchingNotificationRulesParams{
		WorkspaceID: run.WorkspaceID, TemplateID: run.TemplateID})
	if err != nil {
		return fmt.Errorf("match rules: %w", err)
	}
	subs := map[uuid.UUID]map[string]bool{}
	for _, r := range rules {
		m := subs[r.ChannelID]
		if m == nil {
			m = map[string]bool{}
			subs[r.ChannelID] = m
		}
		for _, e := range r.Events {
			m[e] = true
		}
	}
	for ch, sub := range subs {
		kind := PickKind(derived, sub)
		if kind == "" {
			continue
		}
		if err := q.InsertNotificationDelivery(ctx, dbgen.InsertNotificationDeliveryParams{
			EventID: ev.ID, RunID: run.ID, ChannelID: ch, Kind: kind}); err != nil {
			return fmt.Errorf("insert delivery: %w", err)
		}
	}
	return nil
}

// SendDue claims due deliveries and sends them. The claim moves
// next_attempt_at forward as a lease, so a crash during a send only delays the
// retry. It returns the number of deliveries attempted.
func (d *Dispatcher) SendDue(ctx context.Context, log *slog.Logger) (int, error) {
	due, err := d.q.ClaimDueNotificationDeliveries(ctx, dbgen.ClaimDueNotificationDeliveriesParams{
		Limit: deliveryBatch, NextAttemptAt: pgTime(d.now().Add(deliveryLease))})
	if err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
	}
	for _, dl := range due {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		d.attempt(ctx, dl, log)
	}
	return len(due), nil
}

func (d *Dispatcher) attempt(ctx context.Context, dl dbgen.NotificationDelivery, log *slog.Logger) {
	sendErr := d.send(ctx, dl)
	bg := context.WithoutCancel(ctx)
	switch {
	case sendErr == nil:
		if err := d.q.MarkNotificationDeliverySent(bg, dl.ID); err != nil {
			log.Error("mark delivery sent failed", "delivery", dl.ID, "error", err)
		}
	case permanent(sendErr) || int(dl.Attempts) >= MaxAttempts:
		log.Warn("notification delivery failed", "delivery", dl.ID, "attempts", dl.Attempts, "error", sendErr)
		if err := d.q.MarkNotificationDeliveryFailed(bg, dbgen.MarkNotificationDeliveryFailedParams{ID: dl.ID, LastError: sendErr.Error()}); err != nil {
			log.Error("mark delivery failed failed", "delivery", dl.ID, "error", err)
		}
	default:
		if err := d.q.MarkNotificationDeliveryRetry(bg, dbgen.MarkNotificationDeliveryRetryParams{
			ID: dl.ID, NextAttemptAt: pgTime(d.now().Add(Backoff(int(dl.Attempts)))), LastError: sendErr.Error()}); err != nil {
			log.Error("schedule delivery retry failed", "delivery", dl.ID, "error", err)
		}
	}
}

type permanentError struct{ msg string }

func (e permanentError) Error() string { return e.msg }

func permanent(err error) bool { var p permanentError; return errors.As(err, &p) }

func (d *Dispatcher) send(ctx context.Context, dl dbgen.NotificationDelivery) error {
	ch, err := d.q.GetNotificationChannelByID(ctx, dl.ChannelID)
	if err != nil {
		return permanentError{"channel not found"}
	}
	if !ch.Enabled {
		return permanentError{"channel disabled"}
	}
	run, err := d.q.GetRunForNotification(ctx, dl.RunID)
	if err != nil {
		return permanentError{"run not found"}
	}
	ws, err := d.q.GetNotificationWorkspaceName(ctx, run.WorkspaceID)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	msg := buildMessage(run, ws, dl.Kind, d.publicURL)
	msg.TxnID = dl.ID.String()
	return sendVia(ctx, d.secrets, d.senders, ch, msg)
}

func buildMessage(run dbgen.Run, workspace, kind, publicURL string) Message {
	m := Message{Kind: kind, RunID: run.ID.String(), Workspace: workspace, Template: run.TemplateName,
		Status: run.Status, Trigger: run.Trigger, HostsOK: int(run.HostsOk), HostsChanged: int(run.HostsChanged),
		HostsFailed: int(run.HostsFailed), HostsUnreachable: int(run.HostsUnreachable),
		URL: RunURL(publicURL, run.ID.String())}
	if p, err := runs.DecodeParams(run.Params); err == nil {
		m.Check = p.Check
	}
	if run.FinishedAt.Valid {
		m.FinishedAt = run.FinishedAt.Time
		if run.StartedAt.Valid {
			m.Duration = run.FinishedAt.Time.Sub(run.StartedAt.Time)
		}
	}
	return m
}

// Prune removes processed outbox events (and their deliveries) older than the
// retention. It returns the number of events removed.
func (d *Dispatcher) Prune(ctx context.Context) (int64, error) {
	if d.retention <= 0 {
		return 0, nil
	}
	return d.q.DeleteOldNotificationEvents(ctx, pgTime(d.now().AddDate(0, 0, -d.retention)))
}
