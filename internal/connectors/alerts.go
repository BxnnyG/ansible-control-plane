package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Limits applied to every stored alert, whatever its source.
const (
	maxAlertKey     = 256
	maxAlertText    = 1000
	maxAlertShort   = 255
	maxAlertLabels  = 50
	maxAlertLabelKV = 500
)

// AlertLinker links stored alerts to merged hosts. *fleet.Store implements
// it; it runs after every merge and after webhook deliveries.
type AlertLinker interface {
	LinkAlerts(ctx context.Context, workspace uuid.UUID) error
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// normalizeAlert trims and bounds the text fields and checks the key and
// severity.
func normalizeAlert(a Alert) (Alert, error) {
	a.ExternalKey = clip(a.ExternalKey, maxAlertKey)
	if a.ExternalKey == "" {
		return a, errors.New("alert without key")
	}
	if !ValidSeverity(a.Severity) {
		return a, fmt.Errorf("alert %q has invalid severity %q", a.ExternalKey, a.Severity)
	}
	a.Host, a.IP, a.Service = clip(a.Host, maxAlertShort), clip(a.IP, maxAlertShort), clip(a.Service, maxAlertShort)
	a.Summary = clip(a.Summary, maxAlertText)
	if len(a.Labels) > 0 {
		out := make(map[string]string, min(len(a.Labels), maxAlertLabels))
		for k, v := range a.Labels {
			if len(out) >= maxAlertLabels {
				break
			}
			out[clip(k, maxAlertLabelKV)] = clip(v, maxAlertLabelKV)
		}
		a.Labels = out
	}
	return a, nil
}

func upsertAlert(ctx context.Context, q *dbgen.Queries, c dbgen.Connector, a Alert, now time.Time) error {
	labels := a.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	lb, err := json.Marshal(labels)
	if err != nil {
		return err
	}
	started := a.StartedAt
	if started.IsZero() || started.After(now) {
		started = now
	}
	if err := q.UpsertAlert(ctx, dbgen.UpsertAlertParams{WorkspaceID: c.WorkspaceID, ConnectorID: c.ID, ExternalKey: a.ExternalKey,
		HostName: a.Host, HostIp: a.IP, Service: a.Service, Severity: a.Severity, Summary: a.Summary, Labels: lb,
		StartedAt: ts(started), LastSeenAt: ts(now)}); err != nil {
		return fmt.Errorf("upsert alert: %w", err)
	}
	return nil
}

// persistAlerts stores the alerts of a pull sync in the sync transaction.
func persistAlerts(ctx context.Context, q *dbgen.Queries, c dbgen.Connector, res Result, now time.Time) error {
	keys := make([]string, 0, len(res.Alerts))
	for _, a := range res.Alerts {
		if err := upsertAlert(ctx, q, c, a, now); err != nil {
			return err
		}
		keys = append(keys, a.ExternalKey)
	}
	if res.ReplaceAlerts {
		if _, err := q.ResolveAlertsNotIn(ctx, dbgen.ResolveAlertsNotInParams{ConnectorID: c.ID, Column2: keys, ResolvedAt: ts(now)}); err != nil {
			return fmt.Errorf("resolve alerts: %w", err)
		}
	}
	return nil
}
