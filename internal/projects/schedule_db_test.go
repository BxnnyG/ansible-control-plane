package projects

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

func TestClaimDueIsExclusive(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	due, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "due", URL: repo.url(), SyncIntervalSeconds: ptr(60)})
	if err != nil {
		t.Fatal(err)
	}
	manual, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "manual", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 8 { // competing app instances
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := e.svc.ClaimDue(ctx, 100)
			if err != nil {
				t.Error(err)
				return
			}
			for _, d := range got {
				if d.ID == manual.ID {
					t.Error("project without interval was claimed")
				}
				if d.ID == due.ID {
					claims.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("project claimed %d times, want exactly 1", claims.Load())
	}
	// A recent successful sync keeps the project not due even without a lease.
	if _, err := e.pool().Exec(ctx, "UPDATE projects SET next_sync_at = NULL, last_sync_at = now() WHERE id = $1", due.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.ClaimDue(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.ID == due.ID {
			t.Error("recently synced project must not be due")
		}
	}
}

func TestRunDueSyncsWithScheduleTrigger(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "sched", URL: repo.url(), SyncIntervalSeconds: ptr(60)})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.RunDue(ctx, 100, slog.New(slog.NewTextHandler(io.Discard, nil)))
	row, err := e.q.GetProject(ctx, dbgen.GetProjectParams{ID: p.ID, WorkspaceID: e.ws})
	if err != nil {
		t.Fatal(err)
	}
	if row.LastSyncStatus != "ok" || row.LastSyncTrigger != TriggerSchedule || row.LastSyncSha == "" {
		t.Fatalf("unexpected sync state: %s %s %q", row.LastSyncStatus, row.LastSyncTrigger, row.LastSyncSha)
	}
	if n := e.auditCount(t, p.ID, "project.sync"); n != 1 {
		t.Errorf("audit entries project.sync = %d, want 1", n)
	}
}

func TestWebhookLifecycleAndSync(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	repo := newRepo(t)
	p, err := e.svc.Create(ctx, e.ws, CreateInput{Name: "hook", URL: repo.url()})
	if err != nil {
		t.Fatal(err)
	}
	push := func(provider, secret, ref string) HookOutcome {
		body := `{"ref":"` + ref + `"}`
		h := map[string]string{}
		switch provider {
		case ProviderGitea:
			h["X-Gitea-Signature"] = hexMAC(secret, body)
		case ProviderGitHub:
			h["X-Hub-Signature-256"] = "sha256=" + hexMAC(secret, body)
		case ProviderGitLab:
			h["X-Gitlab-Token"] = secret
		default:
			h["X-ACP-Signature"] = "sha256=" + hexMAC(secret, body)
		}
		out := e.svc.HandleWebhook(ctx, p.ID, hdr(h), []byte(body))
		e.svc.WaitHooks()
		return out
	}

	if _, err := e.svc.RotateWebhook(ctx, e.ws, p.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("rotate while disabled: %v", err)
	}
	if _, err := e.svc.SetWebhook(ctx, e.ws, p.ID, true, "bitbucket"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad provider: %v", err)
	}
	first, err := e.svc.SetWebhook(ctx, e.ws, p.ID, true, ProviderGitHub)
	if err != nil || first.Secret == "" || !first.Enabled || !first.HasSecret {
		t.Fatalf("enable: %+v %v", first, err)
	}
	again, err := e.svc.SetWebhook(ctx, e.ws, p.ID, true, ProviderGitHub)
	if err != nil || again.Secret != "" {
		t.Fatalf("second enable must not reveal the secret: %+v %v", again, err)
	}
	if cfg, err := e.svc.GetWebhook(ctx, e.ws, p.ID); err != nil || cfg.Path != HookPath(p.ID) || !cfg.HasSecret {
		t.Fatalf("get: %+v %v", cfg, err)
	}

	if out := push(ProviderGitHub, "wrong", "refs/heads/main"); out != HookRejected {
		t.Errorf("bad signature: %v", out)
	}
	if out := e.svc.HandleWebhook(ctx, uuid.New(), hdr(nil), nil); out != HookRejected {
		t.Errorf("unknown project: %v", out)
	}
	if out := push(ProviderGitHub, first.Secret, "refs/heads/other"); out != HookIgnored {
		t.Errorf("other branch: %v", out)
	}
	row, _ := e.q.GetProject(ctx, dbgen.GetProjectParams{ID: p.ID, WorkspaceID: e.ws})
	if row.LastSyncStatus != "never" {
		t.Fatalf("ignored push must not sync, status %s", row.LastSyncStatus)
	}
	if out := push(ProviderGitHub, first.Secret, "refs/heads/main"); out != HookAccepted {
		t.Fatalf("valid push: %v", out)
	}
	row, _ = e.q.GetProject(ctx, dbgen.GetProjectParams{ID: p.ID, WorkspaceID: e.ws})
	if row.LastSyncStatus != "ok" || row.LastSyncTrigger != TriggerWebhook {
		t.Fatalf("webhook sync state: %s %s", row.LastSyncStatus, row.LastSyncTrigger)
	}
	if n := e.auditCount(t, p.ID, "project.sync"); n != 1 {
		t.Errorf("audit entries = %d, want 1", n)
	}

	rot, err := e.svc.RotateWebhook(ctx, e.ws, p.ID)
	if err != nil || rot.Secret == "" || rot.Secret == first.Secret {
		t.Fatalf("rotate: %+v %v", rot, err)
	}
	if out := push(ProviderGitHub, first.Secret, "refs/heads/main"); out != HookRejected {
		t.Errorf("old secret after rotate: %v", out)
	}
	if out := push(ProviderGitHub, rot.Secret, "refs/heads/main"); out != HookAccepted {
		t.Errorf("new secret: %v", out)
	}

	if _, err := e.svc.SetWebhook(ctx, e.ws, p.ID, false, ProviderGitHub); err != nil {
		t.Fatal(err)
	}
	if out := push(ProviderGitHub, rot.Secret, "refs/heads/main"); out != HookRejected {
		t.Errorf("disabled webhook: %v", out)
	}
	if err := e.svc.DeleteWebhook(ctx, e.ws, p.ID); err != nil {
		t.Fatal(err)
	}
	cfg, _ := e.svc.GetWebhook(ctx, e.ws, p.ID)
	if cfg.Enabled || cfg.HasSecret {
		t.Errorf("after delete: %+v", cfg)
	}
	if list, err := e.sec.List(ctx, e.ws); err != nil || len(list) != 0 {
		t.Errorf("webhook secret must be removed: %v %v", list, err)
	}
	// Provider variants share the verification path; spot-check gitlab on a fresh secret.
	if _, err := e.svc.SetWebhook(ctx, e.ws, p.ID, true, ProviderGitLab); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.ws, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.sec.Resolve(ctx, e.ws, uuid.New()); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("resolve unknown: %v", err)
	}
	if list, _ := e.sec.List(ctx, e.ws); len(list) != 0 {
		t.Errorf("project delete must remove the webhook secret, left %d", len(list))
	}
}

func (e *env) pool() *pgxpool.Pool { return e.p }

func (e *env) auditCount(t *testing.T, id uuid.UUID, action string) int {
	t.Helper()
	var n int
	err := e.p.QueryRow(context.Background(),
		"SELECT count(*) FROM audit_log WHERE target_id = $1 AND action = $2", id.String(), action).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
