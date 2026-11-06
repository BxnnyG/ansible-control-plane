package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Runtime defaults (ADR-0018).
const (
	DefaultTick        = 30 * time.Second
	DefaultConcurrency = 2
	DefaultSyncTimeout = 2 * time.Minute
	DefaultInterval    = 5 * time.Minute
	MinInterval        = 30 * time.Second
	MaxBackoff         = time.Hour
	ObservationMaxAge  = 30 * 24 * time.Hour
	keepSyncs          = 200
	maxErrorLen        = 300
)

// Sync triggers recorded in connector_syncs.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
)

// Errors returned by the service.
var (
	ErrNotFound = errors.New("connector not found")
	ErrConflict = errors.New("connector name already exists")
)

// SecretResolver decrypts workspace secrets; *secrets.Service implements it.
type SecretResolver interface {
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
}

// PostSync runs after a successful sync was committed, outside the sync
// transaction. M5 plugs the host merge in here; errors are logged only.
type PostSync interface {
	AfterSync(ctx context.Context, workspace, connector uuid.UUID) error
}

// PostSyncFunc adapts a function to PostSync.
type PostSyncFunc func(ctx context.Context, workspace, connector uuid.UUID) error

// AfterSync implements PostSync.
func (f PostSyncFunc) AfterSync(ctx context.Context, ws, c uuid.UUID) error { return f(ctx, ws, c) }

// Options configure the service.
type Options struct {
	DB       *pgxpool.Pool
	Registry *Registry
	Secrets  SecretResolver
	PostSync PostSync      // optional
	Timeout  time.Duration // per sync; default DefaultSyncTimeout
	Now      func() time.Time
}

// Service manages connectors and runs their syncs.
type Service struct {
	o Options
	q *dbgen.Queries
}

// New builds the service.
func New(o Options) *Service {
	if o.Timeout <= 0 {
		o.Timeout = DefaultSyncTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o, q: dbgen.New(o.DB)}
}

// CreateInput describes a new connector.
type CreateInput struct {
	Type            string
	Name            string
	BaseURL         string
	Config          map[string]any
	SecretID        *uuid.UUID
	IntervalSeconds int // 0 selects DefaultInterval
	Enabled         bool
	CreatedBy       *uuid.UUID
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, a...))
}

// Create validates and stores a connector. It becomes due immediately.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (dbgen.Connector, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return dbgen.Connector{}, invalid("name must be 1-100 characters")
	}
	interval := in.IntervalSeconds
	if interval == 0 {
		interval = int(DefaultInterval / time.Second)
	}
	if time.Duration(interval)*time.Second < MinInterval {
		return dbgen.Connector{}, invalid("interval must be at least %ds", int(MinInterval/time.Second))
	}
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	secretType := ""
	if in.SecretID != nil {
		sec, err := s.q.GetSecret(ctx, dbgen.GetSecretParams{ID: *in.SecretID, WorkspaceID: ws})
		if err != nil {
			return dbgen.Connector{}, invalid("secret not found in workspace")
		}
		secretType = sec.Type
	}
	if err := s.o.Registry.Validate(in.Type, in.BaseURL, cfg, secretType, in.SecretID != nil); err != nil {
		return dbgen.Connector{}, err
	}
	cb, _ := json.Marshal(cfg)
	p := dbgen.CreateConnectorParams{
		WorkspaceID: ws, Type: in.Type, Name: name, BaseUrl: in.BaseURL, Config: cb,
		IntervalSeconds: int32(interval), Enabled: in.Enabled, //nolint:gosec // validated range
	}
	if in.SecretID != nil {
		p.SecretID = pgtype.UUID{Bytes: *in.SecretID, Valid: true}
	}
	if in.CreatedBy != nil {
		p.CreatedBy = pgtype.UUID{Bytes: *in.CreatedBy, Valid: true}
	}
	c, err := s.q.CreateConnector(ctx, p)
	if err != nil {
		if strings.Contains(err.Error(), "connectors_workspace_id_name_key") {
			return dbgen.Connector{}, ErrConflict
		}
		return dbgen.Connector{}, fmt.Errorf("create connector: %w", err)
	}
	return c, nil
}

// List returns the connectors of a workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]dbgen.Connector, error) {
	return s.q.ListConnectors(ctx, ws)
}

// Get returns one connector.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (dbgen.Connector, error) {
	c, err := s.q.GetConnector(ctx, dbgen.GetConnectorParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Connector{}, ErrNotFound
	}
	return c, err
}

// GetByName returns one connector by name.
func (s *Service) GetByName(ctx context.Context, ws uuid.UUID, name string) (dbgen.Connector, error) {
	c, err := s.q.GetConnectorByName(ctx, dbgen.GetConnectorByNameParams{WorkspaceID: ws, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Connector{}, ErrNotFound
	}
	return c, err
}

// RequestSync makes the connector due now; the sync loop picks it up.
func (s *Service) RequestSync(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.RequestConnectorSync(ctx, dbgen.RequestConnectorSyncParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// build resolves the credential and constructs the connector. The returned
// cleanup zeroes the plaintext secret.
func (s *Service) build(ctx context.Context, c dbgen.Connector) (Connector, []string, func(), error) {
	cfg := map[string]any{}
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return nil, nil, nil, fmt.Errorf("decode config: %w", err)
		}
	}
	var cred Credential
	var plain []string
	if c.SecretID.Valid {
		r, err := s.o.Secrets.Resolve(ctx, c.WorkspaceID, c.SecretID.Bytes)
		if err != nil {
			return nil, nil, nil, errors.New("cannot read connector secret")
		}
		cred = Credential{Type: r.Type, Value: r.Value}
		plain = append(plain, string(r.Value.Reveal()))
	}
	cleanup := func() { cred.Value.Zero() }
	inst := Instance{ID: c.ID, Workspace: c.WorkspaceID, Name: c.Name, BaseURL: c.BaseUrl, Config: cfg}
	conn, err := s.o.Registry.New(c.Type, inst, cred)
	if err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	return conn, plain, cleanup, nil
}

// Test checks a connector without storing anything.
func (s *Service) Test(ctx context.Context, ws, id uuid.UUID) error {
	c, err := s.Get(ctx, ws, id)
	if err != nil {
		return err
	}
	conn, plain, cleanup, err := s.build(ctx, c)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
	defer cancel()
	if err := conn.Test(ctx); err != nil {
		return errors.New(sanitize(err, plain))
	}
	return nil
}

// sanitize makes an error safe to store and show: secrets are masked,
// whitespace collapsed and the length bounded.
func sanitize(err error, secretValues []string) string {
	msg := err.Error()
	for _, v := range secretValues {
		if len(v) >= 4 {
			msg = strings.ReplaceAll(msg, v, "***")
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > maxErrorLen {
		msg = msg[:maxErrorLen] + "..."
	}
	return msg
}

// BackoffDelay returns the wait after a failed sync: interval * 2^failures
// (failures = consecutive failures before this one), capped at one hour but
// never below the interval itself.
func BackoffDelay(interval time.Duration, failures int) time.Duration {
	limit := max(MaxBackoff, interval)
	d := interval
	for range failures {
		d *= 2
		if d >= limit {
			return limit
		}
	}
	return d
}

// Counts is stored with every sync.
type Counts struct {
	Networks     int `json:"networks"`
	Observations int `json:"observations"`
	Alerts       int `json:"alerts"`
	// Warnings records a partial sync (optional endpoints that failed).
	Warnings []string `json:"warnings,omitempty"`
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func validate(r Result) (Result, error) {
	nets := map[string]bool{}
	for _, n := range r.Networks {
		if n.ExternalKey == "" || !n.CIDR.IsValid() {
			return r, errors.New("connector returned an invalid network")
		}
		if nets[n.ExternalKey] {
			return r, fmt.Errorf("connector returned duplicate network key %q", n.ExternalKey)
		}
		nets[n.ExternalKey] = true
	}
	obs := map[string]bool{}
	for _, o := range r.Observations {
		if o.ExternalKey == "" || !validKinds[o.Kind] {
			return r, errors.New("connector returned an invalid observation")
		}
		if obs[o.ExternalKey] {
			return r, fmt.Errorf("connector returned duplicate observation key %q", o.ExternalKey)
		}
		obs[o.ExternalKey] = true
	}
	return r, nil
}

// Run syncs one connector now: it builds it, calls Sync with the timeout and
// persists the outcome. The returned error is sanitized.
func (s *Service) Run(ctx context.Context, c dbgen.Connector, trigger string) (Counts, error) {
	started := s.o.Now()
	var (
		res   Result
		plain []string
		serr  error
	)
	func() {
		conn, p, cleanup, err := s.build(ctx, c)
		if err != nil {
			serr = err
			return
		}
		defer cleanup()
		plain = p
		sctx, cancel := context.WithTimeout(ctx, s.o.Timeout)
		defer cancel()
		res, serr = conn.Sync(sctx)
		if serr == nil {
			res, serr = validate(res)
		}
		if serr != nil && errors.Is(sctx.Err(), context.DeadlineExceeded) {
			serr = fmt.Errorf("sync timed out after %s", s.o.Timeout)
		}
	}()
	counts := Counts{Networks: len(res.Networks), Observations: len(res.Observations), Alerts: len(res.Alerts), Warnings: res.Warnings}
	// Persist even when the caller was cancelled, so the status is not lost.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	finished := s.o.Now()
	if serr != nil {
		msg := sanitize(serr, plain)
		if err := s.persistError(pctx, c, trigger, started, finished, msg); err != nil {
			return counts, fmt.Errorf("record sync failure: %w", err)
		}
		return Counts{}, errors.New(msg)
	}
	if err := s.persistOK(pctx, c, trigger, started, finished, res, counts); err != nil {
		return counts, fmt.Errorf("store sync result: %w", err)
	}
	if s.o.PostSync != nil {
		if err := s.o.PostSync.AfterSync(pctx, c.WorkspaceID, c.ID); err != nil {
			slog.Error("post-sync hook failed", "connector", c.ID, "error", err)
		}
	}
	return counts, nil
}

func (s *Service) persistOK(ctx context.Context, c dbgen.Connector, trigger string, started, finished time.Time, res Result, counts Counts) error {
	tx, err := s.o.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	for _, n := range res.Networks {
		p := dbgen.UpsertNetworkParams{WorkspaceID: c.WorkspaceID, ConnectorID: c.ID, ExternalKey: n.ExternalKey, Cidr: n.CIDR.Masked(),
			Interface: n.Interface, Description: n.Description, Source: n.Source, FirstSeen: ts(started)}
		if n.VLAN != nil {
			p.Vlan = pgtype.Int4{Int32: int32(*n.VLAN), Valid: true} //nolint:gosec // checked by DB constraint
		}
		if err := q.UpsertNetwork(ctx, p); err != nil {
			return fmt.Errorf("upsert network: %w", err)
		}
	}
	for _, o := range res.Observations {
		fb, err := json.Marshal(o.Fields)
		if err != nil {
			return err
		}
		if err := q.UpsertObservation(ctx, dbgen.UpsertObservationParams{WorkspaceID: c.WorkspaceID, ConnectorID: c.ID,
			ExternalKey: o.ExternalKey, Kind: o.Kind, Fields: fb, FirstSeen: ts(started)}); err != nil {
			return fmt.Errorf("upsert observation: %w", err)
		}
	}
	cutoff := ts(started.Add(-ObservationMaxAge))
	if _, err := q.PruneNetworks(ctx, dbgen.PruneNetworksParams{ConnectorID: c.ID, LastSeen: cutoff}); err != nil {
		return err
	}
	if _, err := q.PruneObservations(ctx, dbgen.PruneObservationsParams{ConnectorID: c.ID, LastSeen: cutoff}); err != nil {
		return err
	}
	if err := s.recordSync(ctx, q, c, trigger, "ok", started, finished, counts, ""); err != nil {
		return err
	}
	cb, _ := json.Marshal(counts)
	if err := q.FinishConnectorSyncOK(ctx, dbgen.FinishConnectorSyncOKParams{ID: c.ID, LastSyncAt: ts(finished),
		LastDurationMs: durMs(started, finished), LastCounts: cb}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) persistError(ctx context.Context, c dbgen.Connector, trigger string, started, finished time.Time, msg string) error {
	tx, err := s.o.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := s.recordSync(ctx, q, c, trigger, "error", started, finished, Counts{}, msg); err != nil {
		return err
	}
	delay := BackoffDelay(time.Duration(c.IntervalSeconds)*time.Second, int(c.BackoffCount))
	if err := q.FinishConnectorSyncError(ctx, dbgen.FinishConnectorSyncErrorParams{ID: c.ID, LastSyncAt: ts(finished), LastError: msg,
		LastDurationMs: durMs(started, finished), Column5: int32(delay / time.Second)}); err != nil { //nolint:gosec // capped
		return err
	}
	return tx.Commit(ctx)
}

func durMs(a, b time.Time) int32 { return int32(min(b.Sub(a).Milliseconds(), 1<<30)) } //nolint:gosec // clamped

func (s *Service) recordSync(ctx context.Context, q *dbgen.Queries, c dbgen.Connector, trigger, status string, started, finished time.Time, counts Counts, msg string) error {
	cb, _ := json.Marshal(counts)
	if _, err := q.InsertConnectorSync(ctx, dbgen.InsertConnectorSyncParams{ConnectorID: c.ID, WorkspaceID: c.WorkspaceID, Trigger: trigger,
		Status: status, StartedAt: ts(started), FinishedAt: ts(finished), DurationMs: durMs(started, finished), Counts: cb, Error: msg}); err != nil {
		return err
	}
	_, err := q.PruneConnectorSyncs(ctx, dbgen.PruneConnectorSyncsParams{ConnectorID: c.ID, Limit: keepSyncs})
	return err
}

// ClaimDue leases up to limit due connectors. The lease is a single
// UPDATE ... RETURNING behind FOR UPDATE SKIP LOCKED, so concurrent app
// instances never receive the same connector.
func (s *Service) ClaimDue(ctx context.Context, limit int) ([]dbgen.Connector, error) {
	lease := int32((s.o.Timeout + 30*time.Second) / time.Second)                                                    //nolint:gosec // small
	rows, err := s.q.ClaimDueConnectors(ctx, dbgen.ClaimDueConnectorsParams{Column1: lease, Column2: int32(limit)}) //nolint:gosec // small
	if err != nil {
		return nil, fmt.Errorf("claim due connectors: %w", err)
	}
	return rows, nil
}

// RunDue claims and syncs due connectors once and waits for them.
func (s *Service) RunDue(ctx context.Context, concurrency int, log *slog.Logger) {
	due, err := s.ClaimDue(ctx, concurrency)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("connector sync: claim failed", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, c := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Run(ctx, c, TriggerSchedule); err != nil && ctx.Err() == nil {
				log.Warn("connector sync failed", "connector", c.ID, "type", c.Type, "error", err)
			}
		}()
	}
	wg.Wait()
}

// RunScheduler syncs due connectors until ctx is cancelled. Zero tick or
// concurrency select the defaults.
func (s *Service) RunScheduler(ctx context.Context, tick time.Duration, concurrency int, log *slog.Logger) {
	if tick <= 0 {
		tick = DefaultTick
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
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
