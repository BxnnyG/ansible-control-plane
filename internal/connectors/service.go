package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/BxnnyG/ansible-control-plane/internal/metrics"
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

// SecretWriter creates workspace secrets; *secrets.Service implements it.
type SecretWriter interface {
	Create(ctx context.Context, ws uuid.UUID, in secrets.CreateInput) (secrets.Meta, error)
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
	PostSync PostSync // optional
	// Alerts links stored alerts to hosts after webhook deliveries; optional.
	Alerts AlertLinker
	// SecretWriter stores generated secrets (types with Spec.GeneratesSecret);
	// optional.
	SecretWriter SecretWriter
	Timeout      time.Duration // per sync; default DefaultSyncTimeout
	Now          func() time.Time
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
	c, _, err := s.CreateGenerating(ctx, ws, in)
	return c, err
}

// CreateGenerating is Create that also returns the plaintext of a secret
// generated for types with Spec.GeneratesSecret ("" otherwise). The caller
// must show it once and never store or log it.
func (s *Service) CreateGenerating(ctx context.Context, ws uuid.UUID, in CreateInput) (dbgen.Connector, string, error) {
	c, plain, err := s.create(ctx, ws, in)
	if err != nil {
		return dbgen.Connector{}, "", err
	}
	return c, plain, nil
}

func (s *Service) create(ctx context.Context, ws uuid.UUID, in CreateInput) (dbgen.Connector, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 100 {
		return dbgen.Connector{}, "", invalid("name must be 1-100 characters")
	}
	interval := in.IntervalSeconds
	if interval == 0 {
		interval = int(DefaultInterval / time.Second)
	}
	if time.Duration(interval)*time.Second < MinInterval {
		return dbgen.Connector{}, "", invalid("interval must be at least %ds", int(MinInterval/time.Second))
	}
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	secretType := ""
	if in.SecretID != nil {
		sec, err := s.q.GetSecret(ctx, dbgen.GetSecretParams{ID: *in.SecretID, WorkspaceID: ws})
		if err != nil {
			return dbgen.Connector{}, "", invalid("secret not found in workspace")
		}
		secretType = sec.Type
	}
	if err := s.o.Registry.Validate(in.Type, in.BaseURL, cfg, secretType, in.SecretID != nil); err != nil {
		return dbgen.Connector{}, "", err
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
	spec, _ := s.o.Registry.Spec(in.Type)
	generate := spec.GeneratesSecret && in.SecretID == nil
	if generate && s.o.SecretWriter == nil {
		return dbgen.Connector{}, "", errors.New("secret generation is not available")
	}
	c, err := s.q.CreateConnector(ctx, p)
	if err != nil {
		if strings.Contains(err.Error(), "connectors_workspace_id_name_key") {
			return dbgen.Connector{}, "", ErrConflict
		}
		return dbgen.Connector{}, "", fmt.Errorf("create connector: %w", err)
	}
	if !generate {
		return c, "", nil
	}
	c, plain, err := s.attachGeneratedSecret(ctx, ws, c)
	if err != nil {
		_, _ = s.q.DeleteConnector(ctx, dbgen.DeleteConnectorParams{ID: c.ID, WorkspaceID: ws})
		return dbgen.Connector{}, "", err
	}
	return c, plain, nil
}

const generatedSecretDescription = "Generated secret of connector "

// attachGeneratedSecret stores a fresh random token secret and links it to c.
func (s *Service) attachGeneratedSecret(ctx context.Context, ws uuid.UUID, c dbgen.Connector) (dbgen.Connector, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return c, "", fmt.Errorf("generate secret: %w", err)
	}
	plain := hex.EncodeToString(b)
	m, err := s.o.SecretWriter.Create(ctx, ws, secrets.CreateInput{Name: "connector-" + c.Name + "-" + hex.EncodeToString(c.ID[:4]),
		Type: secrets.TypeToken, Description: generatedSecretDescription + c.Name, Value: []byte(plain)})
	if err != nil {
		return c, "", fmt.Errorf("store generated secret: %w", err)
	}
	c, err = s.q.SetConnectorSecret(ctx, dbgen.SetConnectorSecretParams{ID: c.ID, WorkspaceID: ws, SecretID: pgtype.UUID{Bytes: m.ID, Valid: true}})
	if err != nil {
		return c, "", fmt.Errorf("link generated secret: %w", err)
	}
	return c, plain, nil
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
	alerts := make([]Alert, 0, len(r.Alerts))
	seenAlerts := map[string]bool{}
	for _, a := range r.Alerts {
		na, err := normalizeAlert(a)
		if err != nil {
			return r, fmt.Errorf("connector returned an invalid alert: %w", err)
		}
		if seenAlerts[na.ExternalKey] {
			return r, fmt.Errorf("connector returned duplicate alert key %q", na.ExternalKey)
		}
		seenAlerts[na.ExternalKey] = true
		alerts = append(alerts, na)
	}
	r.Alerts = alerts
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
	metrics.ConnectorSyncs.WithLabelValues(c.Type, metrics.Result(serr)).Inc()
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
	metrics.ConnectorLastSuccess.WithLabelValues(c.Type).SetToCurrentTime()
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
	if err := persistAlerts(ctx, q, c, res, started); err != nil {
		return err
	}
	cutoff := ts(started.Add(-ObservationMaxAge))
	if _, err := q.PruneAlerts(ctx, dbgen.PruneAlertsParams{ConnectorID: c.ID, ResolvedAt: cutoff}); err != nil {
		return err
	}
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

// UpdateInput changes a connector; nil fields stay as they are. Config
// replaces the whole option map. ClearSecret removes the credential.
type UpdateInput struct {
	Name            *string
	BaseURL         *string
	Config          map[string]any
	SecretID        *uuid.UUID
	ClearSecret     bool
	IntervalSeconds *int
	Enabled         *bool
}

// Update validates and stores changes. Changes that affect how the remote
// system is reached make an enabled connector due immediately.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in UpdateInput) (dbgen.Connector, error) {
	c, err := s.Get(ctx, ws, id)
	if err != nil {
		return dbgen.Connector{}, err
	}
	p := dbgen.UpdateConnectorParams{ID: id, WorkspaceID: ws, Name: c.Name, BaseUrl: c.BaseUrl, Config: c.Config,
		SecretID: c.SecretID, IntervalSeconds: c.IntervalSeconds, Enabled: c.Enabled}
	connChanged := false
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
		if p.Name == "" || len(p.Name) > 100 {
			return dbgen.Connector{}, invalid("name must be 1-100 characters")
		}
	}
	if in.BaseURL != nil && *in.BaseURL != c.BaseUrl {
		p.BaseUrl, connChanged = *in.BaseURL, true
	}
	cfg := map[string]any{}
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return dbgen.Connector{}, fmt.Errorf("decode config: %w", err)
		}
	}
	if in.Config != nil {
		cfg, connChanged = in.Config, true
		p.Config, _ = json.Marshal(cfg)
	}
	switch {
	case in.SecretID != nil:
		p.SecretID, connChanged = pgtype.UUID{Bytes: *in.SecretID, Valid: true}, true
	case in.ClearSecret:
		p.SecretID, connChanged = pgtype.UUID{}, true
	}
	if in.IntervalSeconds != nil {
		if time.Duration(*in.IntervalSeconds)*time.Second < MinInterval {
			return dbgen.Connector{}, invalid("interval must be at least %ds", int(MinInterval/time.Second))
		}
		p.IntervalSeconds = int32(*in.IntervalSeconds) //nolint:gosec // lower bound checked, API bounds the upper
	}
	if in.Enabled != nil {
		p.Enabled = *in.Enabled
	}
	secretType := ""
	if p.SecretID.Valid {
		sec, err := s.q.GetSecret(ctx, dbgen.GetSecretParams{ID: p.SecretID.Bytes, WorkspaceID: ws})
		if err != nil {
			return dbgen.Connector{}, invalid("secret not found in workspace")
		}
		secretType = sec.Type
	}
	if err := s.o.Registry.Validate(c.Type, p.BaseUrl, cfg, secretType, p.SecretID.Valid); err != nil {
		return dbgen.Connector{}, err
	}
	updated, err := s.q.UpdateConnector(ctx, p)
	if err != nil {
		if strings.Contains(err.Error(), "connectors_workspace_id_name_key") {
			return dbgen.Connector{}, ErrConflict
		}
		return dbgen.Connector{}, fmt.Errorf("update connector: %w", err)
	}
	if (connChanged || (in.Enabled != nil && *in.Enabled && !c.Enabled)) && updated.Enabled {
		if err := s.RequestSync(ctx, ws, id); err != nil {
			return dbgen.Connector{}, err
		}
	}
	return updated, nil
}

// Delete removes a connector together with its networks and observations and
// re-merges the fleet. A secret generated for the connector (types with
// Spec.GeneratesSecret) is removed with it.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	_, err := s.DeleteConnector(ctx, ws, id, DeleteSecretGenerated)
	return err
}

// SecretPolicy selects what happens to the credential of a deleted connector.
type SecretPolicy int

const (
	// DeleteSecretGenerated removes only secrets generated for the connector.
	DeleteSecretGenerated SecretPolicy = iota
	// DeleteSecretUnused removes the secret unless another connector references it.
	DeleteSecretUnused
	// KeepSecret never removes the secret.
	KeepSecret
)

// DeleteConnector deletes a connector and handles its secret according to
// policy. It reports whether the secret was deleted; a secret still in use
// elsewhere is kept without error.
func (s *Service) DeleteConnector(ctx context.Context, ws, id uuid.UUID, policy SecretPolicy) (bool, error) {
	c, err := s.Get(ctx, ws, id)
	if err != nil {
		return false, err
	}
	// Connector and secret go in one transaction: a crash never leaves an
	// orphaned generated secret (e.g. the bearer secret of an alert webhook).
	tx, err := s.o.DB.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("delete connector: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	n, err := q.DeleteConnector(ctx, dbgen.DeleteConnectorParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return false, fmt.Errorf("delete connector: %w", err)
	}
	if n == 0 {
		return false, ErrNotFound
	}
	deleted := false
	if c.SecretID.Valid && policy != KeepSecret {
		deleted = s.deleteSecretIfUnused(ctx, tx, q, ws, c, policy)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("delete connector: %w", err)
	}
	if s.o.PostSync != nil {
		if err := s.o.PostSync.AfterSync(ctx, ws, id); err != nil {
			slog.Error("post-delete hook failed", "connector", id, "error", err)
		}
	}
	return deleted, nil
}

// deleteSecretIfUnused runs inside the delete transaction. The secret delete
// is wrapped in a savepoint: when other objects still reference the secret the
// statement fails, the savepoint is rolled back and the connector delete
// still commits.
func (s *Service) deleteSecretIfUnused(ctx context.Context, tx pgx.Tx, q *dbgen.Queries, ws uuid.UUID, c dbgen.Connector, policy SecretPolicy) bool {
	sid := c.SecretID.Bytes
	sec, err := q.GetSecret(ctx, dbgen.GetSecretParams{ID: sid, WorkspaceID: ws})
	if err != nil {
		return false
	}
	if policy == DeleteSecretGenerated && !strings.HasPrefix(sec.Description, generatedSecretDescription) {
		return false
	}
	var refs int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM connectors WHERE secret_id = $1`, sid).Scan(&refs); err != nil || refs > 0 {
		return false
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return false
	}
	if err := q.WithTx(sp).DeleteSecret(ctx, dbgen.DeleteSecretParams{ID: sid, WorkspaceID: ws}); err != nil {
		_ = sp.Rollback(ctx)
		slog.Warn("secret of deleted connector kept", "connector", c.ID, "error", err)
		return false
	}
	return sp.Commit(ctx) == nil
}

// TestInput describes an unsaved connector configuration to probe.
type TestInput struct {
	Type     string
	BaseURL  string
	Config   map[string]any
	SecretID *uuid.UUID
}

// TestConfig validates and probes a configuration that is not stored
// (SETUP-2 "test before save"). A probe failure is returned as a sanitized
// error; validation problems wrap ErrInvalidConfig.
func (s *Service) TestConfig(ctx context.Context, ws uuid.UUID, in TestInput) error {
	cfg := in.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	secretType := ""
	c := dbgen.Connector{ID: uuid.New(), WorkspaceID: ws, Type: in.Type, Name: "unsaved", BaseUrl: in.BaseURL}
	c.Config, _ = json.Marshal(cfg)
	if in.SecretID != nil {
		sec, err := s.q.GetSecret(ctx, dbgen.GetSecretParams{ID: *in.SecretID, WorkspaceID: ws})
		if err != nil {
			return invalid("secret not found in workspace")
		}
		secretType = sec.Type
		c.SecretID = pgtype.UUID{Bytes: *in.SecretID, Valid: true}
	}
	if err := s.o.Registry.Validate(in.Type, in.BaseURL, cfg, secretType, in.SecretID != nil); err != nil {
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

// ListSyncs returns the newest syncs of a connector, newest first.
func (s *Service) ListSyncs(ctx context.Context, ws, id uuid.UUID, limit int) ([]dbgen.ConnectorSync, error) {
	if _, err := s.Get(ctx, ws, id); err != nil {
		return nil, err
	}
	return s.q.ListConnectorSyncs(ctx, dbgen.ListConnectorSyncsParams{ConnectorID: id, WorkspaceID: ws, Limit: int32(limit)}) //nolint:gosec // bounded by the API
}

// SecretRef is the non-secret description of a connector credential.
type SecretRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Type string    `json:"type"`
}

// SecretRefs returns name and type of the workspace secrets by id (metadata
// only, never values).
func (s *Service) SecretRefs(ctx context.Context, ws uuid.UUID) (map[uuid.UUID]SecretRef, error) {
	rows, err := s.q.ListSecretsMeta(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	out := make(map[uuid.UUID]SecretRef, len(rows))
	for _, r := range rows {
		out[r.ID] = SecretRef{ID: r.ID, Name: r.Name, Type: r.Type}
	}
	return out, nil
}

// Specs returns the registered connector types with their config schema.
func (s *Service) Specs() []Spec { return s.o.Registry.Specs() }
