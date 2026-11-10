package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/runstream"
)

type fakeHub struct {
	mu   sync.Mutex
	subs map[string][]*runstream.Sub
}

func (f *fakeHub) Subscribe(_ context.Context, ch string) (*runstream.Sub, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &runstream.Sub{C: make(chan struct{}, 1)}
	if f.subs == nil {
		f.subs = map[string][]*runstream.Sub{}
	}
	f.subs[ch] = append(f.subs[ch], s)
	return s, nil
}

func (f *fakeHub) wake(ch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subs[ch] {
		select {
		case s.C <- struct{}{}:
		default:
		}
	}
}

type fakeChunks struct {
	mu   sync.Mutex
	rows []dbgen.RunLogChunk
}

func (f *fakeChunks) add(seq int64, data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, dbgen.RunLogChunk{Seq: seq, Data: data})
}

func (f *fakeChunks) ListRunLogChunks(_ context.Context, a dbgen.ListRunLogChunksParams) ([]dbgen.RunLogChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []dbgen.RunLogChunk
	for _, r := range f.rows {
		if r.Seq > a.Seq && len(out) < int(a.Limit) {
			out = append(out, r)
		}
	}
	return out, nil
}

// streamEnv serves the runs API with the stream enabled for one fixed user.
type streamEnv struct {
	h      http.Handler
	fr     *fakeRuns
	hub    *fakeHub
	chunks *fakeChunks
}

func newStreamEnv(role string, finished bool, scopes ...dbgen.MembershipScope) *streamEnv {
	fr := newFakeRuns()
	if finished {
		now := time.Now()
		fr.run.Status, fr.run.FinishedAt = runs.StatusSucceeded, &now
	}
	e := &streamEnv{fr: fr, hub: &fakeHub{}, chunks: &fakeChunks{}}
	for i := range scopes { // an empty RefID means "the inventory of the run"
		if scopes[i].RefID == "" {
			scopes[i].RefID = fr.run.Params.InventoryID.String()
		}
	}
	uid := uuid.New()
	st := fakeAPIStore{ws: dbgen.Workspace{ID: uuid.New(), Slug: "default", Name: "Default"}, user: uid, role: role, scopes: scopes}
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{User: dbgen.User{ID: uid}})))
		})
	}
	e.h = Router(quiet, fakePing{}, DefaultStatic(), WithPolicy(policy.New(st, nil, authn, quiet), st), WithRuns(fr), WithRunStream(e.hub, e.chunks))
	return e
}

func (e *streamEnv) path(suffix string) string {
	return "/api/v1/workspaces/default/runs/" + e.fr.run.ID.String() + suffix
}

func (e *streamEnv) get(path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestRunLogSSEReplayAndEnd(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, true)
	for i := int64(1); i <= 4; i++ {
		e.chunks.add(i, "line "+strconv.FormatInt(i, 10)+"\r\n\x1b[32mok\x1b[0m\n")
	}
	rec := e.get(e.path("/log"), nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{"id: 1\nevent: log\n", "id: 4\nevent: log\n", "event: status\n", "event: end\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %q", want, body)
		}
	}
	// CR, LF and ANSI escapes survive inside the single-line JSON payload.
	if !strings.Contains(body, `line 1\r\n\u001b[32mok`) {
		t.Errorf("payload not JSON-escaped: %q", body)
	}
	if strings.Index(body, "event: end") < strings.Index(body, "id: 4") {
		t.Error("end must come after the last chunk")
	}

	// Resume: header and query parameter skip delivered chunks.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"header": e.get(e.path("/log"), map[string]string{"Last-Event-ID": "2"}),
		"query":  e.get(e.path("/log?after=2"), nil),
	} {
		b := rec.Body.String()
		if strings.Contains(b, "id: 1\n") || strings.Contains(b, "id: 2\n") || !strings.Contains(b, "id: 3\n") || !strings.Contains(b, "id: 4\n") {
			t.Errorf("%s resume: %q", name, b)
		}
	}
	if rec := e.get(e.path("/log?after=x"), nil); rec.Code != 400 {
		t.Errorf("bad after: %d", rec.Code)
	}
}

func TestRunLogSSELive(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, false)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+e.path("/log"), nil)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed via defer or bodies
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(prefix string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %q", prefix)
				}
				if strings.HasPrefix(l, prefix) {
					return
				}
			case <-timeout:
				t.Fatalf("timeout waiting for %q", prefix)
			}
		}
	}
	waitFor("event: status") // initial snapshot of the queued run
	e.chunks.add(1, "hello")
	e.hub.wake(runstream.RunChannel(e.fr.run.ID))
	waitFor("id: 1")
	waitFor(`data: "hello"`)
	// Finish the run: status change and end follow the wake-up.
	now := time.Now()
	e.fr.run.Status, e.fr.run.FinishedAt = runs.StatusFailed, &now
	e.chunks.add(2, "bye")
	e.hub.wake(runstream.RunChannel(e.fr.run.ID))
	waitFor(`data: "bye"`)
	waitFor("event: end")
}

func TestRunLogAuthz(t *testing.T) {
	t.Run("viewer may read", func(t *testing.T) {
		e := newStreamEnv(policy.RoleViewer, true)
		if rec := e.get(e.path("/log"), nil); rec.Code != 200 {
			t.Errorf("log: %d", rec.Code)
		}
		if rec := e.get(e.path("/log.txt"), nil); rec.Code != 200 {
			t.Errorf("log.txt: %d", rec.Code)
		}
	})
	t.Run("bad run id", func(t *testing.T) {
		e := newStreamEnv(policy.RoleViewer, true)
		if rec := e.get("/api/v1/workspaces/default/runs/nope/log", nil); rec.Code != 404 {
			t.Errorf("log: %d", rec.Code)
		}
	})
	t.Run("in-scope member", func(t *testing.T) {
		e := newStreamEnv(policy.RoleOperator, true, dbgen.MembershipScope{Kind: policy.KindInventory})
		if rec := e.get(e.path("/log.txt"), nil); rec.Code != 200 {
			t.Errorf("in scope: %d", rec.Code)
		}
	})
	t.Run("out-of-scope member sees 404 and no stream", func(t *testing.T) {
		out := dbgen.MembershipScope{Kind: policy.KindInventory, RefID: uuid.NewString()}
		e := newStreamEnv(policy.RoleOperator, true, out)
		for _, s := range []string{"/log", "/log.txt"} {
			if rec := e.get(e.path(s), nil); rec.Code != 404 {
				t.Errorf("%s: %d", s, rec.Code)
			}
		}
		if len(e.hub.subs) != 0 {
			t.Error("out-of-scope request must not subscribe")
		}
	})
}

func TestRunLogDownload(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, true)
	e.chunks.add(1, "a\x1b[1m")
	e.chunks.add(2, "b\n")
	rec := e.get(e.path("/log.txt"), nil)
	if rec.Body.String() != "a\x1b[1mb\n" {
		t.Errorf("body %q", rec.Body.String())
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, e.fr.run.ID.String()) {
		t.Errorf("disposition %q", cd)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("type %q", ct)
	}
}

func TestStreamLimiter(t *testing.T) {
	l := &streamLimiter{max: 2, n: map[uuid.UUID]int{}}
	u, other := uuid.New(), uuid.New()
	r1, ok1 := l.acquire(u)
	_, ok2 := l.acquire(u)
	_, ok3 := l.acquire(u)
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("acquire: %v %v %v", ok1, ok2, ok3)
	}
	if _, ok := l.acquire(other); !ok {
		t.Error("limit must be per user")
	}
	r1()
	r1() // idempotent
	if _, ok := l.acquire(u); !ok {
		t.Error("release must free a slot")
	}
}

func TestRunLogStreamLimitHTTP(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, false)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bodies []*http.Response
	defer func() {
		for _, b := range bodies {
			_ = b.Body.Close()
		}
	}()
	for i := 0; i < maxStreamsPerUser; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+e.path("/log"), nil)
		resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed via defer or bodies
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("stream %d: %v", i, err)
		}
		bodies = append(bodies, resp) //nolint:bodyclose // closed in the deferred loop
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+e.path("/log"), nil)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed via defer or bodies
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11th stream: %d", resp.StatusCode)
	}
}

func TestRunListSSE(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, false)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/workspaces/default/runs/events", nil)
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed via defer or bodies
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	lines := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	// Wait for the stream to be set up, then change the status and wake it.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case l := <-lines:
			if l == ": connected" {
				e.fr.run.Status = runs.StatusRunning
				e.hub.wake(runstream.ListChannel)
				for {
					select {
					case l := <-lines:
						if strings.HasPrefix(l, "event: run") {
							return
						}
					case <-deadline:
						t.Fatal("no run event")
					}
				}
			}
		case <-deadline:
			t.Fatal("not connected")
		}
	}
}

// TestRunLogSSENotCompressed proves that a client advertising gzip still gets an
// uncompressed stream and receives each event as soon as it is written.
func TestRunLogSSENotCompressed(t *testing.T) {
	e := newStreamEnv(policy.RoleViewer, false)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+e.path("/log"), nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	resp, err := http.DefaultTransport.RoundTrip(req) //nolint:bodyclose // closed below
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("Content-Encoding = %q, want none", ce)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	got := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: status") {
				got <- sc.Text()
				return
			}
		}
	}()
	select {
	case <-got: // first event arrived while the stream is still open
	case <-time.After(3 * time.Second):
		t.Fatal("first event was not flushed")
	}
}
