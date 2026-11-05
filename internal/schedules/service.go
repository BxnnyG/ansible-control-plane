package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// Errors returned by the Service.
var ErrNotFound = errors.New("not found")

// Missed-run policies (schedules.missed_policy).
const (
	PolicySkip    = "skip"
	PolicyRunOnce = "run_once"
)

// DefaultTick is the scheduler poll interval; DefaultMissedGrace is how far a
// due time may lie in the past before the run counts as missed.
const (
	DefaultTick        = 30 * time.Second
	DefaultMissedGrace = 90 * time.Second
	claimBatch         = 50
)

// DB is the database access of the Service; *pgxpool.Pool implements it.
type DB interface {
	dbgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Runner is the part of the runs service used here.
type Runner interface {
	Enqueue(ctx context.Context, ws, templateID uuid.UUID, ov runs.Overrides, trigger string, actor runs.Actor, scheduleID *uuid.UUID) (runs.Run, error)
	ValidateOverrides(ctx context.Context, ws, templateID uuid.UUID, ov runs.Overrides) error
}

// Options configures a Service.
type Options struct {
	DB     DB
	Runs   Runner
	Secret SecretStore // template webhooks; nil disables them
	// MissedGrace overrides DefaultMissedGrace when positive.
	MissedGrace time.Duration
	Now         func() time.Time
}

// Service manages schedules and runs the scheduler loop.
type Service struct {
	db    DB
	q     *dbgen.Queries
	runs  Runner
	sec   SecretStore
	grace time.Duration
	now   func() time.Time
}

// New builds a Service.
func New(o Options) *Service {
	s := &Service{db: o.DB, q: dbgen.New(o.DB), runs: o.Runs, sec: o.Secret, grace: o.MissedGrace, now: o.Now}
	if s.grace <= 0 {
		s.grace = DefaultMissedGrace
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// Schedule is the API view of a schedule.
type Schedule struct {
	ID           uuid.UUID      `json:"id"`
	TemplateID   uuid.UUID      `json:"template_id"`
	Name         string         `json:"name"`
	Cron         string         `json:"cron"`
	Timezone     string         `json:"timezone"`
	Enabled      bool           `json:"enabled"`
	MissedPolicy string         `json:"missed_policy"`
	Overrides    runs.Overrides `json:"overrides"`
	NextRunAt    *time.Time     `json:"next_run_at"`
	LastRunAt    *time.Time     `json:"last_run_at"`
	CreatedBy    *uuid.UUID     `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// Input carries create and update fields; nil fields keep the current value
// on update and take the default on create.
type Input struct {
	Name         *string         `json:"name"`
	Cron         *string         `json:"cron"`
	Timezone     *string         `json:"timezone"`
	Enabled      *bool           `json:"enabled"`
	MissedPolicy *string         `json:"missed_policy"`
	Overrides    *runs.Overrides `json:"overrides"`
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func toSchedule(r dbgen.Schedule) (Schedule, error) {
	var ov runs.Overrides
	if len(r.Overrides) > 0 {
		if err := json.Unmarshal(r.Overrides, &ov); err != nil {
			return Schedule{}, fmt.Errorf("decode schedule overrides: %w", err)
		}
	}
	var by *uuid.UUID
	if r.CreatedBy.Valid {
		id := uuid.UUID(r.CreatedBy.Bytes)
		by = &id
	}
	return Schedule{ID: r.ID, TemplateID: r.TemplateID, Name: r.Name, Cron: r.CronExpr, Timezone: r.Timezone,
		Enabled: r.Enabled, MissedPolicy: r.MissedPolicy, Overrides: ov, NextRunAt: tsPtr(r.NextRunAt),
		LastRunAt: tsPtr(r.LastRunAt), CreatedBy: by, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// resolved is a validated schedule definition.
type resolved struct {
	name, cron, tz, policy string
	enabled                bool
	ov                     runs.Overrides
	next                   pgtype.Timestamptz
}

func (s *Service) resolve(ctx context.Context, ws, tpl uuid.UUID, cur *Schedule, in Input) (resolved, error) {
	r := resolved{cron: "", tz: "UTC", policy: PolicySkip, enabled: true}
	if cur != nil {
		r = resolved{name: cur.Name, cron: cur.Cron, tz: cur.Timezone, policy: cur.MissedPolicy, enabled: cur.Enabled, ov: cur.Overrides}
	}
	if in.Name != nil {
		r.name = strings.TrimSpace(*in.Name)
	}
	if in.Cron != nil {
		r.cron = strings.TrimSpace(*in.Cron)
	}
	if in.Timezone != nil {
		r.tz = strings.TrimSpace(*in.Timezone)
	}
	if in.Enabled != nil {
		r.enabled = *in.Enabled
	}
	if in.MissedPolicy != nil {
		r.policy = *in.MissedPolicy
	}
	if in.Overrides != nil {
		r.ov = *in.Overrides
	}
	if len(r.name) > 200 {
		return r, invalid("name is too long")
	}
	if r.policy != PolicySkip && r.policy != PolicyRunOnce {
		return r, invalid("missed_policy must be skip or run_once")
	}
	c, err := ParseCron(r.cron)
	if err != nil {
		return r, err
	}
	loc, err := LoadLocation(r.tz)
	if err != nil {
		return r, err
	}
	if err := s.runs.ValidateOverrides(ctx, ws, tpl, r.ov); err != nil {
		if errors.Is(err, runs.ErrNotFound) {
			return r, ErrNotFound
		}
		if errors.Is(err, runs.ErrInvalid) {
			return r, fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), runs.ErrInvalid.Error()+": "))
		}
		return r, err
	}
	if r.enabled {
		n := c.Next(s.now().In(loc))
		if n.IsZero() {
			return r, invalid("the cron expression never matches")
		}
		r.next = pgtype.Timestamptz{Time: n, Valid: true}
	}
	return r, nil
}

func (s *Service) template(ctx context.Context, ws, id uuid.UUID) error {
	if _, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws}); err != nil {
		return mapErr(err)
	}
	return nil
}

// Create adds a schedule to a template.
func (s *Service) Create(ctx context.Context, ws, tpl, createdBy uuid.UUID, in Input) (Schedule, error) {
	if err := s.template(ctx, ws, tpl); err != nil {
		return Schedule{}, err
	}
	if in.Cron == nil {
		return Schedule{}, invalid("cron is required")
	}
	r, err := s.resolve(ctx, ws, tpl, nil, in)
	if err != nil {
		return Schedule{}, err
	}
	ov, _ := json.Marshal(r.ov)
	row, err := s.q.CreateSchedule(ctx, dbgen.CreateScheduleParams{WorkspaceID: ws, TemplateID: tpl, Name: r.name,
		CronExpr: r.cron, Timezone: r.tz, Enabled: r.enabled, MissedPolicy: r.policy, Overrides: ov, NextRunAt: r.next,
		CreatedBy: pgtype.UUID{Bytes: createdBy, Valid: createdBy != uuid.Nil}})
	if err != nil {
		return Schedule{}, fmt.Errorf("create schedule: %w", mapErr(err))
	}
	return toSchedule(row)
}

// List returns the schedules of a template.
func (s *Service) List(ctx context.Context, ws, tpl uuid.UUID) ([]Schedule, error) {
	if err := s.template(ctx, ws, tpl); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTemplateSchedules(ctx, dbgen.ListTemplateSchedulesParams{TemplateID: tpl, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	out := make([]Schedule, 0, len(rows))
	for _, r := range rows {
		v, err := toSchedule(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Get returns one schedule.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Schedule, error) {
	row, err := s.q.GetSchedule(ctx, dbgen.GetScheduleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Schedule{}, mapErr(err)
	}
	return toSchedule(row)
}

// Update changes a schedule and recomputes its next run from now.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in Input) (Schedule, error) {
	cur, err := s.Get(ctx, ws, id)
	if err != nil {
		return Schedule{}, err
	}
	r, err := s.resolve(ctx, ws, cur.TemplateID, &cur, in)
	if err != nil {
		return Schedule{}, err
	}
	ov, _ := json.Marshal(r.ov)
	row, err := s.q.UpdateSchedule(ctx, dbgen.UpdateScheduleParams{ID: id, WorkspaceID: ws, Name: r.name, CronExpr: r.cron,
		Timezone: r.tz, Enabled: r.enabled, MissedPolicy: r.policy, Overrides: ov, NextRunAt: r.next})
	if err != nil {
		return Schedule{}, fmt.Errorf("update schedule: %w", mapErr(err))
	}
	return toSchedule(row)
}

// Delete removes a schedule; runs it created stay in the history.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteSchedule(ctx, dbgen.DeleteScheduleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Preview returns the next n run times (1-20) of an expression.
func (s *Service) Preview(expr, tz string, n int) ([]time.Time, error) {
	if n < 1 || n > 20 {
		return nil, invalid("n must be between 1 and 20")
	}
	return NextRuns(expr, tz, s.now(), n)
}

// RunDue claims the due schedules once, enqueues their runs and advances them.
// It returns the number of runs enqueued.
func (s *Service) RunDue(ctx context.Context, log *slog.Logger) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin scheduler tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := dbgen.New(tx)
	due, err := q.ClaimDueSchedules(ctx, claimBatch)
	if err != nil {
		return 0, fmt.Errorf("claim due schedules: %w", err)
	}
	now, started := s.now(), 0
	for _, row := range due {
		if s.fire(ctx, q, row, now, log) {
			started++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit scheduler tx: %w", err)
	}
	return started, nil
}

// fire handles one claimed schedule and reports whether a run was enqueued.
func (s *Service) fire(ctx context.Context, q *dbgen.Queries, row dbgen.Schedule, now time.Time, log *slog.Logger) bool {
	next := pgtype.Timestamptz{}
	loc, lerr := LoadLocation(row.Timezone)
	c, cerr := ParseCron(row.CronExpr)
	if lerr != nil || cerr != nil { // corrupt definition: never retry in a loop
		s.disable(ctx, q, row, "invalid schedule definition", log)
		return false
	}
	if n := c.Next(now.In(loc)); !n.IsZero() {
		next = pgtype.Timestamptz{Time: n, Valid: true}
	}
	missed := row.NextRunAt.Valid && now.Sub(row.NextRunAt.Time) > s.grace
	run := !missed || row.MissedPolicy == PolicyRunOnce
	enqueued := false
	if run {
		var ov runs.Overrides
		_ = json.Unmarshal(row.Overrides, &ov)
		actor := runs.Actor{}
		if row.CreatedBy.Valid {
			actor.UserID = uuid.UUID(row.CreatedBy.Bytes)
		}
		id := row.ID
		r, err := s.runs.Enqueue(ctx, row.WorkspaceID, row.TemplateID, ov, runs.TriggerSchedule, actor, &id)
		switch {
		case err == nil:
			enqueued = true
			s.audit(ctx, q, row, "schedule.run", map[string]any{"run_id": r.ID, "missed": missed})
		case errors.Is(err, runs.ErrNotFound):
			s.disable(ctx, q, row, "template no longer exists", log)
			return false
		default:
			log.Warn("scheduled run not started", "schedule", row.ID, "error", err)
			s.audit(ctx, q, row, "schedule.run_failed", map[string]any{"error": err.Error()})
		}
	} else {
		s.audit(ctx, q, row, "schedule.run_skipped", map[string]any{"due": row.NextRunAt.Time})
	}
	var last pgtype.Timestamptz
	if enqueued {
		last = pgtype.Timestamptz{Time: now, Valid: true}
	}
	if err := q.AdvanceSchedule(ctx, dbgen.AdvanceScheduleParams{ID: row.ID, NextRunAt: next, LastRunAt: last}); err != nil {
		log.Error("advance schedule", "schedule", row.ID, "error", err)
	}
	return enqueued
}

func (s *Service) disable(ctx context.Context, q *dbgen.Queries, row dbgen.Schedule, why string, log *slog.Logger) {
	log.Warn("schedule disabled", "schedule", row.ID, "reason", why)
	if err := q.DisableSchedule(ctx, row.ID); err != nil {
		log.Error("disable schedule", "schedule", row.ID, "error", err)
		return
	}
	s.audit(ctx, q, row, "schedule.disabled", map[string]any{"reason": why})
}

func (s *Service) audit(ctx context.Context, q *dbgen.Queries, row dbgen.Schedule, action string, details map[string]any) {
	b, _ := json.Marshal(details)
	_, err := q.InsertAudit(ctx, dbgen.InsertAuditParams{ActorLabel: "schedule", Action: action, TargetType: "schedule",
		TargetID: row.ID.String(), WorkspaceID: pgtype.UUID{Bytes: row.WorkspaceID, Valid: true}, Details: b})
	if err != nil {
		slog.Error("audit write failed", "action", action, "error", err)
	}
}

// Run polls for due schedules until ctx is cancelled. A zero tick selects
// DefaultTick.
func (s *Service) Run(ctx context.Context, tick time.Duration, log *slog.Logger) {
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.RunDue(ctx, log); err != nil && ctx.Err() == nil {
			log.Error("scheduler tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
