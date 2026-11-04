// Package vaultids manages vault identities (ADR-0015): workspace-level
// vault-id labels bound to a password source. Provider "secret" resolves the
// password from a secret of type vault_password; provider "script" only
// stores the configuration, a script is executed by the worker at run time
// and never by the app.
package vaultids

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Providers of a vault password.
const (
	ProviderSecret = "secret"
	ProviderScript = "script"
)

// DefaultLabel is the label of the unlabeled default vault.
const DefaultLabel = "default"

const (
	maxDescLen    = 500
	maxScriptPath = 512
	maxScriptArgs = 32
	maxScriptArg  = 512
)

var (
	// ErrNotFound is returned when an identity does not exist.
	ErrNotFound = errors.New("vault identity not found")
	// ErrConflict is returned when the label is already used in the workspace.
	ErrConflict = errors.New("vault identity label already exists")
	// ErrInvalid wraps validation failures; the message is safe to show to clients.
	ErrInvalid = errors.New("invalid vault identity")
	// ErrProviderNotSupported is returned when the app cannot obtain the
	// password itself (script provider).
	ErrProviderNotSupported = errors.New("provider not supported in the app")
	// ErrNoPassword is returned when the identity has no usable password source.
	ErrNoPassword = errors.New("vault identity has no password source")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// Store is the persistence of the service; *dbgen.Queries implements it.
type Store interface {
	CreateVaultIdentity(ctx context.Context, arg dbgen.CreateVaultIdentityParams) (dbgen.VaultIdentity, error)
	GetVaultIdentity(ctx context.Context, arg dbgen.GetVaultIdentityParams) (dbgen.VaultIdentity, error)
	GetVaultIdentityByLabel(ctx context.Context, arg dbgen.GetVaultIdentityByLabelParams) (dbgen.VaultIdentity, error)
	ListVaultIdentities(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.VaultIdentity, error)
	UpdateVaultIdentity(ctx context.Context, arg dbgen.UpdateVaultIdentityParams) (dbgen.VaultIdentity, error)
	DeleteVaultIdentity(ctx context.Context, arg dbgen.DeleteVaultIdentityParams) (int64, error)
}

// Secrets is the part of the secrets service used here.
type Secrets interface {
	Get(ctx context.Context, ws, id uuid.UUID) (secrets.Meta, error)
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
}

// Identity is the API view of a vault identity. It never contains a password.
type Identity struct {
	ID          uuid.UUID  `json:"id"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	Provider    string     `json:"provider"`
	SecretID    *uuid.UUID `json:"secret_id"`
	ScriptPath  string     `json:"script_path,omitempty"`
	ScriptArgs  []string   `json:"script_args,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Input describes a vault identity for create and (full) replace.
type Input struct {
	Label       string
	Description string
	Provider    string
	SecretID    *uuid.UUID
	ScriptPath  string
	ScriptArgs  []string
	CreatedBy   uuid.UUID
}

// Service manages vault identities.
type Service struct {
	store Store
	sec   Secrets
}

// New builds a Service.
func New(store Store, sec Secrets) *Service { return &Service{store: store, sec: sec} }

func toIdentity(r dbgen.VaultIdentity) Identity {
	id := Identity{ID: r.ID, Label: r.Label, Description: r.Description, Provider: r.Provider,
		ScriptPath: r.ScriptPath, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time}
	if r.SecretID.Valid {
		s := uuid.UUID(r.SecretID.Bytes)
		id.SecretID = &s
	}
	var args []string
	_ = json.Unmarshal(r.ScriptArgs, &args)
	if len(args) > 0 {
		id.ScriptArgs = args
	}
	return id
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ValidLabel reports whether l can be used as a vault-id label.
func ValidLabel(l string) bool { return labelRe.MatchString(l) }

func (s *Service) validate(ctx context.Context, ws uuid.UUID, in *Input) (secretID pgtype.UUID, args []byte, err error) {
	in.Label = strings.TrimSpace(in.Label)
	if !ValidLabel(in.Label) {
		return secretID, nil, invalid("label must be 1-63 characters of letters, digits, '_', '.' or '-'")
	}
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Description) > maxDescLen || !utf8.ValidString(in.Description) || hasControl(in.Description) {
		return secretID, nil, invalid("description must be %d characters at most without control characters", maxDescLen)
	}
	switch in.Provider {
	case ProviderSecret:
		if in.SecretID == nil {
			return secretID, nil, invalid("secret_id is required for provider secret")
		}
		if in.ScriptPath != "" || len(in.ScriptArgs) > 0 {
			return secretID, nil, invalid("script settings are only valid for provider script")
		}
		m, err := s.sec.Get(ctx, ws, *in.SecretID)
		if errors.Is(err, secrets.ErrNotFound) {
			return secretID, nil, invalid("secret not found in this workspace")
		}
		if err != nil {
			return secretID, nil, fmt.Errorf("get secret: %w", err)
		}
		if m.Type != secrets.TypeVaultPassword {
			return secretID, nil, invalid("secret must be of type vault_password")
		}
		secretID = pgtype.UUID{Bytes: *in.SecretID, Valid: true}
	case ProviderScript:
		if in.SecretID != nil {
			return secretID, nil, invalid("secret_id is only valid for provider secret")
		}
		if err := validateScript(in.ScriptPath, in.ScriptArgs); err != nil {
			return secretID, nil, err
		}
	default:
		return secretID, nil, invalid("provider must be %q or %q", ProviderSecret, ProviderScript)
	}
	if in.ScriptArgs == nil {
		in.ScriptArgs = []string{}
	}
	args, _ = json.Marshal(in.ScriptArgs)
	return secretID, args, nil
}

func validateScript(p string, args []string) error {
	if p == "" || len(p) > maxScriptPath || !utf8.ValidString(p) || hasControl(p) || strings.TrimSpace(p) != p {
		return invalid("script_path is required (at most %d characters, no control characters)", maxScriptPath)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return invalid("script_path must not contain '..'")
		}
	}
	if len(args) > maxScriptArgs {
		return invalid("at most %d script arguments", maxScriptArgs)
	}
	for _, a := range args {
		if len(a) > maxScriptArg || !utf8.ValidString(a) || hasControl(a) {
			return invalid("script arguments must be %d characters at most without control characters", maxScriptArg)
		}
	}
	return nil
}

// Create validates and stores a new identity.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in Input) (Identity, error) {
	sid, args, err := s.validate(ctx, ws, &in)
	if err != nil {
		return Identity{}, err
	}
	r, err := s.store.CreateVaultIdentity(ctx, dbgen.CreateVaultIdentityParams{
		WorkspaceID: ws, Label: in.Label, Description: in.Description, Provider: in.Provider, SecretID: sid,
		ScriptPath: in.ScriptPath, ScriptArgs: args,
		CreatedBy: pgtype.UUID{Bytes: in.CreatedBy, Valid: in.CreatedBy != uuid.Nil},
	})
	if err != nil {
		return Identity{}, fmt.Errorf("create vault identity: %w", mapErr(err))
	}
	return toIdentity(r), nil
}

// List returns all identities of a workspace ordered by label.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Identity, error) {
	rows, err := s.store.ListVaultIdentities(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list vault identities: %w", err)
	}
	out := make([]Identity, 0, len(rows))
	for _, r := range rows {
		out = append(out, toIdentity(r))
	}
	return out, nil
}

// Get returns one identity.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Identity, error) {
	r, err := s.store.GetVaultIdentity(ctx, dbgen.GetVaultIdentityParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Identity{}, fmt.Errorf("get vault identity: %w", mapErr(err))
	}
	return toIdentity(r), nil
}

// Update replaces all settings of an identity.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in Input) (Identity, error) {
	sid, args, err := s.validate(ctx, ws, &in)
	if err != nil {
		return Identity{}, err
	}
	r, err := s.store.UpdateVaultIdentity(ctx, dbgen.UpdateVaultIdentityParams{
		ID: id, WorkspaceID: ws, Label: in.Label, Description: in.Description, Provider: in.Provider,
		SecretID: sid, ScriptPath: in.ScriptPath, ScriptArgs: args,
	})
	if err != nil {
		return Identity{}, fmt.Errorf("update vault identity: %w", mapErr(err))
	}
	return toIdentity(r), nil
}

// Delete removes an identity. The referenced secret is not touched.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.store.DeleteVaultIdentity(ctx, dbgen.DeleteVaultIdentityParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete vault identity: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Password is a resolved vault password. Call Zero when done; never log it.
type Password struct {
	Label string
	value secrets.Value
}

// NewPassword wraps a password value; the trailing newline of password files
// is not part of the password.
func NewPassword(label, pw string) Password {
	return Password{Label: label, value: secrets.NewValue([]byte(strings.TrimRight(pw, "\r\n")))}
}

// Reveal returns the password.
func (p Password) Reveal() string { return string(p.value.Reveal()) }

// Zero overwrites the password buffer.
func (p *Password) Zero() { p.value.Zero() }

// ByLabel returns the identity with the label. An empty label means "default".
func (s *Service) ByLabel(ctx context.Context, ws uuid.UUID, label string) (Identity, error) {
	if label == "" {
		label = DefaultLabel
	}
	r, err := s.store.GetVaultIdentityByLabel(ctx, dbgen.GetVaultIdentityByLabelParams{WorkspaceID: ws, Label: label})
	if err != nil {
		return Identity{}, fmt.Errorf("get vault identity %q: %w", label, mapErr(err))
	}
	return toIdentity(r), nil
}

// Password resolves the password of the identity with the label (empty means
// "default"). Script identities yield ErrProviderNotSupported: the app never
// runs scripts. The caller owns the result and must call Zero.
func (s *Service) Password(ctx context.Context, ws uuid.UUID, label string) (Password, error) {
	id, err := s.ByLabel(ctx, ws, label)
	if err != nil {
		return Password{}, err
	}
	if id.Provider != ProviderSecret {
		return Password{}, fmt.Errorf("%w: %s", ErrProviderNotSupported, id.Provider)
	}
	if id.SecretID == nil {
		return Password{}, ErrNoPassword
	}
	res, err := s.sec.Resolve(ctx, ws, *id.SecretID)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return Password{}, ErrNoPassword
		}
		return Password{}, fmt.Errorf("resolve vault password: %w", err)
	}
	if res.Type != secrets.TypeVaultPassword {
		res.Value.Zero()
		return Password{}, ErrNoPassword
	}
	defer res.Value.Zero()
	return NewPassword(id.Label, string(res.Value.Reveal())), nil
}
