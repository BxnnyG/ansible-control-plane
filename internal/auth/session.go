package auth

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Principal is the authenticated caller attached to the request context.
// Authorization (policy package) builds on it and runs after Require.
type Principal struct {
	User        dbgen.User
	SessionID   uuid.UUID
	IDTokenHint string
}

type ctxKey struct{}

// FromContext returns the Principal set by Require; it panics-free returns a
// zero value when absent, so handlers must be mounted behind Require.
func FromContext(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxKey{}).(Principal)
	return p
}

// WithPrincipal attaches a principal (used by Require and by tests of
// downstream middleware).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Require is middleware that demands a valid session and, for state-changing
// methods, a valid CSRF token and same-origin Origin header.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.names.session)
		if err != nil || c.Value == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		sess, err := s.o.Store.GetActiveSession(r.Context(), hashToken(c.Value))
		if err != nil {
			s.clearCookie(w, s.names.session, true)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		user, err := s.o.Store.GetUser(r.Context(), sess.UserID)
		if err != nil || user.Disabled {
			_ = s.o.Store.DeleteSession(r.Context(), sess.ID)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		if !safeMethod(r.Method) && !s.csrfOK(r, sess) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf check failed"})
			return
		}
		now := s.o.Now()
		if now.Sub(sess.LastSeenAt.Time) > touchEvery {
			idle := now.Add(s.o.IdleTimeout)
			if err := s.o.Store.TouchSession(r.Context(), dbgen.TouchSessionParams{ID: sess.ID, Column2: tstz(idle)}); err != nil {
				s.o.Log.Warn("touch session failed", "error", err)
			}
		}
		pr := Principal{User: user, SessionID: sess.ID, IDTokenHint: sess.IDTokenHint.String}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), pr)))
	})
}

func (s *Service) csrfOK(r *http.Request, sess dbgen.Session) bool {
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" || subtle.ConstantTimeCompare(hashToken(tok), sess.CsrfHash) != 1 {
		return false
	}
	return s.originOK(r)
}

// originOK is defence in depth: a present Origin header must match the public origin.
func (s *Service) originOK(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		pu, err1 := url.Parse(s.o.PublicURL)
		ou, err2 := url.Parse(o)
		if err1 != nil || err2 != nil || ou.Scheme != pu.Scheme || ou.Host != pu.Host {
			return false
		}
	} else if o == "null" {
		return false
	}
	return true
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func constEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func textVal(s string) pgtype.Text        { return pgtype.Text{String: s, Valid: true} }
func pgUUID(u uuid.UUID) pgtype.UUID      { return pgtype.UUID{Bytes: u, Valid: true} }
func tstz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
