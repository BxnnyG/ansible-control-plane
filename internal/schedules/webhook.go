package schedules

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// ErrRejected is returned by HandleWebhook for unknown template, disabled
// webhook and bad signature alike, so callers cannot tell them apart.
var ErrRejected = errors.New("webhook rejected")

// SecretStore is the part of the secrets service used for webhook secrets;
// *secrets.Service implements it.
type SecretStore interface {
	Create(ctx context.Context, ws uuid.UUID, in secrets.CreateInput) (secrets.Meta, error)
	Update(ctx context.Context, ws, id uuid.UUID, in secrets.UpdateInput) (secrets.Meta, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
}

// HookPath returns the public webhook path of a template.
func HookPath(id uuid.UUID) string { return "/api/hooks/templates/" + id.String() }

// Webhook is the API view of a template's webhook. It never contains the
// secret; Secret is set exactly once, when it is generated.
type Webhook struct {
	Enabled   bool   `json:"enabled"`
	HasSecret bool   `json:"has_secret"`
	Path      string `json:"path"`
	Secret    string `json:"secret,omitempty"`
}

func toWebhook(t dbgen.Template) Webhook {
	return Webhook{Enabled: t.WebhookEnabled, HasSecret: t.WebhookSecretID.Valid, Path: HookPath(t.ID)}
}

func (s *Service) loadTemplate(ctx context.Context, ws, id uuid.UUID) (dbgen.Template, error) {
	if s.sec == nil {
		return dbgen.Template{}, invalid("template webhooks are not available")
	}
	t, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws})
	return t, mapErr(err)
}

// GetWebhook returns the webhook configuration of a template.
func (s *Service) GetWebhook(ctx context.Context, ws, id uuid.UUID) (Webhook, error) {
	t, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Webhook{}, mapErr(err)
	}
	return toWebhook(t), nil
}

// RotateWebhook enables the webhook and replaces its secret. The new secret
// is returned exactly once.
func (s *Service) RotateWebhook(ctx context.Context, ws, id uuid.UUID) (Webhook, error) {
	t, err := s.loadTemplate(ctx, ws, id)
	if err != nil {
		return Webhook{}, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Webhook{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	sec := hex.EncodeToString(b)
	secretID := t.WebhookSecretID
	if secretID.Valid {
		if _, err := s.sec.Update(ctx, ws, secretID.Bytes, secrets.UpdateInput{Value: []byte(sec)}); err != nil {
			if !errors.Is(err, secrets.ErrNotFound) {
				return Webhook{}, fmt.Errorf("rotate webhook secret: %w", err)
			}
			secretID = pgtype.UUID{}
		}
	}
	if !secretID.Valid {
		m, err := s.sec.Create(ctx, ws, secrets.CreateInput{Name: "webhook-template-" + hex.EncodeToString(t.ID[:4]),
			Type: secrets.TypeToken, Description: "Webhook secret of template " + t.Name, Value: []byte(sec)})
		if err != nil {
			return Webhook{}, fmt.Errorf("store webhook secret: %w", err)
		}
		secretID = pgtype.UUID{Bytes: m.ID, Valid: true}
	}
	upd, err := s.q.SetTemplateWebhook(ctx, dbgen.SetTemplateWebhookParams{ID: id, WorkspaceID: ws, WebhookEnabled: true, WebhookSecretID: secretID})
	if err != nil {
		return Webhook{}, fmt.Errorf("enable webhook: %w", mapErr(err))
	}
	w := toWebhook(upd)
	w.Secret = sec
	return w, nil
}

// DisableWebhook disables the webhook and deletes its secret.
func (s *Service) DisableWebhook(ctx context.Context, ws, id uuid.UUID) error {
	t, err := s.loadTemplate(ctx, ws, id)
	if err != nil {
		return err
	}
	if _, err := s.q.SetTemplateWebhook(ctx, dbgen.SetTemplateWebhookParams{ID: id, WorkspaceID: ws}); err != nil {
		return fmt.Errorf("disable webhook: %w", mapErr(err))
	}
	if t.WebhookSecretID.Valid {
		if err := s.sec.Delete(ctx, ws, t.WebhookSecretID.Bytes); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return fmt.Errorf("delete webhook secret: %w", err)
		}
	}
	return nil
}

// signedBy returns the provider whose signature header validates, or "".
func signedBy(secret []byte, header func(string) string, body []byte) string {
	for _, p := range []string{projects.ProviderGeneric, projects.ProviderGitea, projects.ProviderGitHub, projects.ProviderGitLab} {
		if projects.VerifySignature(p, secret, header, body) {
			return p
		}
	}
	return ""
}

// HandleWebhook authenticates a call to a template webhook and enqueues a run
// with trigger=webhook. Any signature scheme of the project hooks is accepted
// (X-ACP-Signature, X-Gitea-Signature, X-Hub-Signature-256, X-Gitlab-Token).
// Only a body signed with the generic scheme may carry JSON run overrides;
// bodies of Git providers are push payloads and are ignored. Authentication
// failures return ErrRejected; everything after is an authenticated caller's
// error (runs.ErrInvalid, runs.ErrNotSynced, ...).
func (s *Service) HandleWebhook(ctx context.Context, id uuid.UUID, header func(string) string, body []byte) (runs.Run, error) {
	if s.sec == nil {
		return runs.Run{}, ErrRejected
	}
	t, err := s.q.GetTemplateByID(ctx, id)
	if err != nil || !t.WebhookEnabled || !t.WebhookSecretID.Valid {
		return runs.Run{}, ErrRejected
	}
	res, err := s.sec.Resolve(ctx, t.WorkspaceID, t.WebhookSecretID.Bytes)
	if err != nil {
		return runs.Run{}, ErrRejected
	}
	defer res.Value.Zero()
	provider := signedBy(res.Value.Reveal(), header, body)
	if provider == "" {
		return runs.Run{}, ErrRejected
	}
	var ov runs.Overrides
	if provider == projects.ProviderGeneric && len(bytesTrim(body)) > 0 {
		dec := json.NewDecoder(bytesReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ov); err != nil {
			return runs.Run{}, invalid("malformed overrides body")
		}
	}
	run, err := s.runs.Enqueue(ctx, t.WorkspaceID, t.ID, ov, runs.TriggerWebhook, runs.Actor{}, nil)
	details := map[string]any{"provider": provider}
	action := "template.webhook_run"
	if err != nil {
		action = "template.webhook_failed"
		details["error"] = err.Error()
	} else {
		details["run_id"] = run.ID
	}
	b, _ := json.Marshal(details)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, aerr := s.q.InsertAudit(actx, dbgen.InsertAuditParams{ActorLabel: "webhook:" + provider, Action: action,
		TargetType: "template", TargetID: t.ID.String(), WorkspaceID: pgtype.UUID{Bytes: t.WorkspaceID, Valid: true}, Details: b}); aerr != nil {
		slog.Error("audit write failed", "action", action, "error", aerr)
	}
	return run, err
}

func bytesTrim(b []byte) []byte { return bytes.TrimSpace(b) }

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
