package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Alert states for the list filter.
const (
	AlertOpen     = "open"
	AlertResolved = "resolved"
)

// MaxAlertRows bounds one alert listing.
const MaxAlertRows = 1000

var _ connectors.AlertLinker = (*Store)(nil)

// linkHost is the matching identity of one merged host.
type linkHost struct {
	ID    uuid.UUID
	Names []string // lower-case name, hostname and FQDN
	IPs   []string // canonical addresses
}

func canonicalIP(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

func shortName(n string) string { return strings.SplitN(n, ".", 2)[0] }

func newLinkHost(id uuid.UUID, name string, f Fields) linkHost {
	h := linkHost{ID: id}
	seen := map[string]bool{}
	for _, n := range []string{name, f.Hostname, f.FQDN} {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !seen[n] {
			seen[n] = true
			h.Names = append(h.Names, n)
		}
	}
	for _, ip := range f.IPs {
		if c := canonicalIP(ip); c != "" {
			h.IPs = append(h.IPs, c)
		}
	}
	return h
}

func (h linkHost) hasIP(ip string) bool {
	for _, a := range h.IPs {
		if a == ip {
			return true
		}
	}
	return false
}

// matchHost links an alert to a host. Names are matched first (exact name,
// then short name against short name, case-insensitive), then the address.
// An ambiguous name is narrowed by the address; if that does not leave exactly
// one host, or the address itself is ambiguous, there is no link.
func matchHost(hosts []linkHost, name, ip string) (uuid.UUID, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	ip = canonicalIP(ip)
	var cands []linkHost
	if name != "" {
		exact := func(h linkHost) bool {
			for _, n := range h.Names {
				if n == name {
					return true
				}
			}
			return false
		}
		short := func(h linkHost) bool {
			for _, n := range h.Names {
				if shortName(n) == shortName(name) {
					return true
				}
			}
			return false
		}
		for _, tier := range []func(linkHost) bool{exact, short} {
			for _, h := range hosts {
				if tier(h) {
					cands = append(cands, h)
				}
			}
			if len(cands) > 0 {
				break
			}
		}
	}
	switch {
	case len(cands) == 1:
		return cands[0].ID, true
	case len(cands) > 1: // narrow by address
		if ip == "" {
			return uuid.Nil, false
		}
	default: // no name match: search all hosts by address
		if ip == "" {
			return uuid.Nil, false
		}
		cands = hosts
	}
	var found *linkHost
	for i := range cands {
		if cands[i].hasIP(ip) {
			if found != nil {
				return uuid.Nil, false
			}
			found = &cands[i]
		}
	}
	if found == nil {
		return uuid.Nil, false
	}
	return found.ID, true
}

// LinkAlerts (re)links every stored alert of the workspace to a merged host
// (connectors.AlertLinker). Only changed links are written.
func (s *Store) LinkAlerts(ctx context.Context, ws uuid.UUID) error {
	return linkAlerts(ctx, s.q, ws)
}

func linkAlerts(ctx context.Context, q *dbgen.Queries, ws uuid.UUID) error {
	alerts, err := q.ListAlertsForLink(ctx, ws)
	if err != nil {
		return fmt.Errorf("list alerts: %w", err)
	}
	if len(alerts) == 0 {
		return nil
	}
	rows, err := q.ListHosts(ctx, ws)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	hosts := make([]linkHost, len(rows))
	for i, r := range rows {
		var f Fields
		_ = json.Unmarshal(r.Fields, &f)
		hosts[i] = newLinkHost(r.ID, r.Name, f)
	}
	for _, a := range alerts {
		id, ok := matchHost(hosts, a.HostName, a.HostIp)
		switch {
		case ok && a.HostID.Valid && a.HostID.Bytes == id, !ok && !a.HostID.Valid:
			continue
		}
		next := pgtype.UUID{Bytes: id, Valid: ok}
		if err := q.SetAlertHost(ctx, dbgen.SetAlertHostParams{ID: a.ID, HostID: next}); err != nil {
			return fmt.Errorf("link alert: %w", err)
		}
	}
	return nil
}

var severityRank = map[string]int{connectors.SeverityInfo: 1, connectors.SeverityUnknown: 2, connectors.SeverityWarning: 3, connectors.SeverityCritical: 4}

func rankSeverity(rank int) string {
	for s, r := range severityRank {
		if r == rank {
			return s
		}
	}
	return ""
}

// alertSummaries returns the open alert count and worst severity per host.
func alertSummaries(ctx context.Context, q *dbgen.Queries, ws uuid.UUID) (map[uuid.UUID]dbgen.ListOpenAlertSummariesRow, error) {
	rows, err := q.ListOpenAlertSummaries(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("alert summaries: %w", err)
	}
	out := make(map[uuid.UUID]dbgen.ListOpenAlertSummariesRow, len(rows))
	for _, r := range rows {
		if r.HostID.Valid {
			out[r.HostID.Bytes] = r
		}
	}
	return out, nil
}

// AlertView is one stored alert.
type AlertView struct {
	ID            uuid.UUID         `json:"id"`
	ConnectorID   uuid.UUID         `json:"connector_id"`
	Connector     string            `json:"connector"`
	ConnectorType string            `json:"connector_type"`
	ExternalKey   string            `json:"external_key"`
	HostID        *uuid.UUID        `json:"host_id"`
	Host          string            `json:"host"`      // merged host name when linked
	HostName      string            `json:"host_name"` // as reported by the source
	HostIP        string            `json:"host_ip"`
	Service       string            `json:"service"`
	Severity      string            `json:"severity"`
	Summary       string            `json:"summary"`
	Labels        map[string]string `json:"labels"`
	StartedAt     time.Time         `json:"started_at"`
	LastSeenAt    time.Time         `json:"last_seen_at"`
	ResolvedAt    *time.Time        `json:"resolved_at"`
}

// AlertFilter narrows the alert list. State is "", open or resolved.
type AlertFilter struct {
	State       string
	HostID      *uuid.UUID
	ConnectorID *uuid.UUID
	ID          *uuid.UUID // a single alert
	// ExcludeUntracked hides alerts of hosts whose only networks are untracked.
	ExcludeUntracked bool
	Limit            int
}

// Alerts lists stored alerts, open ones first and worst severity first.
// Alerts linked to a host are visible when the host is; unlinked alerts are
// visible to unrestricted callers only.
func (s *Store) Alerts(ctx context.Context, ws uuid.UUID, f AlertFilter, visible Visible) ([]AlertView, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxAlertRows {
		limit = MaxAlertRows
	}
	p := dbgen.ListAlertsParams{WorkspaceID: ws, State: f.State, MaxRows: int32(limit)} //nolint:gosec // bounded
	if f.HostID != nil {
		p.HostID = pgtype.UUID{Bytes: *f.HostID, Valid: true}
	}
	if f.ConnectorID != nil {
		p.ConnectorID = pgtype.UUID{Bytes: *f.ConnectorID, Valid: true}
	}
	if f.ID != nil {
		p.ID = pgtype.UUID{Bytes: *f.ID, Valid: true}
	}
	rows, err := s.q.ListAlerts(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	infos, err := s.hostInfos(ctx, ws)
	if err != nil {
		return nil, err
	}
	var hidden func([]string) bool
	if f.ExcludeUntracked {
		if hidden, err = s.untrackedFilter(ctx, ws); err != nil {
			return nil, err
		}
	}
	out := make([]AlertView, 0, len(rows))
	for _, r := range rows {
		v := AlertView{ID: r.ID, ConnectorID: r.ConnectorID, Connector: r.ConnectorName, ConnectorType: r.ConnectorType,
			ExternalKey: r.ExternalKey, HostName: r.HostName, HostIP: r.HostIp, Service: r.Service, Severity: r.Severity,
			Summary: r.Summary, Labels: map[string]string{}, StartedAt: r.StartedAt.Time.UTC(), LastSeenAt: r.LastSeenAt.Time.UTC()}
		_ = json.Unmarshal(r.Labels, &v.Labels)
		if r.ResolvedAt.Valid {
			t := r.ResolvedAt.Time.UTC()
			v.ResolvedAt = &t
		}
		if r.HostID.Valid {
			id := uuid.UUID(r.HostID.Bytes)
			info, ok := infos[id]
			if !ok || !visible.ok(info.ips) || (hidden != nil && hidden(info.ips)) {
				continue
			}
			v.HostID, v.Host = &id, info.name
		} else if visible != nil {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}
