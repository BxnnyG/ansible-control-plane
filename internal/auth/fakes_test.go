package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// fakeIdP is an in-process OIDC provider with PKCE verification.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]idpCode
	// knobs
	subject, email, name string
	roles                map[string]any
	nonceOverride        string
	audience             string
	expired              bool
	tokenFails           bool
	otherKey             *rsa.PrivateKey
}

type idpCode struct{ challenge, nonce string }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, codes: map[string]idpCode{}, subject: "sub-1", email: "a@example.com", name: "Alice",
		roles: map[string]any{"acp-admin": map[string]any{"org1": "example.com"}}, audience: "client-1"}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
			"end_session_endpoint":                  f.srv.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", f.token)
	return f
}

// authorize simulates the user approving the request and returns the code.
func (f *fakeIdP) authorize(challenge, nonce string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, _ := randomToken(8)
	f.codes[c] = idpCode{challenge, nonce}
	return c
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	c, ok := f.codes[r.Form.Get("code")]
	delete(f.codes, r.Form.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if f.tokenFails || !ok || r.Form.Get("client_secret") != "" || r.Form.Get("client_id") != "client-1" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	nonce := c.nonce
	if f.nonceOverride != "" {
		nonce = f.nonceOverride
	}
	signKey := f.key
	if f.otherKey != nil {
		signKey = f.otherKey
	}
	sg, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	exp := time.Now().Add(5 * time.Minute)
	if f.expired {
		exp = time.Now().Add(-5 * time.Minute)
	}
	raw, _ := jwt.Signed(sg).Claims(jwt.Claims{
		Issuer: f.srv.URL, Subject: f.subject, Audience: jwt.Audience{f.audience},
		Expiry: jwt.NewNumericDate(exp), IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}).Claims(map[string]any{
		"nonce": nonce, "email": f.email, "name": f.name,
		"urn:zitadel:iam:org:project:roles": f.roles,
	}).Serialize()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at-secret", "token_type": "Bearer", "expires_in": 300, "id_token": raw,
	})
}

// memStore is an in-memory Store.
type memStore struct {
	mu       sync.Mutex
	users    map[uuid.UUID]dbgen.User
	sessions map[uuid.UUID]dbgen.Session
	audit    []dbgen.InsertAuditParams
	noAccess bool
	now      func() time.Time
}

func newMemStore(now func() time.Time) *memStore {
	return &memStore{users: map[uuid.UUID]dbgen.User{}, sessions: map[uuid.UUID]dbgen.Session{}, now: now}
}

func (m *memStore) UpsertOIDCUser(_ context.Context, a dbgen.UpsertOIDCUserParams) (dbgen.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, u := range m.users {
		if u.OidcIssuer == a.OidcIssuer && u.OidcSubject == a.OidcSubject {
			u.Email, u.DisplayName = a.Email, a.DisplayName
			m.users[id] = u
			return u, nil
		}
	}
	u := dbgen.User{ID: uuid.New(), OidcIssuer: a.OidcIssuer, OidcSubject: a.OidcSubject, Email: a.Email, DisplayName: a.DisplayName}
	m.users[u.ID] = u
	return u, nil
}

func (m *memStore) GetUser(_ context.Context, id uuid.UUID) (dbgen.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return u, pgx.ErrNoRows
	}
	return u, nil
}

func (m *memStore) CreateSession(_ context.Context, a dbgen.CreateSessionParams) (dbgen.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := dbgen.Session{ID: uuid.New(), TokenHash: a.TokenHash, CsrfHash: a.CsrfHash, UserID: a.UserID, IDTokenHint: a.IDTokenHint,
		LastSeenAt: tstz(m.now()), IdleExpiresAt: a.IdleExpiresAt, ExpiresAt: a.ExpiresAt}
	m.sessions[s.ID] = s
	return s, nil
}

func (m *memStore) GetActiveSession(_ context.Context, h []byte) (dbgen.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if string(s.TokenHash) == string(h) && s.IdleExpiresAt.Time.After(m.now()) && s.ExpiresAt.Time.After(m.now()) {
			return s, nil
		}
	}
	return dbgen.Session{}, pgx.ErrNoRows
}

func (m *memStore) TouchSession(_ context.Context, a dbgen.TouchSessionParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[a.ID]
	s.LastSeenAt = tstz(m.now())
	if a.Column2.Time.Before(s.ExpiresAt.Time) {
		s.IdleExpiresAt = a.Column2
	} else {
		s.IdleExpiresAt = s.ExpiresAt
	}
	m.sessions[a.ID] = s
	return nil
}

func (m *memStore) DeleteSession(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
	return nil
}

func (m *memStore) InsertAudit(_ context.Context, a dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, a)
	return dbgen.InsertAuditRow{}, nil
}

// ListMembershipsForUser reports one membership unless noAccess is set.
func (m *memStore) ListMembershipsForUser(context.Context, uuid.UUID) ([]dbgen.Membership, error) {
	if m.noAccess {
		return nil, nil
	}
	return []dbgen.Membership{{Role: "viewer"}}, nil
}

func (m *memStore) ListMembershipSummariesForUser(context.Context, uuid.UUID) ([]dbgen.ListMembershipSummariesForUserRow, error) {
	return nil, nil
}

func (m *memStore) actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, a := range m.audit {
		out = append(out, a.Action)
	}
	return out
}

var _ = errors.New
var _ pgtype.Text

// Break-glass store methods. Replay/lockout semantics mirror the SQL queries.

func (m *memStore) addBreakGlass(u dbgen.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[u.ID] = u
}

func (m *memStore) GetBreakGlassUser(_ context.Context, name pgtype.Text) (dbgen.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.IsBreakGlass && u.Username == name {
			return u, nil
		}
	}
	return dbgen.User{}, pgx.ErrNoRows
}

func (m *memStore) RecordBreakGlassFailure(_ context.Context, a dbgen.RecordBreakGlassFailureParams) (dbgen.RecordBreakGlassFailureRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[a.ID]
	u.FailedLogins++
	if u.FailedLogins >= a.MaxFailures {
		u.FailedLogins = 0
		u.LockedUntil = tstz(m.now().Add(time.Duration(a.LockSeconds * float64(time.Second))))
	}
	m.users[a.ID] = u
	return dbgen.RecordBreakGlassFailureRow{FailedLogins: u.FailedLogins, LockedUntil: u.LockedUntil}, nil
}

func (m *memStore) AcceptBreakGlassLogin(_ context.Context, a dbgen.AcceptBreakGlassLoginParams) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[a.ID]
	if u.TotpLastStep >= a.TotpLastStep {
		return uuid.Nil, pgx.ErrNoRows
	}
	u.TotpLastStep, u.FailedLogins, u.LockedUntil = a.TotpLastStep, 0, pgtype.Timestamptz{}
	m.users[a.ID] = u
	return a.ID, nil
}
