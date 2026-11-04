package projects

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Webhook providers and the headers carrying their signature.
const (
	ProviderGitea   = "gitea"
	ProviderGitHub  = "github"
	ProviderGitLab  = "gitlab"
	ProviderGeneric = "generic"
)

// MaxWebhookBody is the largest accepted webhook request body.
const MaxWebhookBody = 1 << 20

// HookPath returns the public webhook path of a project.
func HookPath(id uuid.UUID) string { return "/api/hooks/projects/" + id.String() }

// ValidProvider reports whether p is a supported webhook provider.
func ValidProvider(p string) bool {
	switch p {
	case ProviderGitea, ProviderGitHub, ProviderGitLab, ProviderGeneric:
		return true
	}
	return false
}

// Webhook is the API view of a project's webhook configuration. It never
// contains the secret.
type Webhook struct {
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider"`
	HasSecret bool   `json:"has_secret"`
	Path      string `json:"path"`
}

// WebhookSecret is returned once when a secret is generated.
type WebhookSecret struct {
	Webhook
	Secret string `json:"secret,omitempty"`
}

// VerifySignature checks the provider-specific signature of body. header
// returns a request header value. The comparison is constant time.
func VerifySignature(provider string, secret []byte, header func(string) string, body []byte) bool {
	if len(secret) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := mac.Sum(nil)
	hexSig := func(v string) bool {
		got, err := hex.DecodeString(v)
		return err == nil && hmac.Equal(got, want)
	}
	switch provider {
	case ProviderGitea:
		return hexSig(strings.TrimSpace(header("X-Gitea-Signature")))
	case ProviderGitHub:
		v, ok := strings.CutPrefix(strings.TrimSpace(header("X-Hub-Signature-256")), "sha256=")
		return ok && hexSig(v)
	case ProviderGeneric:
		v, ok := strings.CutPrefix(strings.TrimSpace(header("X-ACP-Signature")), "sha256=")
		return ok && hexSig(v)
	case ProviderGitLab:
		got, exp := sha256.Sum256([]byte(header("X-Gitlab-Token"))), sha256.Sum256(secret)
		return subtle.ConstantTimeCompare(got[:], exp[:]) == 1
	}
	return false
}

// RefMatches reports whether a pushed ref (for example refs/heads/main)
// belongs to the configured project ref.
func RefMatches(pushed, configured string) bool {
	if pushed == "" || configured == "" {
		return false
	}
	return pushed == configured || pushed == "refs/heads/"+configured || pushed == "refs/tags/"+configured
}

// pushedRef extracts the "ref" field of a push payload; "" when absent.
func pushedRef(body []byte) string {
	var p struct {
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &p) != nil {
		return ""
	}
	return p.Ref
}

func toWebhook(r dbgen.Project) Webhook {
	return Webhook{Enabled: r.WebhookEnabled, Provider: r.WebhookProvider,
		HasSecret: r.WebhookSecretID.Valid, Path: HookPath(r.ID)}
}

func newHookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GetWebhook returns the webhook configuration without the secret.
func (s *Service) GetWebhook(ctx context.Context, ws, id uuid.UUID) (Webhook, error) {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return Webhook{}, err
	}
	return toWebhook(row), nil
}

// createHookSecret stores a fresh secret and returns its id and plaintext.
func (s *Service) createHookSecret(ctx context.Context, ws uuid.UUID, row dbgen.Project) (uuid.UUID, string, error) {
	sec, err := newHookSecret()
	if err != nil {
		return uuid.Nil, "", err
	}
	name := fmt.Sprintf("webhook-%s-%s", row.Slug, hex.EncodeToString(row.ID[:4]))
	m, err := s.o.Secrets.Create(ctx, ws, secrets.CreateInput{Name: name, Type: secrets.TypeToken,
		Description: "Push webhook secret of project " + row.Name, Value: []byte(sec)})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("store webhook secret: %w", err)
	}
	return m.ID, sec, nil
}

// SetWebhook enables or disables the webhook and selects the provider. The
// secret is generated on first enable and returned exactly once.
func (s *Service) SetWebhook(ctx context.Context, ws, id uuid.UUID, enabled bool, provider string) (WebhookSecret, error) {
	if !ValidProvider(provider) {
		return WebhookSecret{}, invalid("provider must be one of gitea, github, gitlab, generic")
	}
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return WebhookSecret{}, err
	}
	var plain string
	secretID := row.WebhookSecretID
	if enabled && !secretID.Valid {
		sid, sec, err := s.createHookSecret(ctx, ws, row)
		if err != nil {
			return WebhookSecret{}, err
		}
		secretID, plain = pgtype.UUID{Bytes: sid, Valid: true}, sec
	}
	upd, err := s.o.Store.SetProjectWebhook(ctx, dbgen.SetProjectWebhookParams{
		ID: id, WorkspaceID: ws, WebhookEnabled: enabled, WebhookProvider: provider, WebhookSecretID: secretID})
	if err != nil {
		return WebhookSecret{}, fmt.Errorf("set webhook: %w", mapErr(err))
	}
	return WebhookSecret{Webhook: toWebhook(upd), Secret: plain}, nil
}

// RotateWebhook replaces the secret and returns the new one exactly once.
func (s *Service) RotateWebhook(ctx context.Context, ws, id uuid.UUID) (WebhookSecret, error) {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return WebhookSecret{}, err
	}
	if !row.WebhookEnabled {
		return WebhookSecret{}, invalid("webhook is not enabled")
	}
	sec, err := newHookSecret()
	if err != nil {
		return WebhookSecret{}, err
	}
	if row.WebhookSecretID.Valid {
		_, err = s.o.Secrets.Update(ctx, ws, row.WebhookSecretID.Bytes, secrets.UpdateInput{Value: []byte(sec)})
		if err != nil {
			return WebhookSecret{}, fmt.Errorf("rotate webhook secret: %w", err)
		}
		return WebhookSecret{Webhook: toWebhook(row), Secret: sec}, nil
	}
	out, err := s.SetWebhook(ctx, ws, id, true, row.WebhookProvider)
	return out, err
}

// DeleteWebhook disables the webhook and removes its secret.
func (s *Service) DeleteWebhook(ctx context.Context, ws, id uuid.UUID) error {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return err
	}
	if _, err := s.o.Store.SetProjectWebhook(ctx, dbgen.SetProjectWebhookParams{
		ID: id, WorkspaceID: ws, WebhookEnabled: false, WebhookProvider: row.WebhookProvider}); err != nil {
		return fmt.Errorf("clear webhook: %w", mapErr(err))
	}
	if row.WebhookSecretID.Valid {
		if err := s.o.Secrets.Delete(ctx, ws, row.WebhookSecretID.Bytes); err != nil && !errors.Is(err, secrets.ErrNotFound) {
			return fmt.Errorf("delete webhook secret: %w", err)
		}
	}
	return nil
}

// HookOutcome is the result of handling an incoming webhook.
type HookOutcome int

// Webhook outcomes. Rejected covers unknown project, disabled webhook and bad
// signature alike so callers cannot tell them apart.
const (
	HookRejected HookOutcome = iota
	HookAccepted
	HookIgnored
)

// HandleWebhook authenticates a push event and starts an asynchronous sync
// when the pushed ref matches the project's ref.
func (s *Service) HandleWebhook(ctx context.Context, id uuid.UUID, header func(string) string, body []byte) HookOutcome {
	row, err := s.o.Store.GetProjectByID(ctx, id)
	if err != nil || !row.WebhookEnabled || !row.WebhookSecretID.Valid {
		return HookRejected
	}
	res, err := s.o.Secrets.Resolve(ctx, row.WorkspaceID, row.WebhookSecretID.Bytes)
	if err != nil {
		return HookRejected
	}
	defer res.Value.Zero()
	if !VerifySignature(row.WebhookProvider, res.Value.Reveal(), header, body) {
		return HookRejected
	}
	if !RefMatches(pushedRef(body), row.DefaultRef) {
		return HookIgnored
	}
	ws, provider := row.WorkspaceID, row.WebhookProvider
	s.hookWG.Add(1)
	go func() {
		defer s.hookWG.Done()
		s.hookSem <- struct{}{}
		defer func() { <-s.hookSem }()
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.o.SyncTimeout+time.Minute)
		defer cancel()
		err := s.syncTriggered(actx, ws, id, TriggerWebhook, "webhook:"+provider)
		if err != nil && !errors.Is(err, ErrSyncInProgress) {
			slog.Warn("webhook sync failed", "project", id, "error", err)
		}
	}()
	return HookAccepted
}

// WaitHooks blocks until all webhook-triggered syncs have finished.
func (s *Service) WaitHooks() { s.hookWG.Wait() }
