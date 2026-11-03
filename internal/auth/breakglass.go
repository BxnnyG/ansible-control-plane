package auth

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Break-glass defaults (ADR-0006): a local admin for emergencies when the IdP
// is unavailable. Every attempt is audited; the account is locked after
// repeated failures and the source IP is rate limited.
const (
	defaultMaxFailures   = 5
	defaultLockDuration  = 15 * time.Minute
	defaultIPMaxFailures = 10
	defaultIPWindow      = 15 * time.Minute
	maxLoginBody         = 4 << 10
	maxLimiterEntries    = 10000
	maxUsernameLen       = 128
	maxTOTPInputLen      = 16
)

var errInvalidCredentials = map[string]string{"error": "invalid credentials"}

type breakGlassLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

// ipLimiter counts failed logins per source IP in a fixed window. State is
// in memory only: it resets on restart, the per-user lockout in the database
// is the durable guard.
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*ipEntry
}

type ipEntry struct {
	count int
	reset time.Time
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, m: map[string]*ipEntry{}}
}

// blocked reports whether ip has used up its failures and for how long.
func (l *ipLimiter) blocked(ip string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[ip]
	if e == nil || !now.Before(e.reset) || e.count < l.max {
		return 0, false
	}
	return e.reset.Sub(now), true
}

func (l *ipLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= maxLimiterEntries {
		for k, e := range l.m {
			if !now.Before(e.reset) {
				delete(l.m, k)
			}
		}
		if len(l.m) >= maxLimiterEntries { // still full of live entries: drop one arbitrary entry
			for k := range l.m {
				delete(l.m, k)
				break
			}
		}
	}
	e := l.m[ip]
	if e == nil || !now.Before(e.reset) {
		e = &ipEntry{reset: now.Add(l.window)}
		l.m[ip] = e
	}
	e.count++
}

func clientIP(r *http.Request) string {
	// The app terminates TLS itself (ADR-0011); forwarded headers are
	// client-controlled and deliberately ignored.
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// dummyHash is verified when the username is unknown so that response time
// does not reveal which usernames exist.
var (
	dummyOnce sync.Once
	dummyHash string
)

func dummyPasswordHash() string {
	dummyOnce.Do(func() {
		pw, _ := GeneratePassword(24)
		dummyHash, _ = HashPassword(context.Background(), pw)
	})
	return dummyHash
}

func (s *Service) handleBreakGlassLogin(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	ip := clientIP(r)
	now := s.o.Now()
	if wait, blocked := s.limiter.blocked(ip, now); blocked {
		s.audit(r.Context(), nil, "auth.breakglass_failed", map[string]any{"reason": "ip_rate_limited"})
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many attempts"})
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" || !s.originOK(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "request rejected"})
		return
	}
	var req breakGlassLoginRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		len(req.Username) > maxUsernameLen || len(req.TOTP) > maxTOTPInputLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	user, reason, err := s.checkBreakGlass(r.Context(), req, now)
	if err != nil {
		s.o.Log.Error("breakglass login: internal error", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if reason != "" {
		s.limiter.fail(ip, now)
		var u *dbgen.User
		if user.ID != uuid.Nil {
			u = &user
		}
		s.audit(r.Context(), u, "auth.breakglass_failed", map[string]any{"reason": reason})
		writeJSON(w, http.StatusUnauthorized, errInvalidCredentials)
		return
	}
	if err := s.startSession(w, r, user, ""); err != nil {
		s.o.Log.Error("breakglass login: start session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.audit(r.Context(), &user, "auth.breakglass_login", map[string]any{"method": "breakglass"})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// checkBreakGlass validates the credentials. It returns a non-empty failure
// reason (for the audit log only, never sent to the client) or an error for
// infrastructure problems. The same expensive work is done for unknown users,
// locked accounts and wrong passwords to keep timing uniform.
func (s *Service) checkBreakGlass(ctx context.Context, req breakGlassLoginRequest, now time.Time) (dbgen.User, string, error) {
	u, err := s.o.Store.GetBreakGlassUser(ctx, textVal(req.Username))
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return u, "", err
	}
	hash := dummyPasswordHash()
	if known && u.PasswordHash.Valid {
		hash = u.PasswordHash.String
	}
	pwOK, err := VerifyPassword(ctx, req.Password, hash)
	if err != nil && !errors.Is(err, errBadHash) {
		return u, "", err
	}
	if !known {
		return dbgen.User{}, "unknown_user", nil
	}
	if u.Disabled {
		return u, "user_disabled", nil
	}
	if u.LockedUntil.Valid && u.LockedUntil.Time.After(now) {
		return u, "locked", nil
	}
	reason := ""
	var step int64
	if !pwOK {
		reason = "bad_password"
	} else {
		var totpOK bool
		step, totpOK, err = s.verifyUserTOTP(u, req.TOTP, now)
		if err != nil {
			return u, "", err
		}
		if !totpOK {
			reason = "bad_totp"
		}
	}
	if reason == "" {
		// Atomic replay guard: only a step newer than the last accepted one wins.
		if _, err := s.o.Store.AcceptBreakGlassLogin(ctx, dbgen.AcceptBreakGlassLoginParams{ID: u.ID, TotpLastStep: step}); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return u, "", err
			}
			reason = "totp_replay"
		}
	}
	if reason == "" {
		return u, "", nil
	}
	if _, err := s.o.Store.RecordBreakGlassFailure(ctx, dbgen.RecordBreakGlassFailureParams{
		ID: u.ID, MaxFailures: int32(s.bg.maxFailures), LockSeconds: s.bg.lockFor.Seconds(), //nolint:gosec // small constant
	}); err != nil {
		return u, "", err
	}
	return u, reason, nil
}

func (s *Service) verifyUserTOTP(u dbgen.User, code string, now time.Time) (int64, bool, error) {
	if len(u.TotpEnvelope) == 0 {
		return 0, false, nil // no second factor enrolled: login impossible (TOTP is mandatory)
	}
	sec, err := OpenTOTPSecret(s.o.Secrets, u.ID.String(), u.TotpEnvelope)
	if err != nil {
		return 0, false, err
	}
	defer sec.Zero()
	step, ok := VerifyTOTP(sec.Reveal(), code, now)
	return step, ok, nil
}

type breakGlassSettings struct {
	maxFailures int
	lockFor     time.Duration
}

func newBreakGlassSettings(o Options) (breakGlassSettings, *ipLimiter) {
	bs := breakGlassSettings{maxFailures: o.MaxFailures, lockFor: o.LockDuration}
	if bs.maxFailures <= 0 {
		bs.maxFailures = defaultMaxFailures
	}
	if bs.lockFor <= 0 {
		bs.lockFor = defaultLockDuration
	}
	ipMax, win := o.IPMaxFailures, o.IPWindow
	if ipMax <= 0 {
		ipMax = defaultIPMaxFailures
	}
	if win <= 0 {
		win = defaultIPWindow
	}
	return bs, newIPLimiter(ipMax, win)
}
