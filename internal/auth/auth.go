// Package auth implements OIDC login (authorization code + PKCE, BFF pattern),
// server-side sessions and CSRF protection (ADR-0006).
//
// Tokens never reach the browser: the callback exchanges the code on the
// server, validates the ID token and issues an opaque session cookie. Only the
// SHA-256 of the session token and of the CSRF token is stored.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const (
	callbackPath = "/api/auth/oidc/callback"
	loginPath    = "/login"
	flowTTL      = 10 * time.Minute
	touchEvery   = time.Minute
)

// Store is the persistence the auth service needs; *dbgen.Queries implements it.
type Store interface {
	UpsertOIDCUser(ctx context.Context, arg dbgen.UpsertOIDCUserParams) (dbgen.User, error)
	GetUser(ctx context.Context, id uuid.UUID) (dbgen.User, error)
	CreateSession(ctx context.Context, arg dbgen.CreateSessionParams) (dbgen.Session, error)
	GetActiveSession(ctx context.Context, tokenHash []byte) (dbgen.Session, error)
	TouchSession(ctx context.Context, arg dbgen.TouchSessionParams) error
	DeleteSession(ctx context.Context, id uuid.UUID) error
	InsertAudit(ctx context.Context, arg dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error)
	ListMembershipsForUser(ctx context.Context, userID uuid.UUID) ([]dbgen.Membership, error)
	ListMembershipSummariesForUser(ctx context.Context, userID uuid.UUID) ([]dbgen.ListMembershipSummariesForUserRow, error)
	GetBreakGlassUser(ctx context.Context, username pgtype.Text) (dbgen.User, error)
	RecordBreakGlassFailure(ctx context.Context, arg dbgen.RecordBreakGlassFailureParams) (dbgen.RecordBreakGlassFailureRow, error)
	AcceptBreakGlassLogin(ctx context.Context, arg dbgen.AcceptBreakGlassLoginParams) (uuid.UUID, error)
}

// RoleSyncer is the hook for mapping IdP roles to workspace memberships. It is
// called after every successful login with the roles from the roles claim.
// IdPRoleSyncer is the implementation. When a syncer is configured, a login
// that leaves the user without any membership is rejected (no_access).
type RoleSyncer interface {
	SyncRoles(ctx context.Context, user dbgen.User, roles []string) error
}

// Options configure a Service.
type Options struct {
	Issuer          string
	ClientID        string
	Scopes          []string
	RolesClaim      string
	PublicURL       string // only source for redirect targets
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	StateKey        []byte // key material for the sealed login-state cookie
	Store           Store
	Log             *slog.Logger
	RoleSync        RoleSyncer
	HTTPClient      *http.Client // optional, for IdP calls
	Now             func() time.Time

	// Break-glass login (ADR-0006). Needs Secrets to open the sealed TOTP secret.
	BreakGlass bool
	Secrets    secrets.Provider
	// Tuning; zero values select the defaults (5 failures / 15 min lock per
	// user, 10 failures / 15 min window per IP).
	MaxFailures   int
	LockDuration  time.Duration
	IPMaxFailures int
	IPWindow      time.Duration
}

// Service handles login, logout and session authentication.
type Service struct {
	o      Options
	sealer *sealer
	secure bool
	names  cookieNames

	bg      breakGlassSettings
	limiter *ipLimiter

	mu       sync.Mutex
	provider *oidc.Provider
	endSess  string
}

type cookieNames struct{ session, csrf, flow string }

// New validates the options and returns a Service. IdP discovery is lazy so
// the app starts even if the IdP is temporarily unreachable.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Log == nil {
		return nil, errors.New("auth: store and logger are required")
	}
	pu, err := url.Parse(o.PublicURL)
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || pu.Host == "" {
		return nil, errors.New("auth: a valid public URL is required")
	}
	if (o.Issuer == "") != (o.ClientID == "") {
		return nil, errors.New("auth: issuer and client ID must be set together")
	}
	if o.Issuer == "" && !o.BreakGlass {
		return nil, errors.New("auth: neither OIDC nor break-glass login is enabled")
	}
	if o.BreakGlass && o.Secrets == nil {
		return nil, errors.New("auth: break-glass login needs a secrets provider")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	if o.IdleTimeout <= 0 || o.AbsoluteTimeout <= 0 {
		return nil, errors.New("auth: session timeouts must be positive")
	}
	sl, err := newSealer(o.StateKey)
	if err != nil {
		return nil, err
	}
	s := &Service{o: o, sealer: sl, secure: pu.Scheme == "https"}
	s.bg, s.limiter = newBreakGlassSettings(o)
	// The __Host- prefix requires Secure, so it is only used over HTTPS.
	pre := ""
	if s.secure {
		pre = "__Host-"
	}
	s.names = cookieNames{session: pre + "acp_session", csrf: pre + "acp_csrf", flow: pre + "acp_oidc_flow"}
	return s, nil
}

func (s *Service) ctx(ctx context.Context) context.Context {
	if s.o.HTTPClient != nil {
		return oidc.ClientContext(ctx, s.o.HTTPClient)
	}
	return ctx
}

// discover returns the cached provider, performing discovery on first use.
func (s *Service) discover(_ context.Context) (*oidc.Provider, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.provider != nil {
		return s.provider, s.endSess, nil
	}
	// The provider keeps using this context for JWKS refreshes, so it must
	// outlive the request.
	p, err := oidc.NewProvider(withTimeoutClient(s.ctx(context.Background()), s.o.HTTPClient), s.o.Issuer)
	if err != nil {
		return nil, "", fmt.Errorf("oidc discovery: %w", err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err != nil {
		return nil, "", fmt.Errorf("oidc metadata: %w", err)
	}
	s.provider, s.endSess = p, meta.EndSession
	return p, s.endSess, nil
}

func withTimeoutClient(ctx context.Context, c *http.Client) context.Context {
	if c != nil {
		return ctx
	}
	return oidc.ClientContext(ctx, &http.Client{Timeout: 15 * time.Second})
}

func (s *Service) oauth(p *oidc.Provider) *oauth2.Config {
	ep := p.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInParams // public client: client_id in body, no secret
	return &oauth2.Config{
		ClientID:    s.o.ClientID,
		Endpoint:    ep,
		RedirectURL: s.o.PublicURL + callbackPath,
		Scopes:      s.o.Scopes,
	}
}

// Routes registers the auth endpoints. Mount on the root router.
func (s *Service) Routes(r chi.Router) {
	r.Get("/api/auth/config", s.handleConfig)
	if s.o.Issuer != "" {
		r.Get("/api/auth/oidc/login", s.handleLogin)
		r.Get(callbackPath, s.handleCallback)
	}
	if s.o.BreakGlass {
		r.Post("/api/auth/breakglass/login", s.handleBreakGlassLogin)
	}
	r.With(s.Require).Post("/api/auth/logout", s.handleLogout)
	r.With(s.Require).Get("/api/v1/me", s.handleMe)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	p, _, err := s.discover(r.Context())
	if err != nil {
		s.o.Log.Error("login: idp unavailable", "error", err)
		http.Redirect(w, r, loginPath+"?error=idp_unavailable", http.StatusSeeOther)
		return
	}
	state, err1 := randomToken(24)
	nonce, err2 := randomToken(24)
	if err1 != nil || err2 != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	sealed, err := s.sealer.seal(flowState{
		State: state, Nonce: nonce, Verifier: verifier,
		ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")),
		Expires:  s.o.Now().Add(flowTTL).Unix(),
	})
	if err != nil {
		s.o.Log.Error("login: seal state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, s.names.flow, sealed, true, s.o.Now().Add(flowTTL))
	authURL := s.oauth(p).AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce))
	noStore(w)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	fc, _ := r.Cookie(s.names.flow)
	s.clearCookie(w, s.names.flow, true)
	fail := func(code, reason string) {
		s.audit(r.Context(), nil, "auth.login_failed", map[string]any{"reason": reason})
		http.Redirect(w, r, loginPath+"?error="+code, http.StatusSeeOther)
	}
	if fc == nil {
		fail("login_failed", "missing_state_cookie")
		return
	}
	st, err := s.sealer.open(fc.Value, s.o.Now())
	if err != nil {
		fail("login_failed", "invalid_state_cookie")
		return
	}
	q := r.URL.Query()
	if !constEq(q.Get("state"), st.State) {
		fail("login_failed", "state_mismatch")
		return
	}
	if q.Get("error") != "" {
		fail("login_failed", "idp_error")
		return
	}
	code := q.Get("code")
	if code == "" {
		fail("login_failed", "missing_code")
		return
	}
	p, _, err := s.discover(r.Context())
	if err != nil {
		s.o.Log.Error("callback: idp unavailable", "error", err)
		fail("idp_unavailable", "idp_unavailable")
		return
	}
	ctx := s.ctx(r.Context())
	tok, err := s.oauth(p).Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		s.o.Log.Warn("callback: token exchange failed", "error", sanitizeErr(err))
		fail("login_failed", "token_exchange_failed")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		fail("login_failed", "missing_id_token")
		return
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: s.o.ClientID}).Verify(ctx, rawID)
	if err != nil {
		s.o.Log.Warn("callback: id token rejected", "error", sanitizeErr(err))
		fail("login_failed", "id_token_invalid")
		return
	}
	if !constEq(idt.Nonce, st.Nonce) {
		fail("login_failed", "nonce_mismatch")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		fail("login_failed", "claims_invalid")
		return
	}
	user, err := s.provisionUser(r.Context(), idt, claims)
	if err != nil {
		if errors.Is(err, errDisabled) {
			fail("account_disabled", "user_disabled")
			return
		}
		s.o.Log.Error("callback: provision user", "error", err)
		fail("login_failed", "provision_failed")
		return
	}
	roles := extractRoles(claims, s.o.RolesClaim)
	if s.o.RoleSync != nil {
		if err := s.o.RoleSync.SyncRoles(r.Context(), user, roles); err != nil {
			s.o.Log.Error("callback: role sync", "error", err, "user_id", user.ID)
		}
		ms, err := s.o.Store.ListMembershipsForUser(r.Context(), user.ID)
		if err != nil {
			s.o.Log.Error("callback: list memberships", "error", err, "user_id", user.ID)
			fail("login_failed", "membership_check_failed")
			return
		}
		if len(ms) == 0 {
			s.audit(r.Context(), &user, "auth.login_denied", map[string]any{"reason": "no_access", "method": "oidc"})
			http.Redirect(w, r, loginPath+"?error=no_access", http.StatusSeeOther)
			return
		}
	}
	if err := s.startSession(w, r, user, rawID); err != nil {
		s.o.Log.Error("callback: start session", "error", err)
		fail("login_failed", "session_failed")
		return
	}
	s.audit(r.Context(), &user, "auth.login", map[string]any{"method": "oidc", "roles": len(roles)})
	http.Redirect(w, r, s.o.PublicURL+safeReturnTo(st.ReturnTo), http.StatusSeeOther)
}

var errDisabled = errors.New("user disabled")

func (s *Service) provisionUser(ctx context.Context, idt *oidc.IDToken, claims map[string]any) (dbgen.User, error) {
	str := func(k string) string { v, _ := claims[k].(string); return v }
	name := str("name")
	if name == "" {
		name = str("preferred_username")
	}
	if name == "" {
		name = str("email")
	}
	u, err := s.o.Store.UpsertOIDCUser(ctx, dbgen.UpsertOIDCUserParams{
		OidcIssuer:  textVal(idt.Issuer),
		OidcSubject: textVal(idt.Subject),
		Email:       str("email"),
		DisplayName: name,
	})
	if err != nil {
		return u, fmt.Errorf("upsert user: %w", err)
	}
	if u.Disabled {
		return u, errDisabled
	}
	return u, nil
}

func (s *Service) startSession(w http.ResponseWriter, r *http.Request, u dbgen.User, idToken string) error {
	tok, err1 := randomToken(32)
	csrf, err2 := randomToken(32)
	if err1 != nil || err2 != nil {
		return errors.New("generate session tokens")
	}
	now := s.o.Now()
	abs := now.Add(s.o.AbsoluteTimeout)
	idle := now.Add(s.o.IdleTimeout)
	if idle.After(abs) {
		idle = abs
	}
	if _, err := s.o.Store.CreateSession(r.Context(), dbgen.CreateSessionParams{
		TokenHash: hashToken(tok), CsrfHash: hashToken(csrf), UserID: u.ID,
		IDTokenHint:   textVal(idToken),
		IdleExpiresAt: tstz(idle), ExpiresAt: tstz(abs),
	}); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	s.setCookie(w, s.names.session, tok, true, abs)
	s.setCookie(w, s.names.csrf, csrf, false, abs) // readable by the SPA, echoed in X-CSRF-Token
	return nil
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	pr := FromContext(r.Context())
	if err := s.o.Store.DeleteSession(r.Context(), pr.SessionID); err != nil {
		s.o.Log.Error("logout: delete session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.clearCookie(w, s.names.session, true)
	s.clearCookie(w, s.names.csrf, false)
	s.audit(r.Context(), &pr.User, "auth.logout", nil)
	redirect := loginPath
	if pr.IDTokenHint == "" || s.o.Issuer == "" {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": redirect})
		return
	}
	if _, end, err := s.discover(r.Context()); err == nil && end != "" {
		v := url.Values{
			"id_token_hint":            {pr.IDTokenHint},
			"post_logout_redirect_uri": {s.o.PublicURL + loginPath},
			"client_id":                {s.o.ClientID},
		}
		redirect = end + "?" + v.Encode()
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": redirect})
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	pr := FromContext(r.Context())
	ms, err := s.o.Store.ListMembershipSummariesForUser(r.Context(), pr.User.ID)
	if err != nil {
		s.o.Log.Error("me: list memberships", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	type mem struct {
		WorkspaceID uuid.UUID `json:"workspace_id"`
		Slug        string    `json:"slug"`
		Name        string    `json:"name"`
		Role        string    `json:"role"`
		Scoped      bool      `json:"scoped"`
	}
	out := struct {
		ID          uuid.UUID `json:"id"`
		Email       string    `json:"email"`
		DisplayName string    `json:"display_name"`
		Memberships []mem     `json:"memberships"`
	}{ID: pr.User.ID, Email: pr.User.Email, DisplayName: pr.User.DisplayName, Memberships: []mem{}}
	for _, m := range ms {
		out.Memberships = append(out.Memberships, mem{m.WorkspaceID, m.Slug, m.Name, m.Role, m.Scoped})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) audit(ctx context.Context, u *dbgen.User, action string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	b, _ := json.Marshal(details)
	p := dbgen.InsertAuditParams{Action: action, ActorLabel: "anonymous", TargetType: "auth", Details: b}
	if u != nil {
		p.ActorID = pgUUID(u.ID)
		p.ActorLabel = u.DisplayName
		p.TargetID = u.ID.String()
	}
	// Audit must not be cancelled together with the request.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.o.Store.InsertAudit(actx, p); err != nil {
		s.o.Log.Error("audit write failed", "action", action, "error", err)
	}
}

func (s *Service) setCookie(w http.ResponseWriter, name, val string, httpOnly bool, exp time.Time) {
	//nolint:gosec // Secure follows the public URL scheme; csrf cookie is deliberately script-readable
	http.SetCookie(w, &http.Cookie{Name: name, Value: val, Path: "/", Expires: exp, HttpOnly: httpOnly,
		Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

func (s *Service) clearCookie(w http.ResponseWriter, name string, httpOnly bool) {
	//nolint:gosec // clearing cookie, same attributes as setCookie
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: httpOnly,
		Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func writeJSON(w http.ResponseWriter, code int, v any) {
	noStore(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// sanitizeErr strips response bodies that could carry tokens from oauth2 errors.
func sanitizeErr(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return fmt.Sprintf("token endpoint status %d code %q", re.Response.StatusCode, re.ErrorCode)
	}
	msg := err.Error()
	if i := strings.Index(msg, "\n"); i >= 0 {
		msg = msg[:i]
	}
	return msg
}
