package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// Store persists merge results (hosts, host_keys, host_conflicts).
type Store struct {
	db  *pgxpool.Pool
	q   *dbgen.Queries
	now func() time.Time
}

// NewStore builds a store; now may be nil (time.Now).
func NewStore(db *pgxpool.Pool, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, q: dbgen.New(db), now: now}
}

// Summary describes one persisted merge.
type Summary struct {
	Hosts, Created, Deleted                           int
	ConflictsOpen, ConflictsOpened, ConflictsResolved int
}

// AfterSync implements connectors.PostSync: it re-merges the workspace.
func (s *Store) AfterSync(ctx context.Context, ws, _ uuid.UUID) error {
	_, err := s.Merge(ctx, ws)
	return err
}

var _ connectors.PostSync = (*Store)(nil)

// priorityOf reads the optional "priority" config key of a connector.
func priorityOf(cfg []byte) (int, bool) {
	var m map[string]any
	if json.Unmarshal(cfg, &m) != nil {
		return 0, false
	}
	switch n := m[connectors.CfgPriority].(type) {
	case float64:
		return int(n), n == float64(int(n))
	case int:
		return n, true
	}
	return 0, false
}

func (s *Store) load(ctx context.Context, q *dbgen.Queries, ws uuid.UUID) ([]Observation, Priority, error) {
	conns, err := q.ListConnectors(ctx, ws)
	if err != nil {
		return nil, Priority{}, fmt.Errorf("list connectors: %w", err)
	}
	prio := DefaultPriority()
	prio.ByConnector = map[uuid.UUID]int{}
	byID := map[uuid.UUID]dbgen.Connector{}
	for _, c := range conns {
		byID[c.ID] = c
		if p, ok := priorityOf(c.Config); ok {
			prio.ByConnector[c.ID] = p
		}
	}
	rows, err := q.ListObservations(ctx, ws)
	if err != nil {
		return nil, Priority{}, fmt.Errorf("list observations: %w", err)
	}
	obs := make([]Observation, 0, len(rows))
	for _, r := range rows {
		c, ok := byID[r.ConnectorID]
		if !ok {
			continue
		}
		var f connectors.Fields
		if err := json.Unmarshal(r.Fields, &f); err != nil {
			return nil, Priority{}, fmt.Errorf("decode observation %s: %w", r.ID, err)
		}
		obs = append(obs, Observation{ConnectorID: c.ID, ConnectorName: c.Name, ConnectorType: c.Type, ExternalKey: r.ExternalKey,
			Kind: r.Kind, Fields: f, FirstSeen: r.FirstSeen.Time, LastSeen: r.LastSeen.Time})
	}
	return obs, prio, nil
}

// Merge re-merges the workspace's observations and writes hosts, keys and
// conflicts in one transaction. Concurrent merges of a workspace serialize.
func (s *Store) Merge(ctx context.Context, ws uuid.UUID) (Summary, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := q.LockFleetMerge(ctx, ws.String()); err != nil {
		return Summary{}, fmt.Errorf("lock merge: %w", err)
	}
	now := s.now()
	obs, prio, err := s.load(ctx, q, ws)
	if err != nil {
		return Summary{}, err
	}
	res := Merge(obs, prio, now)
	sum, err := s.write(ctx, q, ws, res, now)
	if err != nil {
		return Summary{}, err
	}
	return sum, tx.Commit(ctx)
}

type existingHost struct {
	row  dbgen.Host
	keys map[Key]bool
}

// assignIDs maps every merged host to an existing host id or a new one. Per
// key kind (MAC, then name, then IP) a merged host takes the oldest unclaimed
// existing host sharing a key of that kind. Returns the ids and the claimed
// existing ids.
func assignIDs(hosts []Host, existing []existingHost) ([]uuid.UUID, map[uuid.UUID]bool) {
	sort.SliceStable(existing, func(i, j int) bool {
		a, b := existing[i].row, existing[j].row
		if !a.FirstSeen.Time.Equal(b.FirstSeen.Time) {
			return a.FirstSeen.Time.Before(b.FirstSeen.Time)
		}
		return a.ID.String() < b.ID.String()
	})
	ids := make([]uuid.UUID, len(hosts))
	claimed := map[uuid.UUID]bool{}
	for _, kind := range []string{KeyMAC, KeyName, KeyIP} {
		for i, h := range hosts {
			if ids[i] != uuid.Nil {
				continue
			}
		scan:
			for _, e := range existing {
				if claimed[e.row.ID] {
					continue
				}
				for _, k := range h.Keys {
					if k.Kind == kind && e.keys[k] {
						ids[i] = e.row.ID
						claimed[e.row.ID] = true
						break scan
					}
				}
			}
		}
	}
	for i := range ids {
		if ids[i] == uuid.Nil {
			ids[i] = uuid.New()
		}
	}
	return ids, claimed
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func (s *Store) write(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, res Result, now time.Time) (Summary, error) {
	rows, err := q.ListHosts(ctx, ws)
	if err != nil {
		return Summary{}, fmt.Errorf("list hosts: %w", err)
	}
	keyRows, err := q.ListHostKeys(ctx, ws)
	if err != nil {
		return Summary{}, fmt.Errorf("list host keys: %w", err)
	}
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
	ids, claimed := assignIDs(res.Hosts, existing)

	var sum Summary
	for i, h := range res.Hosts {
		fb, err := json.Marshal(h.Fields)
		if err != nil {
			return sum, err
		}
		if claimed[ids[i]] {
			err = q.UpdateHost(ctx, dbgen.UpdateHostParams{ID: ids[i], WorkspaceID: ws, Name: h.Name, Fields: fb,
				FirstSeen: ts(h.FirstSeen), LastSeen: ts(h.LastSeen)})
			if err == nil {
				err = q.DeleteHostKeys(ctx, ids[i])
			}
		} else {
			sum.Created++
			err = q.InsertHost(ctx, dbgen.InsertHostParams{ID: ids[i], WorkspaceID: ws, Name: h.Name, Fields: fb,
				FirstSeen: ts(h.FirstSeen), LastSeen: ts(h.LastSeen)})
		}
		if err != nil {
			return sum, fmt.Errorf("write host: %w", err)
		}
		keys := make([]string, len(h.Keys))
		for j, k := range h.Keys {
			keys[j] = k.String()
		}
		if err := q.InsertHostKeys(ctx, dbgen.InsertHostKeysParams{HostID: ids[i], WorkspaceID: ws, Column3: keys}); err != nil {
			return sum, fmt.Errorf("write host keys: %w", err)
		}
	}
	sum.Hosts = len(res.Hosts)
	// Hosts without any remaining observation are deleted (ADR-0018).
	for _, e := range existing {
		if !claimed[e.row.ID] {
			if err := q.DeleteHost(ctx, dbgen.DeleteHostParams{ID: e.row.ID, WorkspaceID: ws}); err != nil {
				return sum, fmt.Errorf("delete host: %w", err)
			}
			sum.Deleted++
		}
	}
	return s.writeConflicts(ctx, q, ws, res, ids, now, sum)
}

func (s *Store) writeConflicts(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, res Result, ids []uuid.UUID, now time.Time, sum Summary) (Summary, error) {
	old, err := q.ListHostConflicts(ctx, ws)
	if err != nil {
		return sum, fmt.Errorf("list conflicts: %w", err)
	}
	byFP := map[string]dbgen.HostConflict{}
	for _, c := range old {
		byFP[c.Fingerprint] = c
	}
	current := map[string]bool{}
	for _, c := range res.Conflicts {
		current[c.Fingerprint] = true
		hostIDs := make([]uuid.UUID, len(c.Hosts))
		for i, h := range c.Hosts {
			hostIDs[i] = ids[h]
		}
		details, err := json.Marshal(c.Details)
		if err != nil {
			return sum, err
		}
		prev, ok := byFP[c.Fingerprint]
		switch {
		case !ok:
			sum.ConflictsOpened++
			err = q.InsertHostConflict(ctx, dbgen.InsertHostConflictParams{WorkspaceID: ws, Type: c.Type, Fingerprint: c.Fingerprint,
				HostIds: hostIDs, Details: details, FirstSeen: ts(now)})
		case prev.State == "resolved":
			sum.ConflictsOpened++
			err = q.ReopenHostConflict(ctx, dbgen.ReopenHostConflictParams{ID: prev.ID, HostIds: hostIDs, Details: details, FirstSeen: ts(now)})
		default:
			err = q.RefreshHostConflict(ctx, dbgen.RefreshHostConflictParams{ID: prev.ID, HostIds: hostIDs, Details: details, LastSeen: ts(now)})
		}
		if err != nil {
			return sum, fmt.Errorf("write conflict: %w", err)
		}
	}
	sum.ConflictsOpen = len(res.Conflicts)
	for _, c := range old {
		if c.State == "open" && !current[c.Fingerprint] {
			if err := q.ResolveHostConflict(ctx, dbgen.ResolveHostConflictParams{ID: c.ID, ResolvedAt: ts(now)}); err != nil {
				return sum, fmt.Errorf("resolve conflict: %w", err)
			}
			sum.ConflictsResolved++
		}
	}
	return sum, nil
}
