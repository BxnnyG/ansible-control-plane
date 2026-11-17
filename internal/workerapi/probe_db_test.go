package workerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

type probeFixture struct {
	run     dbgen.Run
	srcOK   uuid.UUID
	srcDead uuid.UUID
	tgt1    uuid.UUID
	tgt2    uuid.UUID
}

// probeRun creates two probe sources (one answering, one unreachable), two
// targets and a queued built-in probe run for them.
func (e *apiEnv) probeRun(t *testing.T, builtin string) probeFixture {
	t.Helper()
	ctx := context.Background()
	var f probeFixture
	mk := func(name string) uuid.UUID {
		h, err := e.q.CreateInventoryHost(ctx, dbgen.CreateInventoryHostParams{InventoryID: e.tpl.InventoryID, Name: name, Vars: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		s, err := e.q.CreateReachabilitySource(ctx, dbgen.CreateReachabilitySourceParams{WorkspaceID: e.ws.ID, HostID: h.ID})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	f.srcOK, f.srcDead = mk("src-a"), mk("src-b")
	tgt := func(name string) uuid.UUID {
		r, err := e.q.CreateReachabilityTarget(ctx, dbgen.CreateReachabilityTargetParams{
			WorkspaceID: e.ws.ID, Name: name, Address: "192.0.2.10", CheckType: "icmp", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	f.tgt1, f.tgt2 = tgt("t1"), tgt("t2")
	p := runs.Params{
		Playbook: runs.ProbePlaybook, Builtin: builtin, InventoryID: e.tpl.InventoryID,
		Limit: "src-a,src-b", Hosts: []string{"src-a", "src-b"}, EnvVars: map[string]any{},
	}
	raw, _ := json.Marshal(p)
	r, err := e.q.EnqueueBuiltinRun(ctx, dbgen.EnqueueBuiltinRunParams{
		WorkspaceID: e.ws.ID, TemplateName: runs.ProbeTemplateName, Trigger: "schedule", Overrides: []byte(`{}`), Params: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.run = r
	return f
}

func (e *apiEnv) results(t *testing.T) map[[2]uuid.UUID]dbgen.ReachabilityResult {
	t.Helper()
	rows, err := e.q.ListReachabilityResults(context.Background(), e.ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[[2]uuid.UUID]dbgen.ReachabilityResult{}
	for _, r := range rows {
		out[[2]uuid.UUID{r.SourceID, r.TargetID}] = r
	}
	return out
}

func probeEvents(f probeFixture) []json.RawMessage {
	ev := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	return []json.RawMessage{
		ev(map[string]any{"event": "stats", "hosts": map[string]any{
			"src-a": map[string]int{"ok": 4},
			"src-b": map[string]int{"unreachable": 1},
		}}),
		ev(map[string]any{"event": runproto.EventProbeResult, "host": "src-a", "results": map[string]any{
			f.tgt1.String():  map[string]any{"status": "ok", "latency_ms": 3, "detail": ""},
			f.tgt2.String():  map[string]any{"status": "failed", "latency_ms": nil, "detail": "100% packet loss"},
			uuid.NewString(): map[string]any{"status": "ok", "latency_ms": 1, "detail": ""}, // unknown target
		}}),
		// Rejected: unknown field. Must not break the batch or store anything.
		ev(map[string]any{"event": runproto.EventProbeResult, "host": "src-b", "results": map[string]any{
			f.tgt1.String(): map[string]any{"status": "ok", "latency_ms": 1, "detail": "", "extra": 1},
		}}),
		// Host that is not a source of the run.
		ev(map[string]any{"event": runproto.EventProbeResult, "host": "other", "results": map[string]any{
			f.tgt1.String(): map[string]any{"status": "ok", "latency_ms": 1, "detail": ""},
		}}),
	}
}

func TestProbeRunIngestion(t *testing.T) {
	e := newAPIEnv(t)
	f := e.probeRun(t, runproto.BuiltinProbe)

	// A built-in run without template is claimable.
	code, body := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w1"})
	var cl runproto.ClaimResponse
	if code != http.StatusOK || json.Unmarshal(body, &cl) != nil || cl.RunID != f.run.ID.String() {
		t.Fatalf("claim: %d %s", code, body)
	}
	// ... and is not cancelled as an orphan of a deleted template.
	if err := e.h.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r, _ := e.q.GetRunByID(context.Background(), f.run.ID); r.CancelRequestedAt.Valid {
		t.Fatal("built-in probe run must not be cancelled as an orphan")
	}

	if code, b := e.do(t, "POST", "/runs/"+cl.RunID+"/events", cl.LeaseID, runproto.EventsRequest{Events: probeEvents(f)}); code != http.StatusNoContent {
		t.Fatalf("events: %d %s", code, b)
	}
	res := e.results(t)
	if r := res[[2]uuid.UUID{f.srcOK, f.tgt1}]; r.Status != "ok" || !r.LatencyMs.Valid || r.LatencyMs.Int32 != 3 || r.RunID.Bytes != f.run.ID {
		t.Errorf("ok result: %+v", r)
	}
	if r := res[[2]uuid.UUID{f.srcOK, f.tgt2}]; r.Status != "failed" || r.Detail != "100% packet loss" || r.LatencyMs.Valid {
		t.Errorf("failed result: %+v", r)
	}
	// The unreachable source marks all its targets probe_unreachable, not failed;
	// the rejected payload of the same host did not overwrite that.
	for _, tg := range []uuid.UUID{f.tgt1, f.tgt2} {
		if r := res[[2]uuid.UUID{f.srcDead, tg}]; r.Status != "probe_unreachable" || r.LatencyMs.Valid {
			t.Errorf("unreachable source result: %+v", r)
		}
	}
	if len(res) != 4 {
		t.Errorf("stored %d results, want 4", len(res))
	}
	// Results are stored before the run is finished.
	if code, _ := e.do(t, "POST", "/runs/"+cl.RunID+"/finish", cl.LeaseID, runproto.FinishRequest{ExitCode: 0}); code != http.StatusOK {
		t.Fatalf("finish: %d", code)
	}
}

func TestProbeEventsIgnoredForOtherRuns(t *testing.T) {
	e := newAPIEnv(t)
	f := e.probeRun(t, "") // not marked as a probe run
	// Claim through the template path: a run without builtin and without
	// template is not claimable, so claim a regular run instead.
	_ = f
	queued := e.enqueue(t)
	code, body := e.do(t, "POST", "/claim", "", runproto.ClaimRequest{WorkerID: "w1"})
	var cl runproto.ClaimResponse
	if code != http.StatusOK || json.Unmarshal(body, &cl) != nil || cl.RunID != queued.ID.String() {
		t.Fatalf("claim: %d %s", code, body)
	}
	if code, b := e.do(t, "POST", "/runs/"+cl.RunID+"/events", cl.LeaseID, runproto.EventsRequest{Events: probeEvents(f)}); code != http.StatusNoContent {
		t.Fatalf("events: %d %s", code, b)
	}
	if n := len(e.results(t)); n != 0 {
		t.Errorf("non-probe run stored %d probe results", n)
	}
}
