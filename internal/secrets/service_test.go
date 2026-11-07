package secrets

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

type memStore struct {
	rows         map[uuid.UUID]dbgen.Secret
	beforeUpdate func() // test hook: runs once before the next update
}

func newMem() *memStore { return &memStore{rows: map[uuid.UUID]dbgen.Secret{}} }

func (m *memStore) CreateSecret(_ context.Context, a dbgen.CreateSecretParams) (dbgen.Secret, error) {
	for _, r := range m.rows {
		if r.WorkspaceID == a.WorkspaceID && r.Name == a.Name {
			return dbgen.Secret{}, errors.New("dup") // mapped below by tests that need it
		}
	}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	s := dbgen.Secret{ID: a.ID, WorkspaceID: a.WorkspaceID, Name: a.Name, Type: a.Type, Description: a.Description,
		EnvVersion: a.EnvVersion, EnvKeyID: a.EnvKeyID, EnvWrapNonce: a.EnvWrapNonce, EnvWrappedDek: a.EnvWrappedDek,
		EnvNonce: a.EnvNonce, EnvCiphertext: a.EnvCiphertext, Metadata: a.Metadata, CreatedAt: now, UpdatedAt: now}
	m.rows[a.ID] = s
	return s, nil
}

func (m *memStore) GetSecret(_ context.Context, a dbgen.GetSecretParams) (dbgen.Secret, error) {
	s, ok := m.rows[a.ID]
	if !ok || s.WorkspaceID != a.WorkspaceID {
		return dbgen.Secret{}, pgx.ErrNoRows
	}
	return s, nil
}

func (m *memStore) ListSecretsMeta(_ context.Context, ws uuid.UUID) ([]dbgen.ListSecretsMetaRow, error) {
	var out []dbgen.ListSecretsMetaRow
	for _, s := range m.rows {
		if s.WorkspaceID == ws {
			out = append(out, dbgen.ListSecretsMetaRow{ID: s.ID, WorkspaceID: ws, Name: s.Name, Type: s.Type, Metadata: s.Metadata})
		}
	}
	return out, nil
}

func (m *memStore) UpdateSecretWithEnvelope(_ context.Context, a dbgen.UpdateSecretWithEnvelopeParams) (dbgen.UpdateSecretWithEnvelopeRow, error) {
	if m.beforeUpdate != nil {
		f := m.beforeUpdate
		m.beforeUpdate = nil
		f()
	}
	s := m.rows[a.ID]
	if !s.UpdatedAt.Time.Equal(a.UpdatedAt.Time) {
		return dbgen.UpdateSecretWithEnvelopeRow{}, pgx.ErrNoRows
	}
	s.UpdatedAt = pgtype.Timestamptz{Time: s.UpdatedAt.Time.Add(time.Microsecond), Valid: true}
	s.Name, s.Description, s.Metadata = a.Name, a.Description, a.Metadata
	s.EnvVersion, s.EnvKeyID, s.EnvWrapNonce, s.EnvWrappedDek = a.EnvVersion, a.EnvKeyID, a.EnvWrapNonce, a.EnvWrappedDek
	s.EnvNonce, s.EnvCiphertext = a.EnvNonce, a.EnvCiphertext
	m.rows[a.ID] = s
	return dbgen.UpdateSecretWithEnvelopeRow{ID: s.ID, WorkspaceID: s.WorkspaceID, Name: s.Name, Type: s.Type,
		Description: s.Description, Metadata: s.Metadata}, nil
}

func (m *memStore) DeleteSecret(_ context.Context, a dbgen.DeleteSecretParams) error {
	delete(m.rows, a.ID)
	return nil
}

func testService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	p, err := NewLocalProvider(k)
	if err != nil {
		t.Fatal(err)
	}
	st := newMem()
	return NewService(st, p), st
}

func genSSHKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestServiceRoundTrip(t *testing.T) {
	svc, st := testService(t)
	ctx, ws := context.Background(), uuid.New()
	m, err := svc.Create(ctx, ws, CreateInput{Name: " db ", Type: TypePassword, Value: []byte("hunter2-test")})
	if err != nil || m.Name != "db" {
		t.Fatalf("create: %v %+v", err, m)
	}
	if strings.Contains(string(st.rows[m.ID].EnvCiphertext), "hunter2") {
		t.Error("ciphertext contains plaintext")
	}
	r, err := svc.Resolve(ctx, ws, m.ID)
	if err != nil || string(r.Value.Reveal()) != "hunter2-test" || r.Type != TypePassword {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := svc.Resolve(ctx, uuid.New(), m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-workspace resolve: %v", err)
	}
	newName := "db2"
	if _, err := svc.Update(ctx, ws, m.ID, UpdateInput{Name: &newName, Value: []byte("changed-test")}); err != nil {
		t.Fatal(err)
	}
	r, _ = svc.Resolve(ctx, ws, m.ID)
	if string(r.Value.Reveal()) != "changed-test" {
		t.Error("value not replaced")
	}
	got, _ := svc.Get(ctx, ws, m.ID)
	if got.Name != "db2" {
		t.Errorf("name %q", got.Name)
	}
	if err := svc.Delete(ctx, ws, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, ws, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestServiceValidation(t *testing.T) {
	svc, _ := testService(t)
	ctx, ws := context.Background(), uuid.New()
	tests := []struct {
		name string
		in   CreateInput
	}{
		{"empty name", CreateInput{Name: " ", Type: TypeToken, Value: []byte("x")}},
		{"unknown type", CreateInput{Name: "a", Type: "bogus", Value: []byte("x")}},
		{"empty value", CreateInput{Name: "a", Type: TypeToken}},
		{"oversized", CreateInput{Name: "a", Type: TypeFile, Value: make([]byte, MaxValueSize+1)}},
		{"bad ssh key", CreateInput{Name: "a", Type: TypeSSHKey, Value: []byte("not a key")}},
		{"control char name", CreateInput{Name: "a\x00b", Type: TypeToken, Value: []byte("x")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, ws, tc.in); !errors.Is(err, ErrInvalid) {
				t.Errorf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestSSHKeyFingerprint(t *testing.T) {
	svc, _ := testService(t)
	ctx, ws := context.Background(), uuid.New()
	m, err := svc.Create(ctx, ws, CreateInput{Name: "k", Type: TypeSSHKey, Value: genSSHKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := m.Metadata["fingerprint"].(string)
	if !strings.HasPrefix(fp, "SHA256:") || m.Metadata["key_type"] != "ssh-ed25519" {
		t.Errorf("metadata %v", m.Metadata)
	}
	m2, err := svc.Update(ctx, ws, m.ID, UpdateInput{Value: genSSHKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Metadata["fingerprint"] == fp {
		t.Error("fingerprint not updated after value replacement")
	}
	if _, err := svc.Update(ctx, ws, m.ID, UpdateInput{Value: []byte("garbage")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid replacement: %v", err)
	}
}

func TestConnectorSecretTypes(t *testing.T) {
	for _, tc := range []struct {
		typ, val string
		ok       bool
	}{
		{TypeAPIToken, "tok", true},
		{TypeAPIKeyPair, `{"key":"k","secret":"s"}`, true},
		{TypeAPIKeyPair, `{"key":"k"}`, false},
		{TypeAPIKeyPair, `not json`, false},
	} {
		if !ValidType(tc.typ) {
			t.Fatalf("%s must be a valid type", tc.typ)
		}
		_, _, err := prepareValue(tc.typ, []byte(tc.val))
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: err=%v, want ok=%v", tc.typ, tc.val, err, tc.ok)
		}
	}
}

// A name-only PUT that loses the race against a value PUT must keep the new value.
func TestUpdateDoesNotLoseConcurrentUpdate(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	ws := uuid.New()
	m, err := svc.Create(ctx, ws, CreateInput{Name: "k", Type: TypePassword, Value: []byte("old-value-test")})
	if err != nil {
		t.Fatal(err)
	}
	st.beforeUpdate = func() { // concurrent writer commits between our read and write
		if _, err := svc.Update(ctx, ws, m.ID, UpdateInput{Value: []byte("new-value-test")}); err != nil {
			t.Error(err)
		}
	}
	newName := "renamed"
	if _, err := svc.Update(ctx, ws, m.ID, UpdateInput{Name: &newName}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Resolve(ctx, ws, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Value.Reveal()) != "new-value-test" || st.rows[m.ID].Name != "renamed" {
		t.Fatalf("lost update: name=%q", st.rows[m.ID].Name)
	}
}

func TestUpdateDeletedWhileRacing(t *testing.T) {
	svc, st := testService(t)
	ctx := context.Background()
	ws := uuid.New()
	m, _ := svc.Create(ctx, ws, CreateInput{Name: "k", Type: TypePassword, Value: []byte("old-value-test")})
	st.beforeUpdate = func() { delete(st.rows, m.ID) }
	n := "x"
	if _, err := svc.Update(ctx, ws, m.ID, UpdateInput{Name: &n}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
