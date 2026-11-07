package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/runstream"
)

// RunHub hands out wake-up subscriptions per NOTIFY channel;
// *runstream.Hub implements it.
type RunHub interface {
	Subscribe(ctx context.Context, channel string) (*runstream.Sub, error)
}

// RunChunks reads persisted log chunks; *dbgen.Queries implements it.
type RunChunks interface {
	ListRunLogChunks(ctx context.Context, arg dbgen.ListRunLogChunksParams) ([]dbgen.RunLogChunk, error)
}

// Stream tuning (ADR-0008).
const (
	sseHeartbeat      = 15 * time.Second
	ssePollInterval   = 5 * time.Second // safety net for missed notifications
	chunkPage         = 500
	maxStreamsPerUser = 10
)

type runStream struct {
	hub    RunHub
	chunks RunChunks
	lim    *streamLimiter
}

// WithRunStream enables the SSE live log, the run list stream and the log
// download. It requires WithRuns and WithPolicy.
func WithRunStream(h RunHub, c RunChunks) Option {
	return func(o *routerOpts) {
		o.stream = &runStream{hub: h, chunks: c, lim: &streamLimiter{max: maxStreamsPerUser, n: map[uuid.UUID]int{}}}
	}
}

// streamLimiter caps concurrent SSE streams per user.
type streamLimiter struct {
	mu  sync.Mutex
	max int
	n   map[uuid.UUID]int
}

func (l *streamLimiter) acquire(u uuid.UUID) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[u] >= l.max {
		return nil, false
	}
	l.n[u]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.n[u]--; l.n[u] <= 0 {
				delete(l.n, u)
			}
		})
	}, true
}

// runHelpers are the shared closures of runRoutes.
type runHelpers struct {
	fail     func(w http.ResponseWriter, what string, err error)
	notFound func(w http.ResponseWriter)
	decision func(w http.ResponseWriter, req *http.Request) (policy.Decision, bool)
	target   func(w http.ResponseWriter, req *http.Request) (policy.Decision, uuid.UUID, bool)
	visible  func(req *http.Request, d policy.Decision) func(runs.Run) bool
}

// sse writes server-sent events and flushes after each one.
type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s *sse) send(event, id string, data []byte) error {
	var b strings.Builder
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + event + "\n")
	b.WriteString("data: ")
	b.Write(data) // single-line JSON
	b.WriteString("\n\n")
	return s.flush(b.String())
}

func (s *sse) comment(text string) error { return s.flush(": " + text + "\n\n") }

func (s *sse) flush(str string) error {
	if _, err := s.w.Write([]byte(str)); err != nil {
		return err
	}
	return s.rc.Flush()
}

// statusEvent is the payload of the `status` event: run state and per-host summary.
type statusEvent struct {
	Status            string            `json:"status"`
	StartedAt         *time.Time        `json:"started_at"`
	FinishedAt        *time.Time        `json:"finished_at"`
	CancelRequestedAt *time.Time        `json:"cancel_requested_at"`
	ExitCode          *int              `json:"exit_code"`
	HostsOK           int               `json:"hosts_ok"`
	HostsChanged      int               `json:"hosts_changed"`
	HostsFailed       int               `json:"hosts_failed"`
	HostsUnreachable  int               `json:"hosts_unreachable"`
	HostResults       []runs.HostResult `json:"host_results"`
}

func toStatusEvent(d runs.RunDetail) statusEvent {
	return statusEvent{Status: d.Status, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt, CancelRequestedAt: d.CancelRequestedAt,
		ExitCode: d.ExitCode, HostsOK: d.HostsOK, HostsChanged: d.HostsChanged, HostsFailed: d.HostsFailed,
		HostsUnreachable: d.HostsUnreachable, HostResults: d.HostResults}
}

// streamRoutes registers the SSE log, the run list stream and the log download.
func streamRoutes(r chi.Router, az *policy.Authorizer, svc RunsAPI, st *runStream, h runHelpers, log *slog.Logger) {
	const rns = "/api/v1/workspaces/{workspace}/runs"

	// scoped loads the run and applies the same scope check as GET runs/{id}.
	scoped := func(w http.ResponseWriter, req *http.Request) (uuid.UUID, uuid.UUID, bool) {
		d, id, ok := h.target(w, req)
		if !ok {
			return uuid.Nil, uuid.Nil, false
		}
		run, err := svc.GetRun(req.Context(), d.Workspace.ID, id)
		if err != nil {
			h.fail(w, "get run", err)
			return uuid.Nil, uuid.Nil, false
		}
		if vis := h.visible(req, d); vis != nil && !vis(run.Run) {
			h.notFound(w)
			return uuid.Nil, uuid.Nil, false
		}
		return d.Workspace.ID, id, true
	}
	// open prepares an SSE response after the per-user limit check.
	open := func(w http.ResponseWriter, req *http.Request) (func(), bool) {
		release, ok := st.lim.acquire(auth.FromContext(req.Context()).User.ID)
		if !ok {
			w.Header().Set("Retry-After", "5")
			policy.WriteJSON(w, http.StatusTooManyRequests, apiError{Error: "too many open streams", Code: "too_many_streams"})
			return nil, false
		}
		return release, true
	}
	begin := func(w http.ResponseWriter) *sse {
		rc := http.NewResponseController(w)
		// Streams outlive the server read/write timeouts; only this handler lifts them
		// (an expired read deadline would also cancel the request context).
		_ = rc.SetWriteDeadline(time.Time{})
		_ = rc.SetReadDeadline(time.Time{})
		hd := w.Header()
		hd.Set("Content-Type", "text/event-stream")
		hd.Set("Cache-Control", "no-cache")
		hd.Set("X-Accel-Buffering", "no") // reverse proxies must not buffer SSE
		w.WriteHeader(http.StatusOK)
		s := &sse{w: w, rc: rc}
		_ = s.comment("connected")
		return s
	}

	az.Handle(r, http.MethodGet, rns+"/events", policy.RunRead, func(w http.ResponseWriter, req *http.Request) {
		d, ok := h.decision(w, req)
		if !ok {
			return
		}
		release, ok := open(w, req)
		if !ok {
			return
		}
		defer release()
		ctx := req.Context()
		sub, err := st.hub.Subscribe(ctx, runstream.ListChannel)
		if err != nil {
			log.Error("subscribe run list", "error", err)
			policy.WriteJSON(w, http.StatusServiceUnavailable, apiError{Error: "stream unavailable"})
			return
		}
		defer sub.Close()
		vis := h.visible(req, d)
		page := func() []runs.Run {
			p, err := svc.ListRuns(ctx, d.Workspace.ID, runs.Filter{Limit: 50, Visible: vis})
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("run list stream", "error", err)
				}
				return nil
			}
			return p.Items
		}
		seen := map[uuid.UUID]string{}
		for _, run := range page() {
			seen[run.ID] = run.Status + "|" + strconv.Itoa(run.HostsFailed)
		}
		s := begin(w)
		hb, poll := time.NewTicker(sseHeartbeat), time.NewTicker(ssePollInterval)
		defer hb.Stop()
		defer poll.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-hb.C:
				if s.comment("ping") != nil {
					return
				}
				continue
			case <-sub.C:
			case <-poll.C:
			}
			for _, run := range page() {
				sig := run.Status + "|" + strconv.Itoa(run.HostsFailed)
				if seen[run.ID] == sig {
					continue
				}
				seen[run.ID] = sig
				b, _ := json.Marshal(maskRun(d, run))
				if s.send("run", "", b) != nil {
					return
				}
			}
		}
	})

	az.Handle(r, http.MethodGet, rns+"/{id}/log", policy.RunRead, func(w http.ResponseWriter, req *http.Request) {
		ws, id, ok := scoped(w, req)
		if !ok {
			return
		}
		after := int64(0)
		if v := req.Header.Get("Last-Event-ID"); v != "" {
			after, _ = strconv.ParseInt(v, 10, 64)
		} else if v := req.URL.Query().Get("after"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				policy.WriteJSON(w, http.StatusBadRequest, apiError{Error: "invalid after", Code: "bad_request"})
				return
			}
			after = n
		}
		after = max(after, 0)
		release, ok := open(w, req)
		if !ok {
			return
		}
		defer release()
		ctx := req.Context()
		// Subscribe before the first read so no chunk written in between is missed.
		sub, err := st.hub.Subscribe(ctx, runstream.RunChannel(id))
		if err != nil {
			log.Error("subscribe run log", "run", id, "error", err)
			policy.WriteJSON(w, http.StatusServiceUnavailable, apiError{Error: "stream unavailable"})
			return
		}
		defer sub.Close()
		s := begin(w)
		hb, poll := time.NewTicker(sseHeartbeat), time.NewTicker(ssePollInterval)
		defer hb.Stop()
		defer poll.Stop()
		var lastStatus []byte
		for {
			// Read the run state first: if it is finished here, every chunk is
			// already stored and the flush below is complete.
			d, err := svc.GetRun(ctx, ws, id)
			if err != nil {
				if ctx.Err() == nil {
					log.Warn("run log stream: load run", "run", id, "error", err)
					_ = s.send("error", "", []byte(`{"error":"run unavailable"}`))
				}
				return
			}
			for {
				rows, err := st.chunks.ListRunLogChunks(ctx, dbgen.ListRunLogChunksParams{RunID: id, Seq: after, Limit: chunkPage})
				if err != nil {
					if ctx.Err() == nil {
						log.Warn("run log stream: read chunks", "run", id, "error", err)
					}
					return
				}
				for _, c := range rows {
					b, _ := json.Marshal(c.Data) // JSON string keeps CR/LF and ANSI escapes intact
					if s.send("log", strconv.FormatInt(c.Seq, 10), b) != nil {
						return
					}
					after = c.Seq
				}
				if len(rows) < chunkPage {
					break
				}
			}
			if b, _ := json.Marshal(toStatusEvent(d)); string(b) != string(lastStatus) {
				lastStatus = b
				if s.send("status", "", b) != nil {
					return
				}
			}
			if d.FinishedAt != nil {
				end, _ := json.Marshal(map[string]any{"status": d.Status, "last_seq": after})
				_ = s.send("end", "", end)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-sub.C:
			case <-poll.C:
			case <-hb.C:
				if s.comment("ping") != nil {
					return
				}
			}
		}
	})

	az.Handle(r, http.MethodGet, rns+"/{id}/log.txt", policy.RunRead, func(w http.ResponseWriter, req *http.Request) {
		_, id, ok := scoped(w, req)
		if !ok {
			return
		}
		hd := w.Header()
		hd.Set("Content-Type", "text/plain; charset=utf-8")
		hd.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "run-"+id.String()+".log"))
		hd.Set("Cache-Control", "no-store")
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		_ = rc.SetReadDeadline(time.Time{})
		after := int64(0)
		for {
			rows, err := st.chunks.ListRunLogChunks(req.Context(), dbgen.ListRunLogChunksParams{RunID: id, Seq: after, Limit: chunkPage})
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Warn("run log download", "run", id, "error", err)
				}
				return // headers are out; the truncated body signals the failure
			}
			for _, c := range rows {
				if _, err := w.Write([]byte(c.Data)); err != nil {
					return
				}
				after = c.Seq
			}
			if len(rows) < chunkPage {
				return
			}
		}
	})
}
