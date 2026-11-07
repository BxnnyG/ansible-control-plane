package secrets

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/ssh"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Secret types accepted by the store.
const (
	TypeSSHKey        = "ssh_key"
	TypePassword      = "password"
	TypeToken         = "token"
	TypeVaultPassword = "vault_password"
	TypeFile          = "file"
	// TypeAPIKeyPair holds a JSON object {"key": "...", "secret": "..."} (connectors).
	TypeAPIKeyPair = "api_key_pair" //nolint:gosec // type name, not a credential
	// TypeAPIToken holds a single API token (connectors).
	TypeAPIToken = "api_token"
)

const (
	// MaxValueSize is the maximum plaintext size of a secret value.
	MaxValueSize = 64 << 10
	maxNameLen   = 80
	maxDescLen   = 500
)

var (
	// ErrNotFound is returned when a secret does not exist in the workspace.
	ErrNotFound = errors.New("secret not found")
	// ErrConflict is returned when the name is already used in the workspace.
	ErrConflict = errors.New("secret name already exists")
	// ErrInvalid wraps validation failures; the message is safe to show to clients.
	ErrInvalid = errors.New("invalid secret")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ValidType reports whether t is a known secret type.
func ValidType(t string) bool {
	switch t {
	case TypeSSHKey, TypePassword, TypeToken, TypeVaultPassword, TypeFile, TypeAPIKeyPair, TypeAPIToken:
		return true
	}
	return false
}

// Store is the persistence of the secrets service; *dbgen.Queries implements it.
type Store interface {
	CreateSecret(ctx context.Context, arg dbgen.CreateSecretParams) (dbgen.Secret, error)
	GetSecret(ctx context.Context, arg dbgen.GetSecretParams) (dbgen.Secret, error)
	ListSecretsMeta(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.ListSecretsMetaRow, error)
	UpdateSecretWithEnvelope(ctx context.Context, arg dbgen.UpdateSecretWithEnvelopeParams) (dbgen.UpdateSecretWithEnvelopeRow, error)
	DeleteSecret(ctx context.Context, arg dbgen.DeleteSecretParams) error
}

// Meta is the non-secret view of a stored secret.
type Meta struct {
	ID          uuid.UUID      `json:"id"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// CreateInput describes a new secret. Value is consumed and never echoed.
type CreateInput struct {
	Name, Type, Description string
	Value                   []byte
	CreatedBy               uuid.UUID
}

// UpdateInput changes a secret; nil fields stay untouched.
type UpdateInput struct {
	Name, Description *string
	Value             []byte // nil keeps the current value
}

// Service stores and resolves workspace secrets.
type Service struct {
	store Store
	prov  Provider
}

// NewService builds a Service on top of a store and key provider.
func NewService(store Store, prov Provider) *Service { return &Service{store: store, prov: prov} }

func aadFor(id, ws uuid.UUID) AAD { return AAD{SecretID: id.String(), WorkspaceID: ws.String()} }

func cleanText(s string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if (required && s == "") || utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return "", invalid("text must be %d characters at most and non-empty if required", max)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", invalid("control characters are not allowed")
		}
	}
	return s, nil
}

// prepareValue validates a value for its type and returns the (possibly
// normalized) value plus non-secret metadata.
func prepareValue(typ string, v []byte) ([]byte, map[string]any, error) {
	if len(v) == 0 {
		return nil, nil, invalid("value must not be empty")
	}
	if len(v) > MaxValueSize {
		return nil, nil, invalid("value exceeds %d bytes", MaxValueSize)
	}
	meta := map[string]any{}
	switch typ {
	case TypeSSHKey:
		raw, err := ssh.ParseRawPrivateKey(v)
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				return nil, nil, invalid("passphrase-protected ssh keys are not supported")
			}
			return nil, nil, invalid("value is not a valid private key")
		}
		if k, ok := raw.(*ed25519.PrivateKey); ok {
			raw = *k
		}
		signer, err := ssh.NewSignerFromKey(raw)
		if err != nil {
			return nil, nil, invalid("unsupported private key type")
		}
		pub := signer.PublicKey()
		meta["fingerprint"] = ssh.FingerprintSHA256(pub)
		meta["key_type"] = pub.Type()
		if v[len(v)-1] != '\n' {
			v = append(append([]byte(nil), v...), '\n')
		}
	case TypeAPIKeyPair:
		var kp struct{ Key, Secret string }
		if err := json.Unmarshal(v, &kp); err != nil || kp.Key == "" || kp.Secret == "" {
			return nil, nil, invalid(`api_key_pair value must be a JSON object with "key" and "secret"`)
		}
	case TypePassword, TypeToken, TypeVaultPassword, TypeFile, TypeAPIToken:
	default:
		return nil, nil, invalid("unknown type")
	}
	return v, meta, nil
}

func metaFrom(id, _ uuid.UUID, name, typ, desc string, md []byte, c, u pgtype.Timestamptz) Meta {
	m := map[string]any{}
	_ = json.Unmarshal(md, &m)
	return Meta{ID: id, Name: name, Type: typ, Description: desc, Metadata: m, CreatedAt: c.Time, UpdatedAt: u.Time}
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

// Create validates and stores a new secret.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (Meta, error) {
	name, err := cleanText(in.Name, maxNameLen, true)
	if err != nil {
		return Meta{}, err
	}
	desc, err := cleanText(in.Description, maxDescLen, false)
	if err != nil {
		return Meta{}, err
	}
	if !ValidType(in.Type) {
		return Meta{}, invalid("unknown type")
	}
	val, md, err := prepareValue(in.Type, in.Value)
	if err != nil {
		return Meta{}, err
	}
	id := uuid.New()
	env, err := Seal(s.prov, val, aadFor(id, ws))
	if err != nil {
		return Meta{}, fmt.Errorf("seal secret: %w", err)
	}
	mdJSON, _ := json.Marshal(md)
	row, err := s.store.CreateSecret(ctx, dbgen.CreateSecretParams{
		ID: id, WorkspaceID: ws, Name: name, Type: in.Type, Description: desc,
		EnvVersion: int32(env.Version), EnvKeyID: env.KeyID, EnvWrapNonce: env.WrapNonce, //nolint:gosec // small constant
		EnvWrappedDek: env.WrappedDEK, EnvNonce: env.Nonce, EnvCiphertext: env.Ciphertext,
		CreatedBy: pgtype.UUID{Bytes: in.CreatedBy, Valid: in.CreatedBy != uuid.Nil}, Metadata: mdJSON,
	})
	if err != nil {
		return Meta{}, fmt.Errorf("create secret: %w", mapErr(err))
	}
	return metaFrom(row.ID, row.WorkspaceID, row.Name, row.Type, row.Description, row.Metadata, row.CreatedAt, row.UpdatedAt), nil
}

// List returns the metadata of all secrets of a workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Meta, error) {
	rows, err := s.store.ListSecretsMeta(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	out := make([]Meta, 0, len(rows))
	for _, r := range rows {
		out = append(out, metaFrom(r.ID, r.WorkspaceID, r.Name, r.Type, r.Description, r.Metadata, r.CreatedAt, r.UpdatedAt))
	}
	return out, nil
}

// Get returns the metadata of one secret.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Meta, error) {
	r, err := s.store.GetSecret(ctx, dbgen.GetSecretParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Meta{}, fmt.Errorf("get secret: %w", mapErr(err))
	}
	return metaFrom(r.ID, r.WorkspaceID, r.Name, r.Type, r.Description, r.Metadata, r.CreatedAt, r.UpdatedAt), nil
}

// Update renames a secret, changes its description and/or replaces its value.
// The type is immutable.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in UpdateInput) (Meta, error) {
	// The write is conditional on the updated_at that was read, so a
	// concurrent PUT can never be overwritten with stale name, description or
	// ciphertext. On a lost race the change is re-applied to the fresh row.
	for range maxUpdateAttempts {
		m, err := s.updateOnce(ctx, ws, id, in)
		if !errors.Is(err, errStale) {
			return m, err
		}
	}
	return Meta{}, fmt.Errorf("update secret: %w", ErrConflictRetry)
}

const maxUpdateAttempts = 5

// errStale means the row changed between read and write.
var errStale = errors.New("secret changed concurrently")

// ErrConflictRetry is returned when an update lost the race repeatedly.
var ErrConflictRetry = errors.New("secret is being modified concurrently, retry")

func (s *Service) updateOnce(ctx context.Context, ws, id uuid.UUID, in UpdateInput) (Meta, error) {
	cur, err := s.store.GetSecret(ctx, dbgen.GetSecretParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Meta{}, fmt.Errorf("get secret: %w", mapErr(err))
	}
	name, desc := cur.Name, cur.Description
	if in.Name != nil {
		if name, err = cleanText(*in.Name, maxNameLen, true); err != nil {
			return Meta{}, err
		}
	}
	if in.Description != nil {
		if desc, err = cleanText(*in.Description, maxDescLen, false); err != nil {
			return Meta{}, err
		}
	}
	p := dbgen.UpdateSecretWithEnvelopeParams{
		ID: id, WorkspaceID: ws, Name: name, Description: desc, Metadata: cur.Metadata, UpdatedAt: cur.UpdatedAt,
		EnvVersion: cur.EnvVersion, EnvKeyID: cur.EnvKeyID, EnvWrapNonce: cur.EnvWrapNonce,
		EnvWrappedDek: cur.EnvWrappedDek, EnvNonce: cur.EnvNonce, EnvCiphertext: cur.EnvCiphertext,
	}
	if in.Value != nil {
		val, md, err := prepareValue(cur.Type, in.Value)
		if err != nil {
			return Meta{}, err
		}
		env, err := Seal(s.prov, val, aadFor(id, ws))
		if err != nil {
			return Meta{}, fmt.Errorf("seal secret: %w", err)
		}
		p.Metadata, _ = json.Marshal(md)
		p.EnvVersion, p.EnvKeyID, p.EnvWrapNonce = int32(env.Version), env.KeyID, env.WrapNonce //nolint:gosec // small constant
		p.EnvWrappedDek, p.EnvNonce, p.EnvCiphertext = env.WrappedDEK, env.Nonce, env.Ciphertext
	}
	r, err := s.store.UpdateSecretWithEnvelope(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return Meta{}, errStale // deleted or changed: the next read tells which
	}
	if err != nil {
		return Meta{}, fmt.Errorf("update secret: %w", mapErr(err))
	}
	return metaFrom(r.ID, r.WorkspaceID, r.Name, r.Type, r.Description, r.Metadata, r.CreatedAt, r.UpdatedAt), nil
}

// Delete removes a secret; it returns ErrNotFound if it does not exist.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	if _, err := s.store.GetSecret(ctx, dbgen.GetSecretParams{ID: id, WorkspaceID: ws}); err != nil {
		return fmt.Errorf("get secret: %w", mapErr(err))
	}
	if err := s.store.DeleteSecret(ctx, dbgen.DeleteSecretParams{ID: id, WorkspaceID: ws}); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

// Resolved is a decrypted secret for internal use (runner, Git, connectors).
// It must never be returned by the API or logged; call Value.Zero when done.
type Resolved struct {
	Type  string
	Name  string
	Value Value
}

// Resolve decrypts a secret of the workspace. Internal use only.
func (s *Service) Resolve(ctx context.Context, ws, id uuid.UUID) (Resolved, error) {
	r, err := s.store.GetSecret(ctx, dbgen.GetSecretParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Resolved{}, fmt.Errorf("get secret: %w", mapErr(err))
	}
	env := &Envelope{Version: int(r.EnvVersion), KeyID: r.EnvKeyID, WrapNonce: r.EnvWrapNonce,
		WrappedDEK: r.EnvWrappedDek, Nonce: r.EnvNonce, Ciphertext: r.EnvCiphertext}
	v, err := Open(s.prov, env, aadFor(r.ID, r.WorkspaceID))
	if err != nil {
		return Resolved{}, fmt.Errorf("open secret %s: %w", r.ID, err)
	}
	return Resolved{Type: r.Type, Name: r.Name, Value: v}, nil
}
