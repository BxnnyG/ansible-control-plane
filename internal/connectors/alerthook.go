package connectors

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// AlertWebhookType is the connector type of the generic alert receiver.
const AlertWebhookType = "alert_webhook"

// AlertHookPath returns the public receiver path of a webhook connector.
func AlertHookPath(id uuid.UUID) string { return "/api/hooks/alerts/" + id.String() }

// Webhook limits.
const (
	// MaxAlertWebhookBody is the largest accepted delivery.
	MaxAlertWebhookBody = 1 << 20
	maxWebhookAlerts    = 1000
)

// Webhook errors. ErrWebhookRejected covers unknown connector, wrong type,
// disabled connector and bad credentials alike so callers cannot tell them
// apart; ErrWebhookPayload is only returned after authentication.
var (
	ErrWebhookRejected = errors.New("alert webhook rejected")
	ErrWebhookPayload  = errors.New("invalid alert payload")
)

// AlertUpdate is one firing or resolved alert of a webhook delivery.
type AlertUpdate struct {
	Alert
	Resolved bool
}

type hookAlert struct {
	// ACP format.
	Key       string            `json:"key"`
	Host      string            `json:"host"`
	IP        string            `json:"ip"`
	Service   string            `json:"service"`
	Severity  string            `json:"severity"`
	Summary   string            `json:"summary"`
	StartedAt *time.Time        `json:"started_at"`
	Labels    map[string]string `json:"labels"`
	Status    string            `json:"status"`
	// Alertmanager format.
	Fingerprint string            `json:"fingerprint"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    *time.Time        `json:"startsAt"`
}

type hookBody struct {
	Version string      `json:"version"` // set by Alertmanager ("4")
	Alerts  []hookAlert `json:"alerts"`
}

// ParseAlertPayload decodes a delivery in the ACP format
// ({"alerts":[{key,host,ip,service,severity,summary,labels,status,started_at}]})
// or the Alertmanager webhook format (version 4).
func ParseAlertPayload(body []byte) ([]AlertUpdate, error) {
	var hb hookBody
	if err := json.Unmarshal(body, &hb); err != nil {
		return nil, fmt.Errorf("%w: malformed JSON", ErrWebhookPayload)
	}
	if len(hb.Alerts) == 0 || len(hb.Alerts) > maxWebhookAlerts {
		return nil, fmt.Errorf("%w: expected 1-%d alerts", ErrWebhookPayload, maxWebhookAlerts)
	}
	am := hb.Version != ""
	out := make([]AlertUpdate, 0, len(hb.Alerts))
	seen := map[string]int{}
	for i, h := range hb.Alerts {
		var u AlertUpdate
		var err error
		if am {
			u, err = fromAlertmanager(h)
		} else {
			u, err = fromACP(h)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: alert %d: %s", ErrWebhookPayload, i, err.Error())
		}
		if u, err = normalizeUpdate(u); err != nil {
			return nil, fmt.Errorf("%w: alert %d: %s", ErrWebhookPayload, i, err.Error())
		}
		if j, dup := seen[u.ExternalKey]; dup { // the later entry wins
			out[j] = u
			continue
		}
		seen[u.ExternalKey] = len(out)
		out = append(out, u)
	}
	return out, nil
}

func normalizeUpdate(u AlertUpdate) (AlertUpdate, error) {
	a, err := normalizeAlert(u.Alert)
	u.Alert = a
	return u, err
}

func fromACP(h hookAlert) (AlertUpdate, error) {
	u := AlertUpdate{Alert: Alert{ExternalKey: h.Key, Host: h.Host, IP: h.IP, Service: h.Service,
		Severity: strings.ToLower(strings.TrimSpace(h.Severity)), Summary: h.Summary, Labels: h.Labels}}
	if h.StartedAt != nil {
		u.StartedAt = *h.StartedAt
	}
	switch strings.ToLower(strings.TrimSpace(h.Status)) {
	case "", "firing":
	case "resolved":
		u.Resolved = true
	default:
		return u, fmt.Errorf("status must be firing or resolved")
	}
	if u.Severity == "" {
		u.Severity = SeverityUnknown
	}
	return u, nil
}

// alertmanagerSeverity maps common severity label values.
func alertmanagerSeverity(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "critical", "crit", "error", "page", "high", "disaster":
		return SeverityCritical
	case "warning", "warn", "medium", "average":
		return SeverityWarning
	case "info", "information", "none", "low":
		return SeverityInfo
	}
	return SeverityUnknown
}

func fromAlertmanager(h hookAlert) (AlertUpdate, error) {
	u := AlertUpdate{Alert: Alert{Labels: h.Labels, Service: h.Labels["alertname"],
		Severity: alertmanagerSeverity(h.Labels["severity"])}}
	key := h.Fingerprint
	if key == "" { // fall back to a hash of the label set
		keys := make([]string, 0, len(h.Labels))
		for k, v := range h.Labels {
			keys = append(keys, k+"="+v)
		}
		slices.Sort(keys)
		sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
		key = hex.EncodeToString(sum[:8])
	}
	u.ExternalKey = key
	u.Host, u.IP = hostFromLabels(h.Labels)
	u.Summary = firstNonEmpty(h.Annotations["summary"], h.Annotations["description"], h.Annotations["message"], u.Service)
	if h.StartsAt != nil && h.StartsAt.Year() > 1 {
		u.StartedAt = *h.StartsAt
	}
	switch strings.ToLower(strings.TrimSpace(h.Status)) {
	case "firing", "":
	case "resolved":
		u.Resolved = true
	default:
		return u, fmt.Errorf("status must be firing or resolved")
	}
	return u, nil
}

// hostFromLabels picks the host name or address from the usual Alertmanager
// labels (host, hostname, instance); a "host:port" instance is cut at the port.
func hostFromLabels(l map[string]string) (host, ip string) {
	v := firstNonEmpty(l["host"], l["hostname"], l["instance"])
	if v == "" {
		return "", ""
	}
	if ap, err := netip.ParseAddrPort(v); err == nil {
		return "", ap.Addr().String()
	}
	if a, err := netip.ParseAddr(strings.Trim(v, "[]")); err == nil {
		return "", a.String()
	}
	if h, _, ok := strings.Cut(v, ":"); ok && h != "" && strings.Count(v, ":") == 1 {
		v = h
	}
	if a, err := netip.ParseAddr(v); err == nil {
		return "", a.String()
	}
	return v, ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// bearerToken extracts the token of an "Authorization: Bearer <token>" value.
func bearerToken(h string) (string, bool) {
	scheme, tok, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

// SecretsEqual compares two secrets in constant time.
func SecretsEqual(a, b []byte) bool {
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// HandleAlertWebhook authenticates a delivery (bearer secret of the
// connector) and applies it: firing alerts are upserted, resolved alerts get
// resolved_at. It returns the number of alerts applied. Every authentication
// problem is ErrWebhookRejected; a payload problem is ErrWebhookPayload.
func (s *Service) HandleAlertWebhook(ctx context.Context, id uuid.UUID, authorization string, body []byte) (uuid.UUID, int, error) {
	c, err := s.q.GetConnectorByID(ctx, id)
	if err != nil || c.Type != AlertWebhookType || !c.Enabled || !c.SecretID.Valid {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, 0, fmt.Errorf("load connector: %w", err)
		}
		return uuid.Nil, 0, ErrWebhookRejected
	}
	tok, ok := bearerToken(authorization)
	if !ok {
		return uuid.Nil, 0, ErrWebhookRejected
	}
	res, err := s.o.Secrets.Resolve(ctx, c.WorkspaceID, c.SecretID.Bytes)
	if err != nil {
		return uuid.Nil, 0, ErrWebhookRejected
	}
	defer res.Value.Zero()
	if !SecretsEqual([]byte(tok), res.Value.Reveal()) {
		return uuid.Nil, 0, ErrWebhookRejected
	}
	updates, err := ParseAlertPayload(body)
	if err != nil {
		return c.WorkspaceID, 0, err
	}
	now := s.o.Now()
	tx, err := s.o.DB.Begin(ctx)
	if err != nil {
		return c.WorkspaceID, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	for _, u := range updates {
		if u.Resolved {
			if _, err := q.ResolveAlert(ctx, dbgen.ResolveAlertParams{ConnectorID: c.ID, ExternalKey: u.ExternalKey, ResolvedAt: ts(now)}); err != nil {
				return c.WorkspaceID, 0, fmt.Errorf("resolve alert: %w", err)
			}
			continue
		}
		if err := upsertAlert(ctx, q, c, u.Alert, now); err != nil {
			return c.WorkspaceID, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return c.WorkspaceID, 0, err
	}
	if s.o.Alerts != nil {
		if err := s.o.Alerts.LinkAlerts(ctx, c.WorkspaceID); err != nil {
			slog.Error("alert link after webhook failed", "connector", c.ID, "error", err)
		}
	}
	return c.WorkspaceID, len(updates), nil
}
