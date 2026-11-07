package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeSender records messages and fails on demand.
type fakeSender struct {
	mu   sync.Mutex
	got  []sent
	fail error
}

type sent struct {
	ch   Channel
	cred string
	msg  Message
}

func (f *fakeSender) Send(_ context.Context, ch Channel, cred []byte, msg Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, sent{ch, string(cred), msg})
	return f.fail
}

func (f *fakeSender) take() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.got
	f.got = nil
	return out
}

type env struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
	ws   dbgen.Workspace
	tpl  dbgen.Template
	sec  *secrets.Service
	snd  *fakeSender
	disp *Dispatcher
	svc  *Service
	now  time.Time
}

func setup(t *testing.T) *env {
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
	ws, err := q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: "n-" + sfx, Name: "Notify " + sfx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	proj, err := q.CreateProject(ctx, dbgen.CreateProjectParams{WorkspaceID: ws.ID, Name: "p", Slug: "p", Url: "https://git.example.test/x.git", DefaultRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := q.CreateInventory(ctx, dbgen.CreateInventoryParams{WorkspaceID: ws.ID, Name: "i", Slug: "i", Mode: "db", SourceFormat: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := q.CreateTemplate(ctx, dbgen.CreateTemplateParams{WorkspaceID: ws.ID, ProjectID: proj.ID, InventoryID: inv.ID,
		Name: "site", Slug: "site", Playbook: "site.yml", ExtraVars: []byte(`{}`), EnvVars: []byte(`{}`), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	prov, err := secrets.NewLocalProvider(key)
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewService(q, prov)
	snd := &fakeSender{}
	senders := func(string) (Sender, error) { return snd, nil }
	e := &env{pool: pool, q: q, ws: ws, tpl: tpl, sec: sec, snd: snd, now: time.Now()}
	e.disp = New(DispatcherOptions{DB: pool, Secrets: sec, PublicURL: "https://acp.example.test", RetentionDays: 30, Senders: senders,
		Now: func() time.Time { return e.now }})
	e.svc = NewService(ServiceOptions{DB: pool, Secrets: sec, PublicURL: "https://acp.example.test", Senders: senders})
	return e
}

// run inserts a queued run for the template; params may be "" for {}.
func (e *env) run(t *testing.T, params string) uuid.UUID {
	t.Helper()
	if params == "" {
		params = "{}"
	}
	var id uuid.UUID
	err := e.pool.QueryRow(context.Background(), `INSERT INTO runs (workspace_id, template_id, template_name, trigger, params)
		VALUES ($1, $2, $3, 'manual', $4::jsonb) RETURNING id`, e.ws.ID, e.tpl.ID, e.tpl.Name, params).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// finish moves a run to a terminal status with a finished_at offset.
func (e *env) finish(t *testing.T, id uuid.UUID, status string, changed int, finishedAgo time.Duration) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(), `UPDATE runs SET status = $2, hosts_changed = $3, started_at = $4::timestamptz - interval '1 minute',
		finished_at = $4::timestamptz WHERE id = $1`, id, status, changed, time.Now().Add(-finishedAgo))
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) channel(t *testing.T, name string, secretID *uuid.UUID) ChannelView {
	t.Helper()
	v, err := e.svc.CreateChannel(context.Background(), e.ws.ID, ChannelInput{Name: name, Type: TypeNtfy,
		Config: []byte(`{"url":"http://ntfy.example.test","topic":"acp"}`), SecretID: secretID, Enabled: true}, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *env) rule(t *testing.T, ch uuid.UUID, tpl *uuid.UUID, events ...string) {
	t.Helper()
	if _, err := e.svc.CreateRule(context.Background(), e.ws.ID, RuleInput{ChannelID: ch, Events: events, TemplateID: tpl, Enabled: true}, uuid.Nil); err != nil {
		t.Fatal(err)
	}
}

func (e *env) eventCount(t *testing.T, runID uuid.UUID) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_events WHERE run_id = $1`, runID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) process(t *testing.T) {
	t.Helper()
	if _, err := e.disp.ProcessEvents(context.Background(), quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.SendDue(context.Background(), quiet); err != nil {
		t.Fatal(err)
	}
}

func TestTriggerFiresOnTerminalTransitionOnly(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := e.run(t, "")
	step := func(sql string, wantEvents int) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, id); err != nil {
			t.Fatal(err)
		}
		if got := e.eventCount(t, id); got != wantEvents {
			t.Fatalf("%s: %d events, want %d", sql, got, wantEvents)
		}
	}
	step(`UPDATE runs SET status = 'running' WHERE id = $1`, 0)
	step(`UPDATE runs SET hosts_ok = 3 WHERE id = $1`, 0)
	step(`UPDATE runs SET status = 'failed', finished_at = now() WHERE id = $1`, 1)
	step(`UPDATE runs SET status = 'lost' WHERE id = $1`, 1) // terminal -> terminal: no second event

	// cancel of a queued run and direct inserts of finished runs (imports)
	c := e.run(t, "")
	if _, err := e.pool.Exec(ctx, `UPDATE runs SET status = 'cancelled' WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	if e.eventCount(t, c) != 1 {
		t.Fatal("cancelled queued run must emit an event")
	}
	var imp uuid.UUID
	if err := e.pool.QueryRow(ctx, `INSERT INTO runs (workspace_id, template_name, status, trigger) VALUES ($1, 'x', 'failed', 'api') RETURNING id`, e.ws.ID).Scan(&imp); err != nil {
		t.Fatal(err)
	}
	if e.eventCount(t, imp) != 0 {
		t.Fatal("inserting a finished run must not emit an event")
	}
}

func (e *env) otherTemplate(t *testing.T) uuid.UUID {
	t.Helper()
	proj, err := e.q.GetTemplateByID(context.Background(), e.tpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := e.q.CreateTemplate(context.Background(), dbgen.CreateTemplateParams{WorkspaceID: e.ws.ID, ProjectID: proj.ProjectID,
		InventoryID: proj.InventoryID, Name: "other", Slug: "other", Playbook: "site.yml", ExtraVars: []byte(`{}`), EnvVars: []byte(`{}`), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	return tpl.ID
}

func TestDispatchMatchingAndPrecedence(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	all := e.channel(t, "all", nil)
	e.rule(t, all.ID, nil, KindFinished)
	failOnly := e.channel(t, "fail-only", nil)
	e.rule(t, failOnly.ID, nil, KindFailed)
	multi := e.channel(t, "multi", nil) // two rules, still one message per run
	e.rule(t, multi.ID, nil, KindFinished)
	e.rule(t, multi.ID, nil, KindFailed)
	other := e.otherTemplate(t)
	filtered := e.channel(t, "other-template", nil)
	e.rule(t, filtered.ID, &other, KindFinished, KindFailed)
	same := e.channel(t, "same-template", nil)
	e.rule(t, same.ID, &e.tpl.ID, KindFailed)
	off, err := e.svc.CreateChannel(ctx, e.ws.ID, ChannelInput{Name: "disabled", Type: TypeNtfy,
		Config: []byte(`{"url":"http://ntfy.example.test","topic":"acp"}`), Enabled: false}, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	e.rule(t, off.ID, nil, KindFinished)

	id := e.run(t, "")
	e.finish(t, id, "failed", 0, time.Second)
	e.process(t)
	got := e.snd.take()
	byKind := map[string]int{}
	for _, s := range got {
		byKind[s.msg.Kind]++
		if s.msg.URL != "https://acp.example.test/runs/"+id.String() || s.msg.Workspace != e.ws.Name || s.msg.Status != "failed" {
			t.Fatalf("message: %+v", s.msg)
		}
	}
	// all -> finished; fail-only, multi, same-template -> failed
	if len(got) != 4 || byKind[KindFailed] != 3 || byKind[KindFinished] != 1 {
		t.Fatalf("deliveries: %d, kinds %v", len(got), byKind)
	}
	// re-processing is a no-op
	e.process(t)
	if n := len(e.snd.take()); n != 0 {
		t.Fatalf("%d duplicate sends", n)
	}
}

func TestRecoveredAndDrift(t *testing.T) {
	e := setup(t)
	ch := e.channel(t, "c", nil)
	e.rule(t, ch.ID, nil, KindRecovered, KindDrift)

	first := e.run(t, "")
	e.finish(t, first, "failed", 0, 10*time.Minute)
	canc := e.run(t, "")
	e.finish(t, canc, "cancelled", 0, 5*time.Minute) // ignored for "previous"
	second := e.run(t, "")
	e.finish(t, second, "succeeded", 0, time.Minute)
	e.process(t)
	got := e.snd.take()
	if len(got) != 1 || got[0].msg.Kind != KindRecovered || got[0].msg.RunID != second.String() {
		t.Fatalf("recovered: %+v", got)
	}

	third := e.run(t, "")
	e.finish(t, third, "succeeded", 0, 30*time.Second) // previous succeeded: neither recovered nor drift
	check := e.run(t, `{"check": true}`)
	e.finish(t, check, "succeeded", 4, 10*time.Second)
	e.process(t)
	got = e.snd.take()
	if len(got) != 1 || got[0].msg.Kind != KindDrift || !got[0].msg.Check || got[0].msg.HostsChanged != 4 {
		t.Fatalf("drift: %+v", got)
	}
}

func TestSecretResolvedAndErrorsSanitized(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const token = "dummy-token-1234567890"
	meta, err := e.sec.Create(ctx, e.ws.ID, secrets.CreateInput{Name: "tok", Type: secrets.TypeToken, Value: []byte(token)})
	if err != nil {
		t.Fatal(err)
	}
	ch := e.channel(t, "c", &meta.ID)
	e.rule(t, ch.ID, nil, KindFinished)
	e.snd.fail = errors.New("boom " + token)
	id := e.run(t, "")
	e.finish(t, id, "succeeded", 0, time.Second)
	e.process(t)
	got := e.snd.take()
	if len(got) != 1 || got[0].cred != token || got[0].msg.TxnID == "" {
		t.Fatalf("sends: %+v", got)
	}
	dl, err := e.svc.ListDeliveries(ctx, e.ws.ID, ch.ID, 10)
	if err != nil || len(dl) != 1 {
		t.Fatalf("deliveries: %v %v", dl, err)
	}
	if dl[0].Status != "pending" || dl[0].Attempts != 1 || !strings.Contains(dl[0].LastError, "boom") || strings.Contains(dl[0].LastError, token) {
		t.Fatalf("delivery: %+v", dl[0])
	}
	if !dl[0].NextAttemptAt.After(time.Now()) {
		t.Fatal("retry must be scheduled in the future")
	}
}

func TestRetriesThenFailsAndSucceeds(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ch := e.channel(t, "c", nil)
	e.rule(t, ch.ID, nil, KindFinished)
	e.snd.fail = errors.New("down")
	id := e.run(t, "")
	e.finish(t, id, "succeeded", 0, time.Second)
	e.process(t)
	for i := 2; i <= MaxAttempts; i++ {
		// not due yet: nothing is sent
		if n, _ := e.disp.SendDue(ctx, quiet); n != 0 {
			t.Fatalf("attempt %d sent before backoff elapsed", i)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if n, err := e.disp.SendDue(ctx, quiet); err != nil || n != 1 {
			t.Fatalf("attempt %d: n=%d err=%v", i, n, err)
		}
	}
	dl, _ := e.svc.ListDeliveries(ctx, e.ws.ID, ch.ID, 10)
	if len(dl) != 1 || dl[0].Status != "failed" || dl[0].Attempts != MaxAttempts {
		t.Fatalf("after max attempts: %+v", dl)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.disp.SendDue(ctx, quiet); n != 0 {
		t.Fatal("failed deliveries must not be retried")
	}

	// a transient failure that recovers
	e.snd.fail = nil
	ch2 := e.channel(t, "c2", nil)
	e.rule(t, ch2.ID, nil, KindFinished)
	id2 := e.run(t, "")
	e.snd.fail = errors.New("flaky")
	e.finish(t, id2, "succeeded", 0, time.Second)
	e.process(t)
	e.snd.take()
	e.snd.fail = nil
	if _, err := e.pool.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 second' WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.SendDue(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	dl, _ = e.svc.ListDeliveries(ctx, e.ws.ID, ch2.ID, 10)
	if len(dl) != 1 || dl[0].Status != "sent" || dl[0].SentAt == nil || dl[0].LastError != "" {
		t.Fatalf("recovered delivery: %+v", dl)
	}

	// workspace-wide listing with SQL filters
	all, err := e.svc.ListWorkspaceDeliveries(ctx, e.ws.ID, nil, "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("workspace deliveries: %v %+v", err, all)
	}
	failed, _ := e.svc.ListWorkspaceDeliveries(ctx, e.ws.ID, nil, "failed", 10)
	if len(failed) != 1 || failed[0].ChannelID != ch.ID {
		t.Fatalf("failed filter: %+v", failed)
	}
	one, _ := e.svc.ListWorkspaceDeliveries(ctx, e.ws.ID, &ch2.ID, "", 10)
	if len(one) != 1 || one[0].Status != "sent" {
		t.Fatalf("channel filter: %+v", one)
	}
	if other, _ := e.svc.ListWorkspaceDeliveries(ctx, uuid.New(), nil, "", 10); len(other) != 0 {
		t.Fatalf("other workspace sees deliveries: %+v", other)
	}
}

func TestDisabledChannelFailsPending(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ch := e.channel(t, "c", nil)
	e.rule(t, ch.ID, nil, KindFinished)
	id := e.run(t, "")
	e.finish(t, id, "succeeded", 0, time.Second)
	if _, err := e.disp.ProcessEvents(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE notification_channels SET enabled = false WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.disp.SendDue(ctx, quiet); err != nil {
		t.Fatal(err)
	}
	dl, _ := e.svc.ListDeliveries(ctx, e.ws.ID, ch.ID, 10)
	if len(e.snd.take()) != 0 || len(dl) != 1 || dl[0].Status != "failed" {
		t.Fatalf("deliveries: %+v", dl)
	}
}

func TestPrune(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ch := e.channel(t, "c", nil)
	e.rule(t, ch.ID, nil, KindFinished)
	old := e.run(t, "")
	e.finish(t, old, "succeeded", 0, time.Second)
	e.process(t)
	pending := e.run(t, "")
	e.snd.fail = errors.New("down")
	e.finish(t, pending, "failed", 0, time.Second)
	e.process(t)
	if _, err := e.pool.Exec(ctx, `UPDATE notification_events SET processed_at = now() - interval '60 days'`); err != nil {
		t.Fatal(err)
	}
	n, err := e.disp.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// the event with a pending (retrying) delivery is kept
	if n != 1 || e.eventCount(t, old) != 0 || e.eventCount(t, pending) != 1 {
		t.Fatalf("pruned %d, old=%d pending=%d", n, e.eventCount(t, old), e.eventCount(t, pending))
	}
	e.disp.retention = 0
	if n, _ := e.disp.Prune(ctx); n != 0 {
		t.Fatal("retention 0 keeps everything")
	}
}

func TestServiceCRUDAndSendTest(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.svc.CreateChannel(ctx, e.ws.ID, ChannelInput{Name: "bad", Type: TypeMatrix, Config: []byte(`{}`)}, uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid config: %v", err)
	}
	missing := uuid.New()
	if _, err := e.svc.CreateChannel(ctx, e.ws.ID, ChannelInput{Name: "bad", Type: TypeNtfy,
		Config: []byte(`{"url":"http://n.example.test","topic":"t"}`), SecretID: &missing}, uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown secret: %v", err)
	}
	ch := e.channel(t, "c", nil)
	if _, err := e.svc.CreateChannel(ctx, e.ws.ID, ChannelInput{Name: "c", Type: TypeNtfy,
		Config: []byte(`{"url":"http://n.example.test","topic":"t"}`)}, uuid.Nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	upd, err := e.svc.UpdateChannel(ctx, e.ws.ID, ch.ID, ChannelInput{Name: "renamed", Config: []byte(`{"url":"http://n.example.test","topic":"t2"}`), Enabled: false}, uuid.Nil)
	if err != nil || upd.Name != "renamed" || upd.Enabled || upd.Type != TypeNtfy {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if _, err := e.svc.CreateRule(ctx, e.ws.ID, RuleInput{ChannelID: ch.ID, Events: []string{"run.bogus"}}, uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad event: %v", err)
	}
	if _, err := e.svc.CreateRule(ctx, e.ws.ID, RuleInput{ChannelID: uuid.New(), Events: []string{KindFailed}}, uuid.Nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown channel: %v", err)
	}
	e.rule(t, ch.ID, nil, KindFailed, KindFailed)
	rules, err := e.svc.ListRules(ctx, e.ws.ID)
	if err != nil || len(rules) != 1 || len(rules[0].Events) != 1 {
		t.Fatalf("rules: %+v %v", rules, err)
	}

	// test send works for a disabled channel and uses the sample message
	if err := e.svc.SendTest(ctx, e.ws.ID, ch.ID); err != nil {
		t.Fatal(err)
	}
	got := e.snd.take()
	if len(got) != 1 || !got[0].msg.Test || got[0].msg.Workspace != e.ws.Name {
		t.Fatalf("test send: %+v", got)
	}
	e.snd.fail = errors.New("nope")
	if err := e.svc.SendTest(ctx, e.ws.ID, ch.ID); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("failed test send: %v", err)
	}
	if err := e.svc.SendTest(ctx, e.ws.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}

	// deleting a channel cascades to rules; deleting twice reports not found
	if err := e.svc.DeleteChannel(ctx, e.ws.ID, ch.ID); err != nil {
		t.Fatal(err)
	}
	if rules, _ := e.svc.ListRules(ctx, e.ws.ID); len(rules) != 0 {
		t.Fatal("rules must be removed with the channel")
	}
	if err := e.svc.DeleteChannel(ctx, e.ws.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestServiceTestDraft(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const stored = "stored-credential-xyz"
	const typed = "typed-credential-abc"
	meta, err := e.sec.Create(ctx, e.ws.ID, secrets.CreateInput{Name: "tok", Type: secrets.TypeToken, Value: []byte(stored)})
	if err != nil {
		t.Fatal(err)
	}
	ch := e.channel(t, "c", &meta.ID)
	cfg := json.RawMessage(`{"url":"http://ntfy.example.test","topic":"draft"}`)

	cases := []struct {
		name     string
		in       DraftInput
		wantCred string
		wantErr  error
		sendFail error
	}{
		{name: "no credential", in: DraftInput{Type: TypeNtfy, Config: cfg}},
		{name: "typed value", in: DraftInput{Type: TypeNtfy, Config: cfg, SecretValue: []byte(typed)}, wantCred: typed},
		{name: "existing secret id", in: DraftInput{Type: TypeNtfy, Config: cfg, SecretID: &meta.ID}, wantCred: stored},
		{name: "stored secret of channel", in: DraftInput{Type: TypeNtfy, Config: cfg, ChannelID: &ch.ID}, wantCred: stored},
		{name: "typed value wins over channel", in: DraftInput{Type: TypeNtfy, Config: cfg, ChannelID: &ch.ID, SecretValue: []byte(typed)}, wantCred: typed},
		{name: "invalid config", in: DraftInput{Type: TypeNtfy, Config: json.RawMessage(`{"url":"x"}`)}, wantErr: ErrInvalid},
		{name: "matrix needs credential", in: DraftInput{Type: TypeMatrix, Config: json.RawMessage(`{"homeserver":"https://m.example.test","room_id":"!r:m.example.test"}`)}, wantErr: ErrInvalid},
		{name: "unknown channel", in: DraftInput{Type: TypeNtfy, Config: cfg, ChannelID: ptrUUID(uuid.New())}, wantErr: ErrNotFound},
		{name: "foreign secret", in: DraftInput{Type: TypeNtfy, Config: cfg, SecretID: ptrUUID(uuid.New())}, wantErr: ErrInvalid},
		{name: "send error hides typed credential", in: DraftInput{Type: TypeNtfy, Config: cfg, SecretValue: []byte(typed)}, sendFail: errors.New("boom " + typed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.snd.take()
			e.snd.fail = tc.sendFail
			err := e.svc.TestDraft(ctx, e.ws.ID, tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if got := e.snd.take(); len(got) != 0 {
					t.Fatal("nothing must be sent")
				}
				return
			}
			if tc.sendFail != nil {
				if err == nil || strings.Contains(err.Error(), typed) || !strings.Contains(err.Error(), "boom") {
					t.Fatalf("sanitized error expected, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := e.snd.take()
			if len(got) != 1 || got[0].cred != tc.wantCred || !got[0].msg.Test {
				t.Fatalf("sent: %+v", got)
			}
		})
	}
	e.snd.fail = nil
	if n, _ := e.svc.ListChannels(ctx, e.ws.ID); len(n) != 1 {
		t.Fatalf("draft test must not persist channels, have %d", len(n))
	}
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }
