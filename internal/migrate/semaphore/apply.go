package semaphore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/schedules"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// MappingSource is the migration_mappings.source value of this migration.
const MappingSource = "semaphore"

// Apply results.
const (
	ResCreated  = "created"
	ResExists   = "exists"   // already imported, nothing done
	ResConflict = "conflict" // an unmapped ACP object has the same name; skipped
	ResFailed   = "failed"
	ResSkipped  = "skipped" // unmappable or a dependency is missing
)

// ApplyItem is one line of the apply report.
type ApplyItem struct {
	Kind     string `json:"kind"`
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	Result   string `json:"result"`
	ACPID    string `json:"acp_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ApplyResult is the outcome of Apply.
type ApplyResult struct {
	Workspace string         `json:"workspace"`
	Items     []ApplyItem    `json:"items"`
	Counts    map[string]int `json:"counts"`
}

// Failed reports whether any object failed or conflicted.
func (r *ApplyResult) Failed() bool { return r.Counts[ResFailed] > 0 || r.Counts[ResConflict] > 0 }

// Applier executes a mapping plan against ACP. Secret values are decrypted
// one at a time and handed straight to secrets.Service; they are never logged
// or placed in a report or an audit entry.
type Applier struct {
	Q           *dbgen.Queries
	Secrets     *secrets.Service
	Inventories *inventory.Service
	Runs        *runs.Service
	Schedules   *schedules.Service
	Workspace   uuid.UUID
	Slug        string
	EncKey      Secret
	// Mappings is the Target.Mappings map the plan was built from; Apply
	// extends it as objects are created.
	Mappings map[string]string
}

type applyRun struct {
	*Applier
	res ApplyResult
}

func (a *applyRun) add(it ApplyItem) {
	a.res.Items = append(a.res.Items, it)
	a.res.Counts[it.Result]++
}

func (a *applyRun) mapped(kind string, id int64) (uuid.UUID, bool) {
	v, ok := a.Mappings[MappingKey(kind, id)]
	if !ok {
		return uuid.Nil, false
	}
	u, err := uuid.Parse(v)
	return u, err == nil
}

// record stores the mapping and the audit entry right after a create.
func (a *applyRun) record(ctx context.Context, kind string, srcID int64, id uuid.UUID, name string) error {
	if _, err := a.Q.CreateMigrationMapping(ctx, dbgen.CreateMigrationMappingParams{
		Source: MappingSource, Kind: kind, SourceID: strconv.FormatInt(srcID, 10), AcpID: id, WorkspaceID: a.Workspace,
	}); err != nil {
		return fmt.Errorf("record mapping for %s %d: %w", kind, srcID, err)
	}
	a.Mappings[MappingKey(kind, srcID)] = id.String()
	details, _ := json.Marshal(map[string]any{"source": "semaphore-migration", "source_id": srcID, "name": name})
	if _, err := a.Q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: "migration." + kind + ".create", TargetType: kind, TargetID: id.String(),
		WorkspaceID: pgtype.UUID{Bytes: a.Workspace, Valid: true}, Details: details,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func isUnique(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// Apply creates every planned object that is not yet mapped. It stops with an
// error only when the mapping or audit bookkeeping fails; per-object problems
// are reported as conflict, failed or skipped items.
func (a *Applier) Apply(ctx context.Context, snap *Snapshot, rep *Report) (*ApplyResult, error) {
	r := &applyRun{Applier: a, res: ApplyResult{Workspace: a.Slug, Counts: map[string]int{}}}
	keys := map[int64]Key{}
	for _, k := range snap.Keys {
		keys[k.ID] = k
	}
	invs := map[int64]Inventory{}
	for _, i := range snap.Inventories {
		invs[i.ID] = i
	}
	for _, step := range []func(context.Context, *Snapshot, *Report, map[int64]Key, map[int64]Inventory) error{
		r.secrets, r.vaults, r.inventories, r.templates, r.schedules,
	} {
		if err := step(ctx, snap, rep, keys, invs); err != nil {
			return &r.res, err
		}
	}
	for _, grp := range [][]Item{rep.Secrets, rep.Inventories, rep.Templates, rep.Schedules} {
		for _, it := range grp {
			if it.Status == StatusUnmappable {
				r.add(ApplyItem{Kind: it.Kind, SourceID: it.SourceID, Name: it.Name, Result: ResSkipped, Reason: it.Reason})
			}
		}
	}
	return &r.res, nil
}

// existing reports already imported objects of a kind.
func (a *applyRun) existing(kind string, items []Item) {
	for _, it := range items {
		if it.Status != StatusExists {
			continue
		}
		id, _ := strconv.ParseInt(it.SourceID, 10, 64)
		acp, _ := a.mapped(kind, id)
		a.add(ApplyItem{Kind: kind, SourceID: it.SourceID, Name: it.Name, Result: ResExists, ACPID: acp.String()})
	}
}

func (a *applyRun) secrets(ctx context.Context, _ *Snapshot, rep *Report, keys map[int64]Key, _ map[int64]Inventory) error {
	a.existing(KindSecret, rep.Secrets)
	metas, err := a.Q.ListSecretsMeta(ctx, a.Workspace)
	if err != nil {
		return fmt.Errorf("list secrets: %w", err)
	}
	names := map[string]bool{}
	for _, m := range metas {
		names[m.Name] = true
	}
	for _, ps := range rep.PlanSecrets {
		it := ApplyItem{Kind: KindSecret, SourceID: itoa(ps.SourceKeyID), Name: ps.Name}
		if _, ok := a.mapped(KindSecret, ps.SourceKeyID); ok {
			it.Result = ResExists
			a.add(it)
			continue
		}
		if names[ps.Name] {
			it.Result, it.Reason = ResConflict, "a secret with this name already exists in ACP and is not mapped"
			a.add(it)
			continue
		}
		cred, err := Decrypt(keys[ps.SourceKeyID], a.EncKey)
		if err != nil || cred == nil {
			it.Result, it.Reason = ResFailed, "cannot decrypt key"
			a.add(it)
			continue
		}
		value := cred.PrivateKey
		if ps.Type != "ssh_key" {
			value = cred.Password
		}
		meta, err := a.Secrets.Create(ctx, a.Workspace, secrets.CreateInput{
			Name: ps.Name, Type: ps.Type, Value: value.Reveal(),
			Description: fmt.Sprintf("Imported from Semaphore access key #%d", ps.SourceKeyID),
		})
		switch {
		case errors.Is(err, secrets.ErrConflict):
			it.Result, it.Reason = ResConflict, "a secret with this name already exists in ACP and is not mapped"
		case err != nil:
			it.Result, it.Reason = ResFailed, err.Error()
		default:
			it.Result, it.ACPID = ResCreated, meta.ID.String()
			names[ps.Name] = true
			if err := a.record(ctx, KindSecret, ps.SourceKeyID, meta.ID, ps.Name); err != nil {
				a.add(it)
				return err
			}
		}
		a.add(it)
	}
	return nil
}

func (a *applyRun) vaults(ctx context.Context, snap *Snapshot, rep *Report, _ map[int64]Key, _ map[int64]Inventory) error {
	seen := map[int64]bool{}
	for _, tv := range snap.TemplateVault {
		if tv.VaultKeyID == nil || seen[*tv.VaultKeyID] {
			continue
		}
		seen[*tv.VaultKeyID] = true
		if _, ok := a.mapped(KindVault, *tv.VaultKeyID); ok {
			a.add(ApplyItem{Kind: KindVault, SourceID: itoa(*tv.VaultKeyID), Name: tv.Name, Result: ResExists})
		}
	}
	for _, pv := range rep.PlanVaults {
		it := ApplyItem{Kind: KindVault, SourceID: itoa(pv.SourceKeyID), Name: pv.Label}
		sid, ok := a.mapped(KindSecret, pv.SourceKeyID)
		if !ok {
			it.Result, it.Reason = ResSkipped, "its secret was not imported"
			a.add(it)
			continue
		}
		if _, err := a.Q.GetVaultIdentityByLabel(ctx, dbgen.GetVaultIdentityByLabelParams{WorkspaceID: a.Workspace, Label: pv.Label}); err == nil {
			it.Result, it.Reason = ResConflict, "a vault identity with this label already exists in ACP and is not mapped"
			a.add(it)
			continue
		}
		row, err := a.Q.CreateVaultIdentity(ctx, dbgen.CreateVaultIdentityParams{
			WorkspaceID: a.Workspace, Label: pv.Label, Description: fmt.Sprintf("Imported from Semaphore access key #%d", pv.SourceKeyID),
			Provider: "secret", SecretID: pgtype.UUID{Bytes: sid, Valid: true}, ScriptArgs: []byte("[]"),
		})
		switch {
		case isUnique(err):
			it.Result, it.Reason = ResConflict, "a vault identity with this label already exists in ACP and is not mapped"
		case err != nil:
			it.Result, it.Reason = ResFailed, err.Error()
		default:
			it.Result, it.ACPID = ResCreated, row.ID.String()
			if err := a.record(ctx, KindVault, pv.SourceKeyID, row.ID, pv.Label); err != nil {
				a.add(it)
				return err
			}
		}
		a.add(it)
	}
	return nil
}

func (a *applyRun) inventories(ctx context.Context, snap *Snapshot, rep *Report, _ map[int64]Key, invs map[int64]Inventory) error {
	// Archives are reported per inventory: "exists" for mapped ones, plan for new.
	planned := map[int64]PlannedInventory{}
	for _, p := range rep.PlanInventories {
		planned[p.SourceID] = p
	}
	for _, inv := range snap.Inventories {
		if !isStaticType(inv.Type) {
			continue
		}
		p, isNew := planned[inv.ID]
		if id, ok := a.mapped(KindInventory, inv.ID); ok && !isNew {
			a.add(ApplyItem{Kind: KindInventory, SourceID: itoa(inv.ID), Name: "semaphore-" + inv.Name, Result: ResExists, ACPID: id.String()})
			continue
		}
		if !isNew {
			continue
		}
		it := ApplyItem{Kind: KindInventory, SourceID: itoa(inv.ID), Name: p.Name}
		res, err := a.Inventories.CreateFromText(ctx, a.Workspace, inventory.FromTextInput{Name: p.Name, Slug: p.Slug, Format: p.Format, Content: invs[inv.ID].Inventory})
		switch {
		case errors.Is(err, inventory.ErrConflict):
			it.Result, it.Reason = ResConflict, "an inventory with this name or slug already exists in ACP and is not mapped"
		case err != nil:
			it.Result, it.Reason = ResFailed, err.Error()
		default:
			it.Result, it.ACPID = ResCreated, res.Inventory.ID.String()
			if err := a.record(ctx, KindInventory, inv.ID, res.Inventory.ID, p.Name); err != nil {
				a.add(it)
				return err
			}
		}
		a.add(it)
	}
	return nil
}

func isStaticType(t string) bool { return t == "static" || t == "static-yaml" || t == "" }

func (a *applyRun) templates(ctx context.Context, _ *Snapshot, rep *Report, _ map[int64]Key, _ map[int64]Inventory) error {
	a.existing(KindTemplate, rep.Templates)
	acpInv := map[string]uuid.UUID{}
	list, err := a.Q.ListInventories(ctx, a.Workspace)
	if err != nil {
		return fmt.Errorf("list inventories: %w", err)
	}
	for _, i := range list {
		acpInv[i.Slug] = i.ID
	}
	for _, pt := range rep.PlanTemplates {
		it := ApplyItem{Kind: KindTemplate, SourceID: itoa(pt.SourceID), Name: pt.Name}
		if _, ok := a.mapped(KindTemplate, pt.SourceID); ok {
			it.Result = ResExists
			a.add(it)
			continue
		}
		in, why := a.templateInput(pt, acpInv)
		if why != "" {
			it.Result, it.Reason = ResSkipped, why
			a.add(it)
			continue
		}
		t, err := a.Runs.CreateTemplate(ctx, a.Workspace, in)
		switch {
		case errors.Is(err, runs.ErrConflict):
			it.Result, it.Reason = ResConflict, "a template with this name already exists in ACP and is not mapped"
		case err != nil:
			it.Result, it.Reason = ResFailed, err.Error()
		default:
			it.Result, it.ACPID = ResCreated, t.ID.String()
			if err := a.record(ctx, KindTemplate, pt.SourceID, t.ID, pt.Name); err != nil {
				a.add(it)
				return err
			}
		}
		a.add(it)
	}
	return nil
}

// templateInput resolves the references of a planned template. A non-empty
// reason means a dependency was not imported (conflict or failure upstream).
func (a *applyRun) templateInput(pt PlannedTemplate, acpInv map[string]uuid.UUID) (runs.TemplateInput, string) {
	pid, err := uuid.Parse(pt.ProjectID)
	if err != nil {
		return runs.TemplateInput{}, "invalid project id"
	}
	var iid uuid.UUID
	if pt.InventoryM {
		id, ok := acpInv[pt.Inventory]
		if !ok {
			return runs.TemplateInput{}, fmt.Sprintf("inventory %q does not exist", pt.Inventory)
		}
		iid = id
	} else {
		id, ok := a.mapped(KindInventory, pt.InventorySrc)
		if !ok {
			return runs.TemplateInput{}, "its archive inventory was not imported"
		}
		iid = id
	}
	in := runs.TemplateInput{
		Name: &pt.Name, ProjectID: &pid, InventoryID: &iid, Playbook: &pt.Playbook,
		ExtraVars: pt.ExtraVars, DefaultLimit: &pt.Limit, DefaultTags: &pt.Tags, DefaultSkipTags: &pt.SkipTags,
		Verbosity: &pt.Verbosity, CheckMode: &pt.Check, DiffMode: &pt.Diff,
	}
	if len(pt.EnvVars) > 0 {
		in.EnvVars = map[string]any{}
		for k, v := range pt.EnvVars {
			in.EnvVars[k] = v
		}
	}
	for _, ref := range []struct {
		src *int64
		dst *projects.Field[uuid.UUID]
		lbl string
	}{{pt.SSHKeyID, &in.SSHSecretID, "SSH secret"}, {pt.BecomeID, &in.BecomeSecretID, "become secret"}} {
		if ref.src == nil {
			continue
		}
		id, ok := a.mapped(KindSecret, *ref.src)
		if !ok {
			return runs.TemplateInput{}, "its " + ref.lbl + " was not imported"
		}
		*ref.dst = projects.Field[uuid.UUID]{Set: true, Value: &id}
	}
	if len(pt.VaultKeys) > 0 {
		ids := make([]uuid.UUID, 0, len(pt.VaultKeys))
		for _, k := range pt.VaultKeys {
			id, ok := a.mapped(KindVault, k)
			if !ok {
				return runs.TemplateInput{}, "a vault identity was not imported"
			}
			ids = append(ids, id)
		}
		in.VaultIdentityIDs = &ids
	}
	if len(pt.Survey) > 0 {
		raw, _ := json.Marshal(pt.Survey)
		in.Survey = projects.Field[json.RawMessage]{Set: true, Value: (*json.RawMessage)(&raw)}
	}
	return in, ""
}

func (a *applyRun) schedules(ctx context.Context, _ *Snapshot, rep *Report, _ map[int64]Key, _ map[int64]Inventory) error {
	a.existing(KindSchedule, rep.Schedules)
	for _, ps := range rep.PlanSchedules {
		name := strings.TrimSpace(ps.Name)
		if name == "" {
			name = "semaphore schedule " + itoa(ps.SourceID)
		}
		it := ApplyItem{Kind: KindSchedule, SourceID: itoa(ps.SourceID), Name: name}
		if _, ok := a.mapped(KindSchedule, ps.SourceID); ok {
			it.Result = ResExists
			a.add(it)
			continue
		}
		tid, ok := a.mapped(KindTemplate, ps.TemplateID)
		if !ok {
			it.Result, it.Reason = ResSkipped, "its template was not imported"
			a.add(it)
			continue
		}
		off := false
		s, err := a.Schedules.Create(ctx, a.Workspace, tid, uuid.Nil, schedules.Input{Name: &name, Cron: &ps.Cron, Enabled: &off})
		if err != nil {
			it.Result, it.Reason = ResFailed, err.Error()
			a.add(it)
			continue
		}
		it.Result, it.ACPID = ResCreated, s.ID.String()
		if err := a.record(ctx, KindSchedule, ps.SourceID, s.ID, name); err != nil {
			a.add(it)
			return err
		}
		a.add(it)
	}
	return nil
}
