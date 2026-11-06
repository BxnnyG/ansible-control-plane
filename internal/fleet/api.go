package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Read API errors.
var (
	ErrNotFound = errors.New("not found")
)

// Visible decides from a host's merged addresses whether the caller may see
// it (network scopes). A nil Visible allows everything.
type Visible func(ips []string) bool

func (v Visible) ok(ips []string) bool { return v == nil || v(ips) }

func optTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// NetworkView is a connector-reported network.
type NetworkView struct {
	ID            uuid.UUID `json:"id"`
	ConnectorID   uuid.UUID `json:"connector_id"`
	ConnectorName string    `json:"connector"`
	ExternalKey   string    `json:"external_key"`
	CIDR          string    `json:"cidr"`
	VLAN          *int      `json:"vlan"`
	Interface     string    `json:"interface"`
	Description   string    `json:"description"`
	Source        string    `json:"source"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	HostCount     int       `json:"host_count"`
	// Counts are the status counters of the visible hosts in the network.
	Counts NetworkCounts `json:"counts"`
}

// Networks lists the workspace networks that allow accepts (nil allows all).
// HostCount counts the merged hosts with an address in the network that
// visible allows (nil allows all).
func (s *Store) Networks(ctx context.Context, ws uuid.UUID, allow func(id uuid.UUID, cidr netip.Prefix) bool, visible Visible) ([]NetworkView, error) {
	rows, err := s.q.ListNetworks(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	entries, err := s.hostEntries(ctx, ws, true)
	if err != nil {
		return nil, err
	}
	conns, err := s.q.ListConnectors(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	names := map[uuid.UUID]string{}
	for _, c := range conns {
		names[c.ID] = c.Name
	}
	out := make([]NetworkView, 0, len(rows))
	for _, n := range rows {
		if allow != nil && !allow(n.ID, n.Cidr) {
			continue
		}
		v := NetworkView{ID: n.ID, ConnectorID: n.ConnectorID, ConnectorName: names[n.ConnectorID], ExternalKey: n.ExternalKey,
			CIDR: n.Cidr.String(), Interface: n.Interface, Description: n.Description, Source: n.Source,
			FirstSeen: n.FirstSeen.Time.UTC(), LastSeen: n.LastSeen.Time.UTC()}
		if n.Vlan.Valid {
			vl := int(n.Vlan.Int32)
			v.VLAN = &vl
		}
		for _, e := range entries {
			if visible.ok(e.view.IPs) && hasIPIn(e.view.IPs, n.Cidr) {
				v.HostCount++
				v.Counts.count(e.view)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// HostView is the list representation of a merged host.
type HostView struct {
	ID            uuid.UUID         `json:"id"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	Hostname      string            `json:"hostname"`
	FQDN          string            `json:"fqdn"`
	IPs           []string          `json:"ips"`
	MACs          []string          `json:"macs"`
	Tags          []string          `json:"tags"`
	Attrs         map[string]string `json:"attrs,omitempty"`
	Connectors    []string          `json:"connectors"`
	OpenConflicts int               `json:"open_conflicts"`
	// OpenAlerts counts open monitoring alerts linked to the host;
	// WorstSeverity is the highest severity among them ("" without alerts).
	OpenAlerts    int       `json:"open_alerts"`
	WorstSeverity string    `json:"worst_severity"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`

	// Host status (FLEET-4), derived on read. Online is online, offline or
	// unknown (no live-state source). Ansible lists the linked inventory
	// hosts; Managed is true when there is one. LastRun is the newest finished
	// run on any linked host; Reachable is yes, no (that run hit the host as
	// unreachable) or unknown (never run). Drift is set when the newest
	// check-mode run reported changes.
	Online    string        `json:"online"`
	Managed   bool          `json:"managed"`
	Ansible   []AnsibleLink `json:"ansible"`
	LastRun   *RunRef       `json:"last_run"`
	Reachable string        `json:"reachable"`
	Drift     *Drift        `json:"drift"`

	checkAt time.Time
}

// ObservationView is one connector observation behind a host.
type ObservationView struct {
	ConnectorID   uuid.UUID         `json:"connector_id"`
	Connector     string            `json:"connector"`
	ConnectorType string            `json:"connector_type"`
	ExternalKey   string            `json:"external_key"`
	Kind          string            `json:"kind"`
	Fields        connectors.Fields `json:"fields"`
	FirstSeen     time.Time         `json:"first_seen"`
	LastSeen      time.Time         `json:"last_seen"`
}

// HostDetail is one host with per-field sources, keys, observations and its
// conflicts.
type HostDetail struct {
	HostView
	Fields       Fields            `json:"fields"`
	Keys         []Key             `json:"keys"`
	Observations []ObservationView `json:"observations"`
	Conflicts    []ConflictView    `json:"conflicts"`
}

// HostFilter narrows the host list.
type HostFilter struct {
	NetworkID *uuid.UUID // hosts with an address in this network
	Q         string     // case-insensitive substring of name, address, MAC or tag
	Conflicts bool       // only hosts with an open conflict
	Alerts    bool       // only hosts with an open alert
	// Managed narrows to hosts linked (true) or not linked (false) to an
	// inventory host of the workspace.
	Managed *bool
	// SkipStatus leaves the Ansible status fields empty (cheaper; for
	// callers that only need identities).
	SkipStatus bool
}

// ConflictView is a conflict hint. State is open, acknowledged or resolved.
type ConflictView struct {
	ID             uuid.UUID       `json:"id"`
	Type           string          `json:"type"`
	State          string          `json:"state"`
	Fingerprint    string          `json:"fingerprint"`
	HostIDs        []uuid.UUID     `json:"host_ids"`
	Hosts          []ConflictHost  `json:"hosts"`
	Details        json.RawMessage `json:"details"`
	AcknowledgedBy *uuid.UUID      `json:"acknowledged_by"`
	AcknowledgedAt *time.Time      `json:"acknowledged_at"`
	FirstSeen      time.Time       `json:"first_seen"`
	LastSeen       time.Time       `json:"last_seen"`
	ResolvedAt     *time.Time      `json:"resolved_at"`
}

// ConflictHost names a host referenced by a conflict.
type ConflictHost struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// hostInfo is the display name and merged addresses of a host.
type hostInfo struct {
	name string
	ips  []string
}

// withHosts fills Hosts with the conflict's hosts that visible allows.
func (v ConflictView) withHosts(info map[uuid.UUID]hostInfo, visible Visible) ConflictView {
	v.Hosts = []ConflictHost{}
	for _, id := range v.HostIDs {
		if h, ok := info[id]; ok && visible.ok(h.ips) {
			v.Hosts = append(v.Hosts, ConflictHost{ID: id, Name: h.name})
		}
	}
	return v
}

func conflictView(c dbgen.HostConflict) ConflictView {
	state := c.State
	if state == "open" && c.AcknowledgedAt.Valid {
		state = "acknowledged"
	}
	v := ConflictView{ID: c.ID, Type: c.Type, State: state, Fingerprint: c.Fingerprint, HostIDs: c.HostIds, Details: c.Details,
		AcknowledgedAt: optTime(c.AcknowledgedAt), FirstSeen: c.FirstSeen.Time.UTC(), LastSeen: c.LastSeen.Time.UTC(),
		ResolvedAt: optTime(c.ResolvedAt)}
	if v.HostIDs == nil {
		v.HostIDs = []uuid.UUID{}
	}
	if len(v.Details) == 0 {
		v.Details = json.RawMessage("{}")
	}
	if c.AcknowledgedBy.Valid {
		id := uuid.UUID(c.AcknowledgedBy.Bytes)
		v.AcknowledgedBy = &id
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (v *HostView) setAlerts(sums map[uuid.UUID]dbgen.ListOpenAlertSummariesRow) {
	if r, ok := sums[v.ID]; ok {
		v.OpenAlerts, v.WorstSeverity = int(r.OpenCount), rankSeverity(int(r.WorstRank))
	}
}

func hostView(h dbgen.Host, open int) (HostView, Fields) {
	var f Fields
	_ = json.Unmarshal(h.Fields, &f)
	seen := map[string]bool{}
	var conns []string
	for _, ss := range f.Sources {
		for _, src := range ss {
			if !seen[src.ConnectorName] {
				seen[src.ConnectorName] = true
				conns = append(conns, src.ConnectorName)
			}
		}
	}
	slices.Sort(conns)
	return HostView{ID: h.ID, Name: h.Name, Kind: f.Kind, Status: f.Status, Hostname: f.Hostname, FQDN: f.FQDN,
		IPs: nonNil(f.IPs), MACs: nonNil(f.MACs), Tags: nonNil(f.Tags), Attrs: f.Attrs, Connectors: nonNil(conns), OpenConflicts: open,
		FirstSeen: h.FirstSeen.Time.UTC(), LastSeen: h.LastSeen.Time.UTC()}, f
}

// openConflicts counts open (acknowledged or not) conflicts per host.
func openConflicts(list []dbgen.HostConflict) map[uuid.UUID]int {
	out := map[uuid.UUID]int{}
	for _, c := range list {
		if c.State != "open" {
			continue
		}
		for _, id := range c.HostIds {
			out[id]++
		}
	}
	return out
}

// hostEntry is a host view together with its merged fields.
type hostEntry struct {
	view   HostView
	fields Fields
}

// hostEntries loads every host of the workspace with conflict, alert and
// (when withStatus) Ansible status, in a fixed number of queries.
func (s *Store) hostEntries(ctx context.Context, ws uuid.UUID, withStatus bool) ([]hostEntry, error) {
	rows, err := s.q.ListHosts(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	conflicts, err := s.q.ListHostConflicts(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list conflicts: %w", err)
	}
	open := openConflicts(conflicts)
	sums, err := alertSummaries(ctx, s.q, ws)
	if err != nil {
		return nil, err
	}
	var sd *statusData
	if withStatus {
		keys, err := s.q.ListHostKeys(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("list host keys: %w", err)
		}
		if sd, err = s.loadStatus(ctx, ws, rows, keys); err != nil {
			return nil, err
		}
	}
	now := s.now()
	out := make([]hostEntry, 0, len(rows))
	for _, h := range rows {
		v, f := hostView(h, open[h.ID])
		v.setAlerts(sums)
		sd.apply(&v, f, now)
		out = append(out, hostEntry{view: v, fields: f})
	}
	return out, nil
}

// Hosts lists merged hosts, filtered and limited to what visible allows.
func (s *Store) Hosts(ctx context.Context, ws uuid.UUID, f HostFilter, visible Visible) ([]HostView, error) {
	var prefix *netip.Prefix
	if f.NetworkID != nil {
		nets, err := s.q.ListNetworks(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("list networks: %w", err)
		}
		for _, n := range nets {
			if n.ID == *f.NetworkID {
				p := n.Cidr
				prefix = &p
			}
		}
		if prefix == nil {
			return nil, ErrNotFound
		}
	}
	entries, err := s.hostEntries(ctx, ws, !f.SkipStatus || f.Managed != nil)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(f.Q))
	out := []HostView{}
	for _, e := range entries {
		v := e.view
		if !visible.ok(v.IPs) {
			continue
		}
		if f.Alerts && v.OpenAlerts == 0 {
			continue
		}
		if f.Conflicts && v.OpenConflicts == 0 {
			continue
		}
		if f.Managed != nil && v.Managed != *f.Managed {
			continue
		}
		if prefix != nil && !hasIPIn(v.IPs, *prefix) {
			continue
		}
		if q != "" && !matchesQuery(v, q) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func hasIPIn(ips []string, p netip.Prefix) bool {
	for _, s := range ips {
		if a, err := netip.ParseAddr(s); err == nil && p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func matchesQuery(v HostView, q string) bool {
	hay := []string{v.Name, v.Hostname, v.FQDN}
	hay = append(append(append(hay, v.IPs...), v.MACs...), v.Tags...)
	for _, s := range hay {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// Host returns one host with its sources, keys, observations and conflicts.
// Hosts outside visible are reported as ErrNotFound.
func (s *Store) Host(ctx context.Context, ws, id uuid.UUID, visible Visible) (HostDetail, error) {
	rows, err := s.q.ListHosts(ctx, ws)
	if err != nil {
		return HostDetail{}, fmt.Errorf("list hosts: %w", err)
	}
	idx := slices.IndexFunc(rows, func(h dbgen.Host) bool { return h.ID == id })
	if idx < 0 {
		return HostDetail{}, ErrNotFound
	}
	conflicts, err := s.q.ListHostConflicts(ctx, ws)
	if err != nil {
		return HostDetail{}, fmt.Errorf("list conflicts: %w", err)
	}
	view, fields := hostView(rows[idx], openConflicts(conflicts)[id])
	if !visible.ok(view.IPs) {
		return HostDetail{}, ErrNotFound
	}
	sums, err := alertSummaries(ctx, s.q, ws)
	if err != nil {
		return HostDetail{}, err
	}
	view.setAlerts(sums)
	keyRows, err := s.q.ListHostKeys(ctx, ws)
	if err != nil {
		return HostDetail{}, fmt.Errorf("list host keys: %w", err)
	}
	sd, err := s.loadStatus(ctx, ws, rows, keyRows)
	if err != nil {
		return HostDetail{}, err
	}
	sd.apply(&view, fields, s.now())
	d := HostDetail{HostView: view, Fields: fields, Keys: []Key{}, Observations: []ObservationView{}, Conflicts: []ConflictView{}}
	for _, k := range keyRows {
		if k.HostID == id {
			d.Keys = append(d.Keys, Key{Kind: k.Kind, Value: k.Value})
		}
	}
	for _, c := range conflicts {
		if slices.Contains(c.HostIds, id) {
			d.Conflicts = append(d.Conflicts, conflictView(c))
		}
	}
	d.Observations, err = s.observationsOf(ctx, ws, rows, keyRows, id)
	if err != nil {
		return HostDetail{}, err
	}
	return d, nil
}

// observationsOf re-runs the (pure) merge and returns the observations of the
// merged host that maps to the stored host id.
func (s *Store) observationsOf(ctx context.Context, ws uuid.UUID, rows []dbgen.Host, keyRows []dbgen.HostKey, id uuid.UUID) ([]ObservationView, error) {
	obs, prio, err := s.load(ctx, s.q, ws)
	if err != nil {
		return nil, err
	}
	res := Merge(obs, prio, s.now())
	existing := make([]existingHost, len(rows))
	pos := map[uuid.UUID]int{}
	for i, r := range rows {
		existing[i] = existingHost{row: r, keys: map[Key]bool{}}
		pos[r.ID] = i
	}
	for _, k := range keyRows {
		if i, ok := pos[k.HostID]; ok {
			existing[i].keys[Key{Kind: k.Kind, Value: k.Value}] = true
		}
	}
	ids, _ := assignIDs(res.Hosts, existing)
	at := slices.Index(ids, id)
	out := []ObservationView{}
	if at < 0 {
		return out, nil
	}
	byRef := map[MemberRef]Observation{}
	for _, o := range obs {
		byRef[MemberRef{ConnectorID: o.ConnectorID, ExternalKey: o.ExternalKey}] = o
	}
	for _, m := range res.Hosts[at].Members {
		o, ok := byRef[m]
		if !ok {
			continue
		}
		out = append(out, ObservationView{ConnectorID: o.ConnectorID, Connector: o.ConnectorName, ConnectorType: o.ConnectorType,
			ExternalKey: o.ExternalKey, Kind: o.Kind, Fields: o.Fields, FirstSeen: o.FirstSeen.UTC(), LastSeen: o.LastSeen.UTC()})
	}
	return out, nil
}

// Conflicts states accepted by Conflicts.
const (
	StateOpen         = "open"
	StateAcknowledged = "acknowledged"
	StateResolved     = "resolved"
)

// hostsVisible reports whether every host of the conflict is visible. A
// conflict without hosts is visible to unrestricted callers only.
func hostsVisible(ids []uuid.UUID, ips map[uuid.UUID][]string, visible Visible) bool {
	if visible == nil {
		return true
	}
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !visible(ips[id]) {
			return false
		}
	}
	return true
}

func (s *Store) hostInfos(ctx context.Context, ws uuid.UUID) (map[uuid.UUID]hostInfo, error) {
	rows, err := s.q.ListHosts(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	out := make(map[uuid.UUID]hostInfo, len(rows))
	for _, h := range rows {
		v, _ := hostView(h, 0)
		out[h.ID] = hostInfo{name: v.Name, ips: v.IPs}
	}
	return out, nil
}

// Conflicts lists conflicts; state is "", open, acknowledged or resolved.
// A conflict is visible when all its hosts are.
func (s *Store) Conflicts(ctx context.Context, ws uuid.UUID, state string, visible Visible) ([]ConflictView, error) {
	rows, err := s.q.ListHostConflicts(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list conflicts: %w", err)
	}
	info, err := s.hostInfos(ctx, ws)
	if err != nil {
		return nil, err
	}
	ips := make(map[uuid.UUID][]string, len(info))
	for id, h := range info {
		ips[id] = h.ips
	}
	out := []ConflictView{}
	for _, c := range rows {
		v := conflictView(c)
		if state != "" && v.State != state {
			continue
		}
		if hostsVisible(c.HostIds, ips, visible) {
			out = append(out, v.withHosts(info, visible))
		}
	}
	return out, nil
}

// Acknowledge marks an open conflict as acknowledged by the user. Resolved,
// unknown and invisible conflicts yield ErrNotFound. Acknowledging again
// refreshes the acknowledgement.
func (s *Store) Acknowledge(ctx context.Context, ws, id, user uuid.UUID, visible Visible) (ConflictView, error) {
	rows, err := s.q.ListHostConflicts(ctx, ws)
	if err != nil {
		return ConflictView{}, fmt.Errorf("list conflicts: %w", err)
	}
	idx := slices.IndexFunc(rows, func(c dbgen.HostConflict) bool { return c.ID == id })
	if idx < 0 || rows[idx].State != "open" {
		return ConflictView{}, ErrNotFound
	}
	info, err := s.hostInfos(ctx, ws)
	if err != nil {
		return ConflictView{}, err
	}
	ips := make(map[uuid.UUID][]string, len(info))
	for id, h := range info {
		ips[id] = h.ips
	}
	if !hostsVisible(rows[idx].HostIds, ips, visible) {
		return ConflictView{}, ErrNotFound
	}
	n, err := s.q.AcknowledgeHostConflict(ctx, dbgen.AcknowledgeHostConflictParams{ID: id, WorkspaceID: ws,
		AcknowledgedBy: pgtype.UUID{Bytes: user, Valid: true}, AcknowledgedAt: ts(s.now())})
	if err != nil {
		return ConflictView{}, fmt.Errorf("acknowledge conflict: %w", err)
	}
	if n == 0 {
		return ConflictView{}, ErrNotFound
	}
	rows, err = s.q.ListHostConflicts(ctx, ws)
	if err != nil {
		return ConflictView{}, fmt.Errorf("list conflicts: %w", err)
	}
	for _, c := range rows {
		if c.ID == id {
			return conflictView(c).withHosts(info, visible), nil
		}
	}
	return ConflictView{}, ErrNotFound
}
