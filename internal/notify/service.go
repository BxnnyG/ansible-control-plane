package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Errors returned by the Service.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("notification channel name already exists")
)

// ChannelInput describes a channel to create or update (the type is immutable).
type ChannelInput struct {
	Name          string
	Type          string // create only
	Config        json.RawMessage
	SecretID      *uuid.UUID
	TLSCAPEM      string
	TLSSkipVerify bool
	Enabled       bool
}

// ChannelView is the API-safe view of a channel; it never contains credentials.
type ChannelView struct {
	ID            uuid.UUID       `json:"id"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Config        json.RawMessage `json:"config"`
	SecretID      *uuid.UUID      `json:"secret_id"`
	TLSCAPEM      string          `json:"tls_ca_pem"`
	TLSSkipVerify bool            `json:"tls_skip_verify"`
	Enabled       bool            `json:"enabled"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// RuleInput describes a rule to create or update.
type RuleInput struct {
	ChannelID  uuid.UUID
	Events     []string
	TemplateID *uuid.UUID
	Enabled    bool
}

// RuleView is the API view of a rule.
type RuleView struct {
	ID         uuid.UUID  `json:"id"`
	ChannelID  uuid.UUID  `json:"channel_id"`
	Events     []string   `json:"events"`
	TemplateID *uuid.UUID `json:"template_id"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// DeliveryView is one delivery attempt record.
type DeliveryView struct {
	ID            uuid.UUID  `json:"id"`
	ChannelID     uuid.UUID  `json:"channel_id"`
	RunID         uuid.UUID  `json:"run_id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastError     string     `json:"last_error"`
	SentAt        *time.Time `json:"sent_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Service manages channels and rules and sends test messages.
type Service struct {
	db        *pgxpool.Pool
	q         *dbgen.Queries
	secrets   SecretResolver
	senders   func(string) (Sender, error)
	publicURL string
}

// ServiceOptions configures NewService.
type ServiceOptions struct {
	DB        *pgxpool.Pool
	Secrets   SecretResolver
	PublicURL string
	Senders   func(string) (Sender, error) // tests
}

// NewService creates a Service.
func NewService(o ServiceOptions) *Service {
	s := &Service{db: o.DB, q: dbgen.New(o.DB), secrets: o.Secrets, senders: o.Senders, publicURL: o.PublicURL}
	if s.senders == nil {
		s.senders = SenderFor
	}
	return s
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// actorUUID maps the zero UUID (no acting user, e.g. CLI) to NULL.
func actorUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: id != uuid.Nil}
}

func uuidPtr(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func channelView(c dbgen.NotificationChannel) ChannelView {
	return ChannelView{ID: c.ID, Name: c.Name, Type: c.Type, Config: c.Config, SecretID: uuidPtr(c.SecretID),
		TLSCAPEM: c.TlsCaPem, TLSSkipVerify: c.TlsSkipVerify, Enabled: c.Enabled,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time}
}

func ruleView(r dbgen.NotificationRule) RuleView {
	return RuleView{ID: r.ID, ChannelID: r.ChannelID, Events: r.Events, TemplateID: uuidPtr(r.TemplateID),
		Enabled: r.Enabled, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
}

func mapErr(err error) error {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.As(err, &pe) && pe.Code == "23505":
		return ErrConflict
	}
	return err
}

func (s *Service) validate(ctx context.Context, ws uuid.UUID, typ string, in ChannelInput) (json.RawMessage, error) {
	if in.Name == "" || len(in.Name) > 80 {
		return nil, invalid("name must be 1-80 characters")
	}
	cfg := in.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	if in.SecretID != nil {
		if s.secrets == nil {
			return nil, invalid("secret store unavailable")
		}
		if _, err := s.secrets.Get(ctx, ws, *in.SecretID); err != nil {
			return nil, invalid("secret_id does not reference a secret of this workspace")
		}
	}
	if err := ValidateConfig(typ, cfg, in.SecretID != nil); err != nil {
		return nil, err
	}
	if in.TLSCAPEM != "" {
		if _, err := tlsConfig(Channel{TLSCAPEM: in.TLSCAPEM}, ""); err != nil {
			return nil, invalid("%v", err)
		}
	}
	return cfg, nil
}

// CreateChannel validates and stores a channel.
func (s *Service) CreateChannel(ctx context.Context, ws uuid.UUID, in ChannelInput, actor uuid.UUID) (ChannelView, error) {
	cfg, err := s.validate(ctx, ws, in.Type, in)
	if err != nil {
		return ChannelView{}, err
	}
	row, err := s.q.CreateNotificationChannel(ctx, dbgen.CreateNotificationChannelParams{
		WorkspaceID: ws, Name: in.Name, Type: in.Type, Config: cfg, SecretID: pgUUID(in.SecretID),
		TlsCaPem: in.TLSCAPEM, TlsSkipVerify: in.TLSSkipVerify, Enabled: in.Enabled, CreatedBy: actorUUID(actor)})
	if err != nil {
		return ChannelView{}, fmt.Errorf("create channel: %w", mapErr(err))
	}
	return channelView(row), nil
}

// GetChannel returns one channel.
func (s *Service) GetChannel(ctx context.Context, ws, id uuid.UUID) (ChannelView, error) {
	row, err := s.q.GetNotificationChannel(ctx, dbgen.GetNotificationChannelParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return ChannelView{}, fmt.Errorf("get channel: %w", mapErr(err))
	}
	return channelView(row), nil
}

// ListChannels lists the channels of a workspace.
func (s *Service) ListChannels(ctx context.Context, ws uuid.UUID) ([]ChannelView, error) {
	rows, err := s.q.ListNotificationChannels(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	out := make([]ChannelView, 0, len(rows))
	for _, r := range rows {
		out = append(out, channelView(r))
	}
	return out, nil
}

// UpdateChannel replaces name, config, secret, TLS options and enabled flag.
func (s *Service) UpdateChannel(ctx context.Context, ws, id uuid.UUID, in ChannelInput, actor uuid.UUID) (ChannelView, error) {
	cur, err := s.q.GetNotificationChannel(ctx, dbgen.GetNotificationChannelParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return ChannelView{}, fmt.Errorf("get channel: %w", mapErr(err))
	}
	cfg, err := s.validate(ctx, ws, cur.Type, in)
	if err != nil {
		return ChannelView{}, err
	}
	row, err := s.q.UpdateNotificationChannel(ctx, dbgen.UpdateNotificationChannelParams{
		ID: id, WorkspaceID: ws, Name: in.Name, Config: cfg, SecretID: pgUUID(in.SecretID),
		TlsCaPem: in.TLSCAPEM, TlsSkipVerify: in.TLSSkipVerify, Enabled: in.Enabled, UpdatedBy: actorUUID(actor)})
	if err != nil {
		return ChannelView{}, fmt.Errorf("update channel: %w", mapErr(err))
	}
	return channelView(row), nil
}

// DeleteChannel removes a channel with its rules and deliveries.
func (s *Service) DeleteChannel(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteNotificationChannel(ctx, dbgen.DeleteNotificationChannelParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) checkRule(ctx context.Context, ws uuid.UUID, in RuleInput) ([]string, error) {
	if len(in.Events) == 0 {
		return nil, invalid("at least one event is required")
	}
	seen := map[string]bool{}
	var events []string
	for _, e := range in.Events {
		if !ValidKind(e) {
			return nil, invalid("unknown event %q", e)
		}
		if !seen[e] {
			seen[e] = true
			events = append(events, e)
		}
	}
	if _, err := s.q.GetNotificationChannel(ctx, dbgen.GetNotificationChannelParams{ID: in.ChannelID, WorkspaceID: ws}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, invalid("channel_id does not reference a channel of this workspace")
		}
		return nil, fmt.Errorf("check channel: %w", err)
	}
	if in.TemplateID != nil {
		if _, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: *in.TemplateID, WorkspaceID: ws}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, invalid("template_id does not reference a template of this workspace")
			}
			return nil, fmt.Errorf("check template: %w", err)
		}
	}
	return events, nil
}

// CreateRule stores a rule.
func (s *Service) CreateRule(ctx context.Context, ws uuid.UUID, in RuleInput, actor uuid.UUID) (RuleView, error) {
	events, err := s.checkRule(ctx, ws, in)
	if err != nil {
		return RuleView{}, err
	}
	row, err := s.q.CreateNotificationRule(ctx, dbgen.CreateNotificationRuleParams{WorkspaceID: ws, ChannelID: in.ChannelID,
		Events: events, TemplateID: pgUUID(in.TemplateID), Enabled: in.Enabled, CreatedBy: actorUUID(actor)})
	if err != nil {
		return RuleView{}, fmt.Errorf("create rule: %w", err)
	}
	return ruleView(row), nil
}

// GetRule returns one rule.
func (s *Service) GetRule(ctx context.Context, ws, id uuid.UUID) (RuleView, error) {
	row, err := s.q.GetNotificationRule(ctx, dbgen.GetNotificationRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return RuleView{}, fmt.Errorf("get rule: %w", mapErr(err))
	}
	return ruleView(row), nil
}

// ListRules lists the rules of a workspace.
func (s *Service) ListRules(ctx context.Context, ws uuid.UUID) ([]RuleView, error) {
	rows, err := s.q.ListNotificationRules(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	out := make([]RuleView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ruleView(r))
	}
	return out, nil
}

// UpdateRule replaces channel, events, template filter and enabled flag.
func (s *Service) UpdateRule(ctx context.Context, ws, id uuid.UUID, in RuleInput) (RuleView, error) {
	events, err := s.checkRule(ctx, ws, in)
	if err != nil {
		return RuleView{}, err
	}
	row, err := s.q.UpdateNotificationRule(ctx, dbgen.UpdateNotificationRuleParams{ID: id, WorkspaceID: ws,
		ChannelID: in.ChannelID, Events: events, TemplateID: pgUUID(in.TemplateID), Enabled: in.Enabled})
	if err != nil {
		return RuleView{}, fmt.Errorf("update rule: %w", mapErr(err))
	}
	return ruleView(row), nil
}

// DeleteRule removes a rule.
func (s *Service) DeleteRule(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteNotificationRule(ctx, dbgen.DeleteNotificationRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDeliveries returns the most recent deliveries of a channel.
func (s *Service) ListDeliveries(ctx context.Context, ws, channelID uuid.UUID, limit int) ([]DeliveryView, error) {
	if _, err := s.GetChannel(ctx, ws, channelID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.ListNotificationDeliveries(ctx, dbgen.ListNotificationDeliveriesParams{ChannelID: channelID, Limit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	out := make([]DeliveryView, 0, len(rows))
	for _, r := range rows {
		v := DeliveryView{ID: r.ID, ChannelID: r.ChannelID, RunID: r.RunID, Kind: r.Kind, Status: r.Status, Attempts: int(r.Attempts),
			NextAttemptAt: r.NextAttemptAt.Time, LastError: r.LastError, CreatedAt: r.CreatedAt.Time}
		if r.SentAt.Valid {
			t := r.SentAt.Time
			v.SentAt = &t
		}
		out = append(out, v)
	}
	return out, nil
}

// SendTest sends a sample message through the channel, even when it is
// disabled, and returns the sanitized error text of a failed send.
func (s *Service) SendTest(ctx context.Context, ws, id uuid.UUID) error {
	row, err := s.q.GetNotificationChannel(ctx, dbgen.GetNotificationChannelParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("get channel: %w", mapErr(err))
	}
	name, err := s.q.GetNotificationWorkspaceName(ctx, ws)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	msg := SampleMessage(name, s.publicURL)
	msg.URL = s.publicURL
	return sendVia(ctx, s.secrets, s.senders, row, msg)
}

// ListWorkspaceDeliveries returns the most recent deliveries of the workspace,
// optionally filtered by channel and status (applied in SQL).
func (s *Service) ListWorkspaceDeliveries(ctx context.Context, ws uuid.UUID, channelID *uuid.UUID, status string, limit int) ([]DeliveryView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	p := dbgen.ListWorkspaceNotificationDeliveriesParams{WorkspaceID: ws, ChannelID: pgUUID(channelID), RowLimit: int32(limit)} //nolint:gosec // bounded above
	if status != "" {
		p.Status = pgtype.Text{String: status, Valid: true}
	}
	rows, err := s.q.ListWorkspaceNotificationDeliveries(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	out := make([]DeliveryView, 0, len(rows))
	for _, r := range rows {
		v := DeliveryView{ID: r.ID, ChannelID: r.ChannelID, RunID: r.RunID, Kind: r.Kind, Status: r.Status, Attempts: int(r.Attempts),
			NextAttemptAt: r.NextAttemptAt.Time, LastError: r.LastError, CreatedAt: r.CreatedAt.Time}
		if r.SentAt.Valid {
			t := r.SentAt.Time
			v.SentAt = &t
		}
		out = append(out, v)
	}
	return out, nil
}

// DraftInput describes an unsaved channel for TestDraft. SecretValue is used
// only in memory and zeroed before TestDraft returns.
type DraftInput struct {
	Type          string
	Config        json.RawMessage
	SecretID      *uuid.UUID
	SecretValue   []byte
	TLSCAPEM      string
	TLSSkipVerify bool
	ChannelID     *uuid.UUID // existing channel whose stored secret is used when no secret is given
}

// TestDraft sends a sample message through an unsaved channel definition. The
// config is validated like on create. Credential precedence: SecretValue,
// SecretID, then the stored secret of ChannelID. Nothing is persisted.
func (s *Service) TestDraft(ctx context.Context, ws uuid.UUID, in DraftInput) error {
	defer func() {
		for i := range in.SecretValue {
			in.SecretValue[i] = 0
		}
	}()
	cfg := in.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	secretID := in.SecretID
	if len(in.SecretValue) == 0 && secretID == nil && in.ChannelID != nil {
		row, err := s.q.GetNotificationChannel(ctx, dbgen.GetNotificationChannelParams{ID: *in.ChannelID, WorkspaceID: ws})
		if err != nil {
			return fmt.Errorf("get channel: %w", mapErr(err))
		}
		secretID = uuidPtr(row.SecretID)
	}
	if len(in.SecretValue) > 0 {
		secretID = nil
	}
	if secretID != nil {
		if s.secrets == nil {
			return invalid("secret store unavailable")
		}
		if _, err := s.secrets.Get(ctx, ws, *secretID); err != nil {
			return invalid("secret_id does not reference a secret of this workspace")
		}
	}
	if err := ValidateConfig(in.Type, cfg, secretID != nil || len(in.SecretValue) > 0); err != nil {
		return err
	}
	if in.TLSCAPEM != "" {
		if _, err := tlsConfig(Channel{TLSCAPEM: in.TLSCAPEM}, ""); err != nil {
			return invalid("%v", err)
		}
	}
	snd, err := s.senders(in.Type)
	if err != nil {
		return err
	}
	cred := in.SecretValue
	if secretID != nil {
		r, err := s.secrets.Resolve(ctx, ws, *secretID)
		if err != nil {
			return fmt.Errorf("resolve channel secret: %s", sanitize(err.Error(), nil))
		}
		defer r.Value.Zero()
		cred = r.Value.Reveal()
	}
	name, err := s.q.GetNotificationWorkspaceName(ctx, ws)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}
	msg := SampleMessage(name, s.publicURL)
	msg.URL = s.publicURL
	ch := Channel{Type: in.Type, Config: cfg, TLSCAPEM: in.TLSCAPEM, TLSSkipVerify: in.TLSSkipVerify}
	return sendCred(ctx, snd, ch, cred, msg)
}
