package workerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

const token = "test-worker-token-0123456789"

type apiEnv struct {
	h   *Handler
	srv *httptest.Server
	q   *dbgen.Queries
	ws  dbgen.Workspace
	ids fixtureIDs
	tpl dbgen.Template
	prj dbgen.Project
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	q := dbgen.New(pool)
	sfx := uuid.NewString()[:8]
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "wa-" + sfx, Name: "WA " + sfx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	prj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/x.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := q.CreateInventory(ctx, dbgen.CreateInventoryParams{WorkspaceID: ws.ID, Name: "i", Slug: "i", Mode: "db", SourceFormat: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := q.CreateTemplate(ctx, dbgen.CreateTemplateParams{
		WorkspaceID: ws.ID, ProjectID: prj.ID, InventoryID: inv.ID, Name: "t", Slug: "t", Playbook: "site.yml",
		ExtraVars: []byte(`{}`), EnvVars: []byte(`{}`), MaxConcurrent: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := fixtureIDs{uuid.New(), uuid.New(), uuid.New()}
	b, _ := testBuilder(t, ids)
	h := New(Options{Pool: pool, Token: []byte(token), Builder: b, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), ClaimWait: 1500 * time.Millisecond})
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return &apiEnv{h: h, srv: srv, q: q, ws: ws, ids: ids, tpl: tpl, prj: prj}
}

// enqueue inserts a run whose params use the fake credential ids.
func (e *apiEnv) enqueue(t *testing.T) dbgen.Run {
	t.Helper()
	tmpl := testRun(t, e.ids, nil)
	// Re-seal for the real workspace id (AAD binds the workspace).
	p, _ := runs.DecodeParams(tmpl.Params)
	p.SecretVarsEnc = nil
	p.ExtraVars["pw"] = testSurveyPw
	p.ProjectID = e.prj.ID
	if err := p.SealSecretVars(testProvider(t), e.ws.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	r, err := e.q.EnqueueRun(context.Background(), dbgen.EnqueueRunParams{
		WorkspaceID: e.ws.ID, TemplateID: pgtype.UUID{Bytes: e.tpl.ID, Valid: true}, TemplateName: e.tpl.Name,
		ProjectID: pgtype.UUID{Bytes: e.prj.ID, Valid: true}, ProjectRevision: p.ProjectRevision,
		Trigger: "manual", Overrides: []byte(`{}`), Params: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *apiEnv) do(t *testing.T, method, path, lease string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	if lease != "" {
		req.Header.Set(runproto.LeaseHeader, lease)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestAuthRequired(t *testing.T) {
	e := newAPIEnv(t)
	for _, auth := range []string{"", "Bearer wrong", "Basic " + token, token} {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", e.srv.URL+"/claim", strings.NewReader(`{"worker_id":"w"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d", auth, resp.StatusCode)
		}
	}
}

func TestNewWithoutTokenIsNil(t *testing.T) {
	if New(Options{}) != nil {
		t.Error("handler without token must be nil")
	}
}

func TestRunFlow(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()

	// Empty queue: long poll ends with 204.
	if code, _ := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w1"}); code != http.StatusNoContent {
		t.Fatalf("empty claim: %d", code)
	}
	queued := e.enqueue(t)
	code, body := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w1"})
	var cl runproto.ClaimResponse
	if code != 200 || json.Unmarshal(body, &cl) != nil || cl.RunID != queued.ID.String() || cl.LeaseID == "" {
		t.Fatalf("claim: %d %s", code, body)
	}
	base := "/runs/" + cl.RunID

	// Wrong lease is rejected.
	if code, _ := e.do(t, "POST", base+"/heartbeat", uuid.NewString(), nil); code != http.StatusConflict {
		t.Errorf("wrong lease: %d", code)
	}
	// Bundle once per lease.
	code, body = e.do(t, "GET", base+"/bundle", cl.LeaseID, nil)
	if code != 200 {
		t.Fatalf("bundle: %d %s", code, body)
	}
	if files := readTarGz(t, body); string(files[runproto.FileSSHKey]) != testKey {
		t.Error("bundle lacks ssh key")
	}
	if code, _ := e.do(t, "GET", base+"/bundle", cl.LeaseID, nil); code != http.StatusConflict {
		t.Errorf("second bundle: %d", code)
	}

	// Events: chunks are masked, duplicates ignored, callback events recorded.
	ev := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	req := runproto.EventsRequest{
		Chunks: []runproto.Chunk{{Seq: 1, Data: "PLAY start\n"}, {Seq: 2, Data: "leak " + testBecome + " and " + testSurveyPw + " " + testVault + "\n"}},
		Events: []json.RawMessage{
			ev(map[string]any{"event": "runner_changed", "host": "web1.example.test", "task": "install pkg"}),
			ev(map[string]any{"event": "runner_changed", "host": "web1.example.test", "task": "install pkg"}),
			ev(map[string]any{"event": "runner_failed", "host": "web2.example.test", "task": "x", "ignored": true}),
			ev(map[string]any{"event": "runner_unreachable", "host": "db1.example.test", "task": "ping"}),
			ev(map[string]any{"event": "stats", "hosts": map[string]any{
				"web1.example.test": map[string]int{"ok": 3, "changed": 1, "failures": 0, "unreachable": 0, "skipped": 0, "rescued": 0, "ignored": 0},
				"db1.example.test":  map[string]int{"ok": 0, "changed": 0, "failures": 0, "unreachable": 1},
			}}),
			json.RawMessage(`"not an object"`),
		},
	}
	// Live counters grow with every event; a retried batch is not counted twice.
	live := runproto.EventsRequest{
		Chunks: []runproto.Chunk{{Seq: 100, Data: "ok: [live1.example.test]\n"}},
		Events: []json.RawMessage{
			ev(map[string]any{"event": "runner_ok", "host": "live1.example.test", "task": "a"}),
			ev(map[string]any{"event": "runner_changed", "host": "live1.example.test", "task": "b"}),
			ev(map[string]any{"event": "runner_skipped", "host": "live1.example.test", "task": "c"}),
		},
	}
	for i := 0; i < 2; i++ {
		if code, b := e.do(t, "POST", base+"/events", cl.LeaseID, live); code != http.StatusNoContent {
			t.Fatalf("live events: %d %s", code, b)
		}
	}
	liveRows, _ := e.q.ListRunHostResults(ctx, queued.ID)
	for _, h := range liveRows {
		if h.Host == "live1.example.test" && (h.Ok != 2 || h.Changed != 1 || h.Skipped != 1) {
			t.Errorf("live counters: %+v", h)
		}
	}
	for i := 0; i < 2; i++ { // retry of the same batch must be idempotent
		if code, b := e.do(t, "POST", base+"/events", cl.LeaseID, req); code != http.StatusNoContent {
			t.Fatalf("events: %d %s", code, b)
		}
	}
	runID := queued.ID
	chunks, err := e.q.ListRunLogChunks(ctx, dbgen.ListRunLogChunksParams{RunID: runID, Seq: 0, Limit: 10})
	if err != nil || len(chunks) != 3 {
		t.Fatalf("chunks: %v %d", err, len(chunks))
	}
	for _, s := range []string{testBecome, testSurveyPw, testVault} {
		if strings.Contains(chunks[1].Data, s) {
			t.Errorf("secret persisted in log: %q", chunks[1].Data)
		}
	}
	if chunks[1].Data != "leak *** and *** ***\n" {
		t.Errorf("masked chunk = %q", chunks[1].Data)
	}
	hr, _ := e.q.ListRunHostResults(ctx, runID)
	got := map[string]dbgen.RunHostResult{}
	for _, h := range hr {
		got[h.Host] = h
	}
	if w := got["web1.example.test"]; w.Ok != 3 || w.Changed != 1 || string(w.ChangedTasks) != `["install pkg"]` {
		t.Errorf("web1: %+v %s", w, w.ChangedTasks)
	}
	if w := got["web2.example.test"]; w.Failed != 0 {
		t.Errorf("ignored failure counted: %+v", w)
	}
	if got["db1.example.test"].Unreachable != 1 {
		t.Errorf("db1: %+v", got["db1.example.test"])
	}

	// Cancel is delivered with the heartbeat.
	_, hb := e.do(t, "POST", base+"/heartbeat", cl.LeaseID, nil)
	if !strings.Contains(string(hb), `"cancel":false`) {
		t.Errorf("heartbeat before cancel: %s", hb)
	}
	if _, err := e.q.RequestRunCancel(ctx, dbgen.RequestRunCancelParams{ID: runID, WorkspaceID: e.ws.ID}); err != nil {
		t.Fatal(err)
	}
	_, hb = e.do(t, "POST", base+"/heartbeat", cl.LeaseID, nil)
	if !strings.Contains(string(hb), `"cancel":true`) {
		t.Errorf("heartbeat after cancel: %s", hb)
	}

	// Finish: cancelled status, secrets wiped, lease then gone.
	code, b := e.do(t, "POST", base+"/finish", cl.LeaseID, runproto.FinishRequest{ExitCode: -1, Cancelled: true})
	if code != 200 || !strings.Contains(string(b), runs.StatusCancelled) {
		t.Fatalf("finish: %d %s", code, b)
	}
	r, _ := e.q.GetRunByID(ctx, runID)
	if r.Status != runs.StatusCancelled || strings.Contains(string(r.Params), "secret_vars_enc") || r.HostsUnreachable != 1 {
		t.Errorf("run after finish: %s %s hosts_unreachable=%d", r.Status, r.Params, r.HostsUnreachable)
	}
	if code, _ := e.do(t, "POST", base+"/finish", cl.LeaseID, runproto.FinishRequest{}); code != http.StatusConflict {
		t.Errorf("second finish: %d", code)
	}
}

func TestFinishStatusFromExitCode(t *testing.T) {
	e := newAPIEnv(t)
	for _, tc := range []struct {
		exit int
		want string
	}{{0, runs.StatusSucceeded}, {2, runs.StatusFailed}} {
		e.enqueue(t)
		_, body := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w"})
		var cl runproto.ClaimResponse
		_ = json.Unmarshal(body, &cl)
		code, b := e.do(t, "POST", "/runs/"+cl.RunID+"/finish", cl.LeaseID, runproto.FinishRequest{ExitCode: tc.exit})
		if code != 200 || !strings.Contains(string(b), tc.want) {
			t.Errorf("exit %d: %d %s", tc.exit, code, b)
		}
	}
}

func TestMaintainLostAndOrphans(t *testing.T) {
	e := newAPIEnv(t)
	ctx := context.Background()
	lostRun := e.enqueue(t)
	_, body := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w"})
	var cl runproto.ClaimResponse
	_ = json.Unmarshal(body, &cl)
	if cl.RunID != lostRun.ID.String() {
		t.Fatalf("claimed %s", cl.RunID)
	}
	if err := e.h.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.q.GetRunByID(ctx, lostRun.ID); r.Status != runs.StatusRunning {
		t.Fatalf("fresh heartbeat must not be lost: %s", r.Status)
	}
	// Template deleted while running: cancel is requested.
	if _, err := e.h.o.Pool.Exec(ctx, "DELETE FROM templates WHERE id = $1", e.tpl.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, hb := e.do(t, "POST", "/runs/"+cl.RunID+"/heartbeat", cl.LeaseID, nil); !strings.Contains(string(hb), `"cancel":true`) {
		t.Errorf("orphan heartbeat: %s", hb)
	}
	// Silent for longer than the threshold: lost, secrets wiped.
	if _, err := e.h.o.Pool.Exec(ctx, "UPDATE runs SET last_heartbeat_at = now() - interval '2 minutes' WHERE id = $1", lostRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.h.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := e.q.GetRunByID(ctx, lostRun.ID)
	if r.Status != runs.StatusLost || strings.Contains(string(r.Params), "secret_vars_enc") {
		t.Errorf("lost run: %s %s", r.Status, r.Params)
	}
	if code, _ := e.do(t, "POST", "/runs/"+cl.RunID+"/heartbeat", cl.LeaseID, nil); code != http.StatusConflict {
		t.Errorf("heartbeat of lost run: %d", code)
	}
}
