package vaultids

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

type memStore struct {
	rows map[uuid.UUID]dbgen.VaultIdentity
}

func (m *memStore) CreateVaultIdentity(_ context.Context, a dbgen.CreateVaultIdentityParams) (dbgen.VaultIdentity, error) {
	for _, r := range m.rows {
		if r.WorkspaceID == a.WorkspaceID && r.Label == a.Label {
			return dbgen.VaultIdentity{}, &pgconn.PgError{Code: "23505"}
		}
	}
	r := dbgen.VaultIdentity{ID: uuid.New(), WorkspaceID: a.WorkspaceID, Label: a.Label, Description: a.Description,
		Provider: a.Provider, SecretID: a.SecretID, ScriptPath: a.ScriptPath, ScriptArgs: a.ScriptArgs,
		CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
	m.rows[r.ID] = r
	return r, nil
}

func (m *memStore) GetVaultIdentity(_ context.Context, a dbgen.GetVaultIdentityParams) (dbgen.VaultIdentity, error) {
	if r, ok := m.rows[a.ID]; ok && r.WorkspaceID == a.WorkspaceID {
		return r, nil
	}
	return dbgen.VaultIdentity{}, pgx.ErrNoRows
}

func (m *memStore) GetVaultIdentityByLabel(_ context.Context, a dbgen.GetVaultIdentityByLabelParams) (dbgen.VaultIdentity, error) {
	for _, r := range m.rows {
		if r.WorkspaceID == a.WorkspaceID && r.Label == a.Label {
			return r, nil
		}
	}
	return dbgen.VaultIdentity{}, pgx.ErrNoRows
}

func (m *memStore) ListVaultIdentities(_ context.Context, ws uuid.UUID) ([]dbgen.VaultIdentity, error) {
	var out []dbgen.VaultIdentity
	for _, r := range m.rows {
		if r.WorkspaceID == ws {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) UpdateVaultIdentity(_ context.Context, a dbgen.UpdateVaultIdentityParams) (dbgen.VaultIdentity, error) {
	r, ok := m.rows[a.ID]
	if !ok || r.WorkspaceID != a.WorkspaceID {
		return dbgen.VaultIdentity{}, pgx.ErrNoRows
	}
	r.Label, r.Description, r.Provider, r.SecretID, r.ScriptPath, r.ScriptArgs = a.Label, a.Description, a.Provider, a.SecretID, a.ScriptPath, a.ScriptArgs
	m.rows[a.ID] = r
	return r, nil
}

func (m *memStore) DeleteVaultIdentity(_ context.Context, a dbgen.DeleteVaultIdentityParams) (int64, error) {
	if r, ok := m.rows[a.ID]; ok && r.WorkspaceID == a.WorkspaceID {
		delete(m.rows, a.ID)
		return 1, nil
	}
	return 0, nil
}

type fakeSecrets struct {
	ws    uuid.UUID
	types map[uuid.UUID]string
	vals  map[uuid.UUID]string
}

func (f fakeSecrets) Get(_ context.Context, ws, id uuid.UUID) (secrets.Meta, error) {
	if t, ok := f.types[id]; ok && ws == f.ws {
		return secrets.Meta{ID: id, Type: t}, nil
	}
	return secrets.Meta{}, secrets.ErrNotFound
}

func (f fakeSecrets) Resolve(_ context.Context, ws, id uuid.UUID) (secrets.Resolved, error) {
	if t, ok := f.types[id]; ok && ws == f.ws {
		return secrets.Resolved{Type: t, Value: secrets.NewValue([]byte(f.vals[id]))}, nil
	}
	return secrets.Resolved{}, secrets.ErrNotFound
}

func TestService(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()
	vp, tok := uuid.New(), uuid.New()
	sec := fakeSecrets{ws: ws, types: map[uuid.UUID]string{vp: secrets.TypeVaultPassword, tok: secrets.TypeToken},
		vals: map[uuid.UUID]string{vp: "test-password\n"}}
	s := New(&memStore{rows: map[uuid.UUID]dbgen.VaultIdentity{}}, sec)

	bad := []Input{
		{Label: "", Provider: ProviderSecret, SecretID: &vp},
		{Label: "has space", Provider: ProviderSecret, SecretID: &vp},
		{Label: "semi;colon", Provider: ProviderSecret, SecretID: &vp},
		{Label: "x", Provider: "other"},
		{Label: "x", Provider: ProviderSecret},
		{Label: "x", Provider: ProviderSecret, SecretID: &tok},
		{Label: "x", Provider: ProviderSecret, SecretID: ptr(uuid.New())},
		{Label: "x", Provider: ProviderSecret, SecretID: &vp, ScriptPath: "/bin/x"},
		{Label: "x", Provider: ProviderScript},
		{Label: "x", Provider: ProviderScript, ScriptPath: "../x"},
		{Label: "x", Provider: ProviderScript, ScriptPath: "a\nb"},
		{Label: "x", Provider: ProviderScript, ScriptPath: "/bin/x", SecretID: &vp},
		{Label: "x", Provider: ProviderScript, ScriptPath: "/bin/x", ScriptArgs: make([]string, 33)},
		{Label: "x", Provider: ProviderScript, ScriptPath: "/bin/x", ScriptArgs: []string{"a\x00"}},
		{Label: "x", Provider: ProviderScript, ScriptPath: "/bin/x", ScriptArgs: []string{strings.Repeat("a", 513)}},
	}
	for i, in := range bad {
		if _, err := s.Create(ctx, ws, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad[%d]: %v", i, err)
		}
	}

	def, err := s.Create(ctx, ws, Input{Label: "default", Provider: ProviderSecret, SecretID: &vp})
	if err != nil || def.SecretID == nil || *def.SecretID != vp {
		t.Fatalf("create default: %+v %v", def, err)
	}
	if _, err := s.Create(ctx, ws, Input{Label: "default", Provider: ProviderSecret, SecretID: &vp}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	sc, err := s.Create(ctx, ws, Input{Label: "ext", Provider: ProviderScript, ScriptPath: "/usr/local/bin/get-pw", ScriptArgs: []string{"--id", "ext"}})
	if err != nil || sc.ScriptPath == "" || len(sc.ScriptArgs) != 2 || sc.SecretID != nil {
		t.Fatalf("create script: %+v %v", sc, err)
	}

	pw, err := s.Password(ctx, ws, "")
	if err != nil || pw.Reveal() != "test-password" {
		t.Fatalf("password: %v", err)
	}
	pw.Zero()
	if _, err := s.Password(ctx, ws, "ext"); !errors.Is(err, ErrProviderNotSupported) {
		t.Errorf("script: %v", err)
	}
	if _, err := s.Password(ctx, ws, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := s.Password(ctx, uuid.New(), "default"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other workspace: %v", err)
	}

	upd, err := s.Update(ctx, ws, sc.ID, Input{Label: "ext", Provider: ProviderSecret, SecretID: &vp})
	if err != nil || upd.Provider != ProviderSecret || upd.ScriptPath != "" {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if _, err := s.Update(ctx, ws, uuid.New(), Input{Label: "z", Provider: ProviderSecret, SecretID: &vp}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}
	if l, err := s.List(ctx, ws); err != nil || len(l) != 2 {
		t.Errorf("list: %v %v", l, err)
	}
	if err := s.Delete(ctx, ws, def.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, ws, def.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
