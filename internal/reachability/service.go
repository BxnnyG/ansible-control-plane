package reachability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Errors returned by the Service.
var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("conflict")
)

// Scheduler constants.
const (
	DefaultTick = 30 * time.Second
	claimBatch  = 20
)

// DB is the database access of the Service; *pgxpool.Pool implements it.
type DB interface {
	dbgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Prober starts built-in probe runs; *runs.Service implements it.
type Prober interface {
	EnqueueProbe(ctx context.Context, ws uuid.UUID, in runs.ProbeInput, trigger string, actor runs.Actor) (runs.Run, error)
}

// Options configures a Service.
type Options struct {
	DB   DB
	Runs Prober
	Now  func() time.Time
}

// Service manages probe definitions and runs the interval scheduler.
type Service struct {
	db   DB
	q    *dbgen.Queries
	runs Prober
	now  func() time.Time
}

// New builds a Service.
func New(o Options) *Service {
	s := &Service{db: o.DB, q: dbgen.New(o.DB), runs: o.Runs, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

func fromPg(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func toPg(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func mapErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return ErrConflict
		case "23503", "23514":
			return fmt.Errorf("%w: %s", ErrInvalid, pg.ConstraintName)
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Config is the per-workspace probe configuration.
type Config struct {
	InventoryID     *uuid.UUID `json:"inventory_id"`
	SSHSecretID     *uuid.UUID `json:"ssh_secret_id"`
	IntervalSeconds int        `json:"interval_seconds"`
	NextRunAt       *time.Time `json:"next_run_at"`
	LastRunAt       *time.Time `json:"last_run_at"`
}

// ConfigInput replaces the configuration. IntervalSeconds 0 switches the
// scheduler off; otherwise it must be 60-86400.
type ConfigInput struct {
	InventoryID     *uuid.UUID `json:"inventory_id"`
	SSHSecretID     *uuid.UUID `json:"ssh_secret_id"`
	IntervalSeconds int        `json:"interval_seconds"`
}

func toConfig(r dbgen.ReachabilityConfig) Config {
	return Config{
		InventoryID: fromPg(r.InventoryID), SSHSecretID: fromPg(r.SshSecretID), IntervalSeconds: int(r.IntervalSeconds),
		NextRunAt: tsPtr(r.NextRunAt), LastRunAt: tsPtr(r.LastRunAt),
	}
}

// GetConfig returns the configuration; a workspace without one gets the
// defaults (scheduler off).
func (s *Service) GetConfig(ctx context.Context, ws uuid.UUID) (Config, error) {
	r, err := s.q.GetReachabilityConfig(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("load reachability config: %w", err)
	}
	return toConfig(r), nil
}

// SetConfig validates and stores the configuration. Enabling the interval
// makes the first probe run due at once.
func (s *Service) SetConfig(ctx context.Context, ws uuid.UUID, in ConfigInput) (Config, error) {
	if in.IntervalSeconds != 0 && (in.IntervalSeconds < 60 || in.IntervalSeconds > 86400) {
		return Config{}, fmt.Errorf("%w: interval_seconds must be 0 or between 60 and 86400", ErrInvalid)
	}
	if in.IntervalSeconds > 0 && in.InventoryID == nil {
		return Config{}, fmt.Errorf("%w: an inventory is required to enable probing", ErrInvalid)
	}
	if in.InventoryID != nil {
		if _, err := s.q.GetInventory(ctx, dbgen.GetInventoryParams{ID: *in.InventoryID, WorkspaceID: ws}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Config{}, fmt.Errorf("%w: inventory not found", ErrInvalid)
			}
			return Config{}, fmt.Errorf("load inventory: %w", err)
		}
	}
	if in.SSHSecretID != nil {
		sec, err := s.q.GetSecret(ctx, dbgen.GetSecretParams{ID: *in.SSHSecretID, WorkspaceID: ws})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && sec.Type != secrets.TypeSSHKey) {
			return Config{}, fmt.Errorf("%w: ssh_secret_id must reference an ssh_key secret", ErrInvalid)
		}
		if err != nil {
			return Config{}, fmt.Errorf("load secret: %w", err)
		}
	}
	// Sources of another inventory cannot be probed in one run.
	old, err := s.GetConfig(ctx, ws)
	if err != nil {
		return Config{}, err
	}
	if !sameID(old.InventoryID, in.InventoryID) {
		srcs, err := s.q.ListReachabilitySources(ctx, ws)
		if err != nil {
			return Config{}, fmt.Errorf("list sources: %w", err)
		}
		if len(srcs) > 0 {
			return Config{}, fmt.Errorf("%w: remove the sources before changing the inventory", ErrConflict)
		}
	}
	r, err := s.q.UpsertReachabilityConfig(ctx, dbgen.UpsertReachabilityConfigParams{
		WorkspaceID: ws, InventoryID: toPg(in.InventoryID), SshSecretID: toPg(in.SSHSecretID), IntervalSeconds: int32(in.IntervalSeconds), //nolint:gosec // validated above
	})
	if err != nil {
		return Config{}, fmt.Errorf("store reachability config: %w", mapErr(err))
	}
	return toConfig(r), nil
}

// Source is a probe source: an inventory host.
type Source struct {
	ID        uuid.UUID `json:"id"`
	HostID    uuid.UUID `json:"host_id"`
	HostName  string    `json:"host_name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListSources returns the probe sources ordered by host name.
func (s *Service) ListSources(ctx context.Context, ws uuid.UUID) ([]Source, error) {
	rows, err := s.q.ListReachabilitySources(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	out := make([]Source, 0, len(rows))
	for _, r := range rows {
		out = append(out, Source{ID: r.ID, HostID: r.HostID, HostName: r.HostName, CreatedAt: r.CreatedAt.Time})
	}
	return out, nil
}

// CreateSource makes an inventory host of the configured inventory a probe
// source.
func (s *Service) CreateSource(ctx context.Context, ws, hostID uuid.UUID) (Source, error) {
	cfg, err := s.GetConfig(ctx, ws)
	if err != nil {
		return Source{}, err
	}
	if cfg.InventoryID == nil {
		return Source{}, fmt.Errorf("%w: configure the probe inventory first", ErrInvalid)
	}
	h, err := s.q.GetInventoryHost(ctx, dbgen.GetInventoryHostParams{ID: hostID, InventoryID: *cfg.InventoryID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, fmt.Errorf("%w: host is not in the probe inventory", ErrInvalid)
	}
	if err != nil {
		return Source{}, fmt.Errorf("load host: %w", err)
	}
	if !hostRe.MatchString(h.Name) {
		return Source{}, fmt.Errorf("%w: host name %q cannot be used as a probe source", ErrInvalid, h.Name)
	}
	r, err := s.q.CreateReachabilitySource(ctx, dbgen.CreateReachabilitySourceParams{WorkspaceID: ws, HostID: hostID})
	if err != nil {
		return Source{}, fmt.Errorf("create source: %w", mapErr(err))
	}
	return Source{ID: r.ID, HostID: r.HostID, HostName: h.Name, CreatedAt: r.CreatedAt.Time}, nil
}

// DeleteSource removes a probe source and its results.
func (s *Service) DeleteSource(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteReachabilitySource(ctx, dbgen.DeleteReachabilitySourceParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete source: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Target is a probe target.
type Target struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Check     string    `json:"check"`
	Port      *int      `json:"port"`
	DNSName   string    `json:"dns_name"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toTarget(r dbgen.ReachabilityTarget) Target {
	t := Target{ID: r.ID, Name: r.Name, Address: r.Address, Check: r.CheckType, DNSName: r.DnsName, Enabled: r.Enabled,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
	if r.Port.Valid {
		p := int(r.Port.Int32)
		t.Port = &p
	}
	return t
}

func portPg(p *int) pgtype.Int4 {
	if p == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*p), Valid: true} //nolint:gosec // validated 1-65535
}

// ListTargets returns the probe targets ordered by name.
func (s *Service) ListTargets(ctx context.Context, ws uuid.UUID) ([]Target, error) {
	rows, err := s.q.ListReachabilityTargets(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}
	out := make([]Target, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTarget(r))
	}
	return out, nil
}

// CreateTarget validates and stores a target; Enabled defaults to true.
func (s *Service) CreateTarget(ctx context.Context, ws uuid.UUID, in TargetInput) (Target, error) {
	if err := in.validate(); err != nil {
		return Target{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	r, err := s.q.CreateReachabilityTarget(ctx, dbgen.CreateReachabilityTargetParams{
		WorkspaceID: ws, Name: in.Name, Address: in.Address, CheckType: in.Check, Port: portPg(in.Port), DnsName: in.DNSName, Enabled: enabled,
	})
	if err != nil {
		return Target{}, fmt.Errorf("create target: %w", mapErr(err))
	}
	return toTarget(r), nil
}

// UpdateTarget replaces a target; Enabled nil keeps the current value.
func (s *Service) UpdateTarget(ctx context.Context, ws, id uuid.UUID, in TargetInput) (Target, error) {
	if err := in.validate(); err != nil {
		return Target{}, err
	}
	cur, err := s.q.GetReachabilityTarget(ctx, dbgen.GetReachabilityTargetParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Target{}, mapErr(err)
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	r, err := s.q.UpdateReachabilityTarget(ctx, dbgen.UpdateReachabilityTargetParams{
		ID: id, WorkspaceID: ws, Name: in.Name, Address: in.Address, CheckType: in.Check, Port: portPg(in.Port), DnsName: in.DNSName, Enabled: enabled,
	})
	if err != nil {
		return Target{}, fmt.Errorf("update target: %w", mapErr(err))
	}
	return toTarget(r), nil
}

// DeleteTarget removes a target and its results.
func (s *Service) DeleteTarget(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteReachabilityTarget(ctx, dbgen.DeleteReachabilityTargetParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete target: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Result is the latest result of one (source, target) pair.
type Result struct {
	SourceID  uuid.UUID  `json:"source_id"`
	TargetID  uuid.UUID  `json:"target_id"`
	Status    string     `json:"status"`
	LatencyMS *int       `json:"latency_ms"`
	Detail    string     `json:"detail"`
	RunID     *uuid.UUID `json:"run_id"`
	CheckedAt time.Time  `json:"checked_at"`
}

// Matrix is the source x target read model.
type Matrix struct {
	Config  Config   `json:"config"`
	Sources []Source `json:"sources"`
	Targets []Target `json:"targets"`
	Results []Result `json:"results"`
}

// GetMatrix returns configuration, sources, targets and the latest results.
func (s *Service) GetMatrix(ctx context.Context, ws uuid.UUID) (Matrix, error) {
	var m Matrix
	var err error
	if m.Config, err = s.GetConfig(ctx, ws); err != nil {
		return m, err
	}
	if m.Sources, err = s.ListSources(ctx, ws); err != nil {
		return m, err
	}
	if m.Targets, err = s.ListTargets(ctx, ws); err != nil {
		return m, err
	}
	rows, err := s.q.ListReachabilityResults(ctx, ws)
	if err != nil {
		return m, fmt.Errorf("list results: %w", err)
	}
	m.Results = make([]Result, 0, len(rows))
	for _, r := range rows {
		res := Result{SourceID: r.SourceID, TargetID: r.TargetID, Status: r.Status, Detail: r.Detail, RunID: fromPg(r.RunID), CheckedAt: r.CheckedAt.Time}
		if r.LatencyMs.Valid {
			l := int(r.LatencyMs.Int32)
			res.LatencyMS = &l
		}
		m.Results = append(m.Results, res)
	}
	return m, nil
}

// probeInput assembles the run input from the stored definitions.
func (s *Service) probeInput(ctx context.Context, q *dbgen.Queries, cfg dbgen.ReachabilityConfig) (runs.ProbeInput, error) {
	var in runs.ProbeInput
	if !cfg.InventoryID.Valid {
		return in, fmt.Errorf("%w: no probe inventory configured", ErrInvalid)
	}
	in.InventoryID = uuid.UUID(cfg.InventoryID.Bytes)
	in.SSHSecretID = fromPg(cfg.SshSecretID)
	srcs, err := q.ListReachabilitySources(ctx, cfg.WorkspaceID)
	if err != nil {
		return in, fmt.Errorf("list sources: %w", err)
	}
	for _, r := range srcs {
		if r.InventoryID == in.InventoryID {
			in.Hosts = append(in.Hosts, r.HostName)
		}
	}
	tgts, err := q.ListReachabilityTargets(ctx, cfg.WorkspaceID)
	if err != nil {
		return in, fmt.Errorf("list targets: %w", err)
	}
	for _, t := range tgts {
		if !t.Enabled {
			continue
		}
		m := map[string]any{"id": t.ID.String(), "address": t.Address, "check": t.CheckType, "dns_name": t.DnsName}
		if t.Port.Valid {
			m["port"] = int(t.Port.Int32)
		}
		in.Targets = append(in.Targets, m)
	}
	if len(in.Hosts) == 0 || len(in.Targets) == 0 {
		return in, fmt.Errorf("%w: define at least one source and one enabled target", ErrInvalid)
	}
	return in, nil
}

// RunNow enqueues a probe run immediately (manual trigger). A probe that is
// already queued or running is reported as ErrConflict.
func (s *Service) RunNow(ctx context.Context, ws uuid.UUID, actor runs.Actor) (runs.Run, error) {
	cfg, err := s.q.GetReachabilityConfig(ctx, ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return runs.Run{}, fmt.Errorf("%w: configure the probe first", ErrInvalid)
	}
	if err != nil {
		return runs.Run{}, fmt.Errorf("load reachability config: %w", err)
	}
	active, err := s.q.CountActiveProbeRuns(ctx, ws)
	if err != nil {
		return runs.Run{}, fmt.Errorf("count active probe runs: %w", err)
	}
	if active > 0 {
		return runs.Run{}, fmt.Errorf("%w: a probe run is already queued or running", ErrConflict)
	}
	in, err := s.probeInput(ctx, s.q, cfg)
	if err != nil {
		return runs.Run{}, err
	}
	trigger := runs.TriggerManual
	if actor.UserID == uuid.Nil {
		trigger = runs.TriggerAPI
	}
	return s.runs.EnqueueProbe(ctx, ws, in, trigger, actor)
}

// RunDue claims the workspaces whose probe interval elapsed, enqueues one
// probe run each and advances the schedule. It returns the number of runs
// enqueued. A workspace with a probe still queued or running is skipped for
// this interval; invalid definitions are logged and retried next interval.
func (s *Service) RunDue(ctx context.Context, log *slog.Logger) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin reachability tx: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := dbgen.New(tx)
	due, err := q.ClaimDueReachability(ctx, claimBatch)
	if err != nil {
		return 0, fmt.Errorf("claim due reachability: %w", err)
	}
	started := 0
	for _, cfg := range due {
		var last pgtype.Timestamptz
		switch r, err := s.fire(ctx, q, cfg); {
		case err == nil:
			started++
			last = pgtype.Timestamptz{Time: s.now(), Valid: true}
			log.Debug("reachability probe enqueued", "workspace", cfg.WorkspaceID, "run", r.ID)
		case errors.Is(err, ErrConflict):
			log.Debug("reachability probe skipped: previous run still active", "workspace", cfg.WorkspaceID)
		default:
			log.Warn("reachability probe not started", "workspace", cfg.WorkspaceID, "error", err)
		}
		if err := q.AdvanceReachability(ctx, dbgen.AdvanceReachabilityParams{WorkspaceID: cfg.WorkspaceID, LastRunAt: last}); err != nil {
			log.Error("advance reachability schedule", "workspace", cfg.WorkspaceID, "error", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit reachability tx: %w", err)
	}
	return started, nil
}

func (s *Service) fire(ctx context.Context, q *dbgen.Queries, cfg dbgen.ReachabilityConfig) (runs.Run, error) {
	active, err := q.CountActiveProbeRuns(ctx, cfg.WorkspaceID)
	if err != nil {
		return runs.Run{}, fmt.Errorf("count active probe runs: %w", err)
	}
	if active > 0 {
		return runs.Run{}, ErrConflict
	}
	in, err := s.probeInput(ctx, q, cfg)
	if err != nil {
		return runs.Run{}, err
	}
	return s.runs.EnqueueProbe(ctx, cfg.WorkspaceID, in, runs.TriggerSchedule, runs.Actor{})
}

// Run polls for due probes until ctx is cancelled. A zero tick selects
// DefaultTick.
func (s *Service) Run(ctx context.Context, tick time.Duration, log *slog.Logger) {
	if tick <= 0 {
		tick = DefaultTick
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.RunDue(ctx, log); err != nil && ctx.Err() == nil {
			log.Error("reachability scheduler tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
