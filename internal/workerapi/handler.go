// Package workerapi implements the app side of the internal worker protocol
// (ADR-0016): claim, bundle, events, heartbeat and finish, plus the
// background job that marks runs without heartbeat as lost.
package workerapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runmask"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

const (
	maxEventsBody = 4 << 20
	maxChunkBytes = 128 << 10
	maxHostLen    = 255
	maxTaskLen    = 200
	pollInterval  = time.Second

	// LostAfter is the heartbeat silence after which a run is marked lost.
	LostAfter = 60 * time.Second
	// MaintenanceInterval is the period of the lost-run job.
	MaintenanceInterval = 15 * time.Second
)

// Options configures a Handler.
type Options struct {
	Pool    *pgxpool.Pool
	Token   []byte
	Builder *Builder
	Log     *slog.Logger
	// ClaimWait overrides the maximum claim long-poll (default 25 s); for tests.
	ClaimWait time.Duration
}

// Handler serves the internal worker API.
type Handler struct {
	o     Options
	q     *dbgen.Queries
	mu    sync.Mutex
	masks map[uuid.UUID]*runmask.Masker
}

// New builds a Handler. It returns nil when no token is configured: the API
// is then not mounted at all.
func New(o Options) *Handler {
	if len(o.Token) == 0 {
		return nil
	}
	if o.ClaimWait <= 0 {
		o.ClaimWait = runproto.MaxClaimWait * time.Second
	}
	return &Handler{o: o, q: dbgen.New(o.Pool), masks: map[uuid.UUID]*runmask.Masker{}}
}

// Routes returns the router to mount at runproto.BasePath.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(h.bearer)
	r.Post("/claim", h.claim)
	r.Get("/runs/{id}/bundle", h.bundle)
	r.Post("/runs/{id}/events", h.events)
	r.Post("/runs/{id}/heartbeat", h.heartbeat)
	r.Post("/runs/{id}/finish", h.finish)
	return r
}

// bearer accepts only the configured worker token (constant-time compare).
func (h *Handler) bearer(next http.Handler) http.Handler {
	want := sha256.Sum256(h.o.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(tok))
		if !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, req)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) internalErr(w http.ResponseWriter, what string, err error) {
	h.o.Log.Error("worker api: "+what, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func (h *Handler) claim(w http.ResponseWriter, req *http.Request) {
	var in runproto.ClaimRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&in); err != nil || in.WorkerID == "" || len(in.WorkerID) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "worker_id required"})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), h.o.ClaimWait)
	defer cancel()
	for {
		run, err := db.ClaimRun(ctx, h.o.Pool, in.WorkerID)
		switch {
		case err == nil:
			h.o.Log.Info("run claimed", "run", run.ID, "worker", in.WorkerID)
			writeJSON(w, http.StatusOK, runproto.ClaimResponse{
				RunID: run.ID.String(), LeaseID: uuid.UUID(run.LeaseID.Bytes).String(), TemplateName: run.TemplateName,
			})
			return
		case errors.Is(err, db.ErrNoRun):
		case ctx.Err() != nil:
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			h.internalErr(w, "claim", err)
			return
		}
		select {
		case <-ctx.Done():
			w.WriteHeader(http.StatusNoContent)
			return
		case <-time.After(pollInterval):
		}
	}
}

// lease loads the running run of the request and checks its lease.
func (h *Handler) lease(w http.ResponseWriter, req *http.Request) (dbgen.Run, bool) {
	id, err := uuid.Parse(chi.URLParam(req, "id"))
	lease, err2 := uuid.Parse(req.Header.Get(runproto.LeaseHeader))
	if err != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run or lease id"})
		return dbgen.Run{}, false
	}
	run, err := h.q.GetRunForLease(req.Context(), dbgen.GetRunForLeaseParams{ID: id, LeaseID: pgtype.UUID{Bytes: lease, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "lease lost", "code": "lease_lost"})
		return dbgen.Run{}, false
	}
	if err != nil {
		h.internalErr(w, "load run", err)
		return dbgen.Run{}, false
	}
	return run, true
}

func (h *Handler) bundle(w http.ResponseWriter, req *http.Request) {
	run, ok := h.lease(w, req)
	if !ok {
		return
	}
	if _, err := h.q.MarkRunBundleServed(req.Context(), dbgen.MarkRunBundleServedParams{ID: run.ID, LeaseID: run.LeaseID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "bundle already served for this lease", "code": "bundle_served"})
			return
		}
		h.internalErr(w, "mark bundle served", err)
		return
	}
	// Build into memory first: a failure then still yields a clean error
	// response instead of a truncated archive.
	var buf bytes.Buffer
	mask, err := h.o.Builder.Build(req.Context(), run, &buf)
	if err != nil {
		if errors.Is(err, ErrBundle) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "code": "bundle_invalid"})
			return
		}
		h.internalErr(w, "build bundle", err)
		return
	}
	h.setMasker(run.ID, mask)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(buf.Bytes())
}

func (h *Handler) setMasker(id uuid.UUID, values []string) *runmask.Masker {
	m := runmask.New(values)
	h.mu.Lock()
	h.masks[id] = m
	h.mu.Unlock()
	return m
}

func (h *Handler) forget(id uuid.UUID) {
	h.mu.Lock()
	delete(h.masks, id)
	h.mu.Unlock()
}

// masker returns the masker of the run, rebuilding it after an app restart.
func (h *Handler) masker(ctx context.Context, run dbgen.Run) (*runmask.Masker, error) {
	h.mu.Lock()
	m, ok := h.masks[run.ID]
	h.mu.Unlock()
	if ok {
		return m, nil
	}
	values, err := h.o.Builder.MaskValues(ctx, run)
	if err != nil {
		return nil, err
	}
	return h.setMasker(run.ID, values), nil
}

// Channel returns the NOTIFY channel of the run (SSE fan-out, ADR-0008).
func Channel(run uuid.UUID) string { return "acp_run_" + strings.ReplaceAll(run.String(), "-", "") }

func (h *Handler) events(w http.ResponseWriter, req *http.Request) {
	run, ok := h.lease(w, req)
	if !ok {
		return
	}
	var in runproto.EventsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxEventsBody))
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	ctx := req.Context()
	var last int64
	if len(in.Chunks) > 0 {
		m, err := h.masker(ctx, run)
		if err != nil {
			h.internalErr(w, "build masker", err)
			return
		}
		for _, c := range in.Chunks {
			if c.Seq < 1 || len(c.Data) > maxChunkBytes {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid chunk"})
				return
			}
			data := m.Mask(strings.ToValidUTF8(c.Data, "�"))
			if _, err := h.q.AppendRunLogChunk(ctx, dbgen.AppendRunLogChunkParams{RunID: run.ID, Seq: c.Seq, Data: data}); err != nil {
				h.internalErr(w, "append chunk", err)
				return
			}
			last = max(last, c.Seq)
		}
	}
	for _, raw := range in.Events {
		if err := h.applyEvent(ctx, run.ID, raw); err != nil {
			h.internalErr(w, "apply event", err)
			return
		}
	}
	if last > 0 {
		h.notify(ctx, run.ID, "log:"+strconv.FormatInt(last, 10))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) notify(ctx context.Context, id uuid.UUID, payload string) {
	if err := h.q.NotifyRun(ctx, dbgen.NotifyRunParams{Channel: Channel(id), Payload: payload}); err != nil {
		h.o.Log.Warn("notify run", "run", id, "error", err)
	}
}

// clamp32 converts a counter from a callback event to int32.
func clamp32(n int) int32 { return int32(max(math.MinInt32, min(n, math.MaxInt32))) } //nolint:gosec // value is clamped

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// applyEvent turns a callback event into per-host results. Malformed events
// are ignored: they must not break the run.
func (h *Handler) applyEvent(ctx context.Context, run uuid.UUID, raw json.RawMessage) error {
	var e runproto.Event
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	switch e.Event {
	case "runner_ok", "runner_changed", "runner_failed", "runner_unreachable":
		if e.Host == "" {
			return nil
		}
		kind := strings.TrimPrefix(e.Event, "runner_")
		if kind == "failed" && e.Ignored {
			kind = "ok"
		}
		return h.q.MarkRunHostEvent(ctx, dbgen.MarkRunHostEventParams{
			RunID: run, Host: clip(e.Host, maxHostLen), Kind: kind, Task: clip(e.Task, maxTaskLen),
		})
	case "stats":
		for host, s := range e.Hosts {
			err := h.q.SetRunHostStats(ctx, dbgen.SetRunHostStatsParams{
				RunID: run, Host: clip(host, maxHostLen), Ok: clamp32(s["ok"]), Changed: clamp32(s["changed"]),
				Failed: clamp32(s["failures"]), Unreachable: clamp32(s["unreachable"]), Skipped: clamp32(s["skipped"]),
				Rescued: clamp32(s["rescued"]), Ignored: clamp32(s["ignored"]),
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *Handler) heartbeat(w http.ResponseWriter, req *http.Request) {
	run, ok := h.lease(w, req)
	if !ok {
		return
	}
	cancelAt, err := h.q.HeartbeatRun(req.Context(), dbgen.HeartbeatRunParams{ID: run.ID, LeaseID: run.LeaseID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "lease lost", "code": "lease_lost"})
		return
	}
	if err != nil {
		h.internalErr(w, "heartbeat", err)
		return
	}
	writeJSON(w, http.StatusOK, runproto.HeartbeatResponse{Cancel: cancelAt.Valid})
}

func (h *Handler) finish(w http.ResponseWriter, req *http.Request) {
	run, ok := h.lease(w, req)
	if !ok {
		return
	}
	var in runproto.FinishRequest
	if err := json.NewDecoder(io.LimitReader(req.Body, 4<<10)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	status := runs.StatusFailed
	switch {
	case in.Cancelled:
		status = runs.StatusCancelled
	case in.ExitCode == 0:
		status = runs.StatusSucceeded
	}
	r, err := h.q.FinishRun(req.Context(), dbgen.FinishRunParams{
		ID: run.ID, LeaseID: run.LeaseID, Status: status, ExitCode: pgtype.Int4{Int32: clamp32(in.ExitCode), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "lease lost", "code": "lease_lost"})
		return
	}
	if err != nil {
		h.internalErr(w, "finish", err)
		return
	}
	h.forget(run.ID)
	h.notify(req.Context(), run.ID, "status:"+r.Status)
	h.o.Log.Info("run finished", "run", run.ID, "status", r.Status, "exit_code", in.ExitCode)
	writeJSON(w, http.StatusOK, map[string]string{"status": r.Status})
}

// Maintain marks runs without heartbeat as lost and requests cancellation of
// running runs whose template was deleted. One pass.
func (h *Handler) Maintain(ctx context.Context) error {
	lost, err := h.q.MarkLostRuns(ctx, LostAfter.Seconds())
	if err != nil {
		return err
	}
	for _, r := range lost {
		h.forget(r.ID)
		h.notify(ctx, r.ID, "status:lost")
		h.o.Log.Warn("run lost: no heartbeat", "run", r.ID, "worker", r.WorkerID)
	}
	orphans, err := h.q.RequestCancelOrphanRuns(ctx)
	if err != nil {
		return err
	}
	for _, id := range orphans {
		h.o.Log.Info("run cancel requested: template deleted", "run", id)
	}
	return nil
}

// RunMaintenance calls Maintain every MaintenanceInterval until ctx is done.
func (h *Handler) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(MaintenanceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := h.Maintain(ctx); err != nil && ctx.Err() == nil {
				h.o.Log.Error("run maintenance", "error", err)
			}
		}
	}
}

// TokenFromFile reads the worker token file; surrounding whitespace is trimmed.
func TokenFromFile(path string, read func(string) ([]byte, error)) ([]byte, error) {
	b, err := read(path)
	if err != nil {
		return nil, err
	}
	t := []byte(strings.TrimSpace(string(b)))
	if len(t) < 16 {
		return nil, errors.New("worker token is shorter than 16 characters")
	}
	return t, nil
}
