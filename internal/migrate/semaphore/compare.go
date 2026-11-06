package semaphore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// CompareRow compares one object kind: how many objects the source holds and
// how many of them are mapped and still present in ACP.
type CompareRow struct {
	Kind    string   `json:"kind"`
	Source  int      `json:"source"`
	Mapped  int      `json:"mapped"`
	Present int      `json:"present"`
	Missing []string `json:"missing,omitempty"`
}

// CheckRun is one queued check-mode run of a migrated template.
type CheckRun struct {
	Template string `json:"template"`
	SourceID string `json:"source_id"`
	RunID    string `json:"run_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CompareResult is the outcome of Compare.
type CompareResult struct {
	Workspace string       `json:"workspace"`
	Rows      []CompareRow `json:"rows"`
	Match     bool         `json:"match"`
	CheckRuns []CheckRun   `json:"check_runs,omitempty"`
}

type srcObj struct {
	id     int64
	name   string
	reason string
}

// Compare counts source objects against the mapped and present ACP objects.
// rep must come from Map on the same snapshot. With checkRuns, one check-mode
// run per migrated template is queued (not awaited).
func (a *Applier) Compare(ctx context.Context, snap *Snapshot, rep *Report, checkRuns bool) (*CompareResult, error) {
	present, err := a.presentIDs(ctx)
	if err != nil {
		return nil, err
	}
	fromItems := func(items []Item, keep func(Item) bool) []srcObj {
		var out []srcObj
		for _, it := range items {
			if !keep(it) {
				continue
			}
			id, _ := strconv.ParseInt(it.SourceID, 10, 64)
			o := srcObj{id: id, name: it.Name}
			if it.Status == StatusUnmappable {
				o.reason = it.Reason
			}
			out = append(out, o)
		}
		return out
	}
	everything := func(Item) bool { return true }
	secretStatus := map[int64]string{}
	for _, it := range rep.Secrets {
		id, _ := strconv.ParseInt(it.SourceID, 10, 64)
		secretStatus[id] = it.Status
	}
	var vaults, invs []srcObj
	seen := map[int64]bool{}
	for _, tv := range snap.TemplateVault {
		if tv.VaultKeyID == nil || seen[*tv.VaultKeyID] {
			continue
		}
		seen[*tv.VaultKeyID] = true
		if st := secretStatus[*tv.VaultKeyID]; st == StatusSkipped {
			continue
		}
		o := srcObj{id: *tv.VaultKeyID, name: tv.Name}
		if secretStatus[*tv.VaultKeyID] == StatusUnmappable {
			o.reason = "its key cannot be migrated"
		}
		vaults = append(vaults, o)
	}
	for _, i := range snap.Inventories {
		if isStaticType(i.Type) {
			invs = append(invs, srcObj{id: i.ID, name: i.Name})
		}
	}
	res := &CompareResult{Workspace: a.Slug, Match: true}
	for _, k := range []struct {
		kind string
		objs []srcObj
		have map[uuid.UUID]bool
	}{
		{KindSecret, fromItems(rep.Secrets, func(it Item) bool { return it.Status != StatusSkipped }), present[KindSecret]},
		{KindVault, vaults, present[KindVault]},
		{KindInventory, invs, present[KindInventory]},
		{KindTemplate, fromItems(rep.Templates, everything), present[KindTemplate]},
		{KindSchedule, fromItems(rep.Schedules, everything), present[KindSchedule]},
	} {
		row := CompareRow{Kind: k.kind, Source: len(k.objs)}
		for _, o := range k.objs {
			id, ok := a.mapped(k.kind, o.id)
			if ok {
				row.Mapped++
			}
			if ok && k.have[id] {
				row.Present++
				continue
			}
			why := "not imported"
			switch {
			case o.reason != "":
				why = o.reason
			case ok:
				why = "mapped, but gone from ACP"
			}
			row.Missing = append(row.Missing, fmt.Sprintf("#%d %s (%s)", o.id, o.name, why))
		}
		if row.Present != row.Source {
			res.Match = false
		}
		res.Rows = append(res.Rows, row)
	}
	if checkRuns {
		a.queueChecks(ctx, snap, res)
	}
	return res, nil
}

func (a *Applier) mapped(kind string, id int64) (uuid.UUID, bool) {
	return (&applyRun{Applier: a}).mapped(kind, id)
}

// presentIDs lists the ACP object ids that exist, per mapping kind.
func (a *Applier) presentIDs(ctx context.Context) (map[string]map[uuid.UUID]bool, error) {
	out := map[string]map[uuid.UUID]bool{
		KindSecret: {}, KindVault: {}, KindInventory: {}, KindTemplate: {}, KindSchedule: {},
	}
	secs, err := a.Q.ListSecretsMeta(ctx, a.Workspace)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	for _, s := range secs {
		out[KindSecret][s.ID] = true
	}
	vs, err := a.Q.ListVaultIdentities(ctx, a.Workspace)
	if err != nil {
		return nil, fmt.Errorf("list vault identities: %w", err)
	}
	for _, v := range vs {
		out[KindVault][v.ID] = true
	}
	is, err := a.Q.ListInventories(ctx, a.Workspace)
	if err != nil {
		return nil, fmt.Errorf("list inventories: %w", err)
	}
	for _, i := range is {
		out[KindInventory][i.ID] = true
	}
	ts, err := a.Q.ListTemplates(ctx, a.Workspace)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	for _, t := range ts {
		out[KindTemplate][t.ID] = true
	}
	ss, err := a.Q.ListSchedules(ctx, a.Workspace)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	for _, s := range ss {
		out[KindSchedule][s.ID] = true
	}
	return out, nil
}

// queueChecks enqueues one check-mode run per migrated template. Runs are
// not awaited; failures are listed per template.
func (a *Applier) queueChecks(ctx context.Context, snap *Snapshot, res *CompareResult) {
	tpls := append([]Template(nil), snap.Templates...)
	sort.Slice(tpls, func(i, j int) bool { return tpls[i].ID < tpls[j].ID })
	check := true
	for _, t := range tpls {
		id, ok := a.mapped(KindTemplate, t.ID)
		if !ok {
			continue
		}
		cr := CheckRun{Template: t.Name, SourceID: itoa(t.ID)}
		run, err := a.Runs.Enqueue(ctx, a.Workspace, id, runs.Overrides{Check: &check}, runs.TriggerAPI, runs.Actor{}, nil)
		if err != nil {
			cr.Error = err.Error()
		} else {
			cr.RunID = run.ID.String()
			details, _ := json.Marshal(map[string]any{"source": "semaphore-migration", "template": t.Name, "check": true})
			_, _ = a.Q.InsertAudit(ctx, dbgen.InsertAuditParams{
				ActorLabel: "cli", Action: "run.start", TargetType: "run", TargetID: run.ID.String(),
				WorkspaceID: pgtype.UUID{Bytes: a.Workspace, Valid: true}, Details: details,
			})
		}
		res.CheckRuns = append(res.CheckRuns, cr)
	}
}
