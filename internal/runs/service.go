// Package runs holds the template and run logic behind the REST API and the
// scheduler/webhook triggers (ADR-0016): merging template defaults with
// per-run overrides, validating survey answers, pinning the project revision,
// authorizing the effective host set and enqueueing the run. It has no HTTP
// code.
package runs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// DB is the database access of the Service; *pgxpool.Pool implements it.
type DB interface {
	dbgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Authorizer checks the effective host set of a run; *policy.Authorizer
// implements it.
type Authorizer interface {
	AuthorizeHosts(ctx context.Context, p auth.Principal, workspaceID uuid.UUID, perm policy.Permission, inv uuid.UUID, hosts []string) error
}

// Options configures a Service.
type Options struct {
	DB DB
	// Graphs loads the inventory structure for limit matching; *inventory.Service implements it.
	Graphs policy.GraphSource
	Authz  Authorizer
	// Sealer encrypts the values of secret survey fields stored in runs.params.
	Sealer secrets.Provider
}

// Service manages templates and runs.
type Service struct {
	db DB
	q  *dbgen.Queries
	gs policy.GraphSource
	az Authorizer
	sp secrets.Provider
}

// New builds a Service.
func New(o Options) *Service {
	return &Service{db: o.DB, q: dbgen.New(o.DB), gs: o.Graphs, az: o.Authz, sp: o.Sealer}
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return ErrConflict
		case "23503":
			return ErrNotFound
		}
	}
	return err
}

func toPg(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}

func fromPg(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// revisionScan returns the latest synced revision of the project and its scan.
func (s *Service) revisionScan(ctx context.Context, project uuid.UUID) (string, scan.Result, error) {
	rev, err := s.q.GetLatestProjectRevision(ctx, project)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", scan.Result{}, ErrNotSynced
	}
	if err != nil {
		return "", scan.Result{}, fmt.Errorf("load project revision: %w", err)
	}
	var res scan.Result
	if len(rev.Scan) > 0 {
		if err := json.Unmarshal(rev.Scan, &res); err != nil {
			return "", scan.Result{}, fmt.Errorf("decode project scan: %w", err)
		}
	}
	return rev.Sha, res, nil
}

func hasPlaybook(res scan.Result, path string) bool {
	for _, p := range res.Playbooks {
		if p.Path == path {
			return true
		}
	}
	return false
}

// scannedSurvey collects the argument_specs surveys of the roles the playbook
// uses (entry point "main"). Their fields type-check the supplied variables;
// required fields are not enforced because the playbook may set them itself.
func scannedSurvey(res scan.Result, playbook string) []scan.Field {
	var pb *scan.Playbook
	for i := range res.Playbooks {
		if res.Playbooks[i].Path == playbook {
			pb = &res.Playbooks[i]
		}
	}
	if pb == nil {
		return nil
	}
	used := map[string]bool{}
	for _, r := range pb.Roles {
		used[r] = true
	}
	var out []scan.Field
	seen := map[string]bool{}
	for _, r := range res.Roles {
		if !used[r.Name] {
			continue
		}
		for _, sv := range r.Surveys {
			if sv.EntryPoint != "main" {
				continue
			}
			for _, f := range sv.Fields {
				if !seen[f.Name] {
					seen[f.Name] = true
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// Enqueue creates a queued run of the template. It merges the template
// defaults with the overrides (rejecting overrides the template does not
// allow), validates the extra variables against the survey, pins the
// project's latest synced revision, authorizes the effective host set
// (inventory and limit) for interactive actors and inserts the run with its
// parameter snapshot. trigger is one of the Trigger constants; scheduleID is
// set for scheduled runs.
func (s *Service) Enqueue(ctx context.Context, ws, templateID uuid.UUID, ov Overrides, trigger string, actor Actor, scheduleID *uuid.UUID) (Run, error) {
	switch trigger {
	case TriggerManual, TriggerAPI:
		if actor.Principal == nil {
			return Run{}, fmt.Errorf("%w: interactive runs need an authenticated actor", ErrInvalid)
		}
	case TriggerSchedule, TriggerWebhook:
	default:
		return Run{}, fmt.Errorf("%w: unknown trigger", ErrInvalid)
	}
	t, err := s.q.GetTemplate(ctx, dbgen.GetTemplateParams{ID: templateID, WorkspaceID: ws})
	if err != nil {
		return Run{}, mapErr(err)
	}
	sha, res, err := s.revisionScan(ctx, t.ProjectID)
	if err != nil {
		return Run{}, err
	}
	if !hasPlaybook(res, t.Playbook) {
		return Run{}, fmt.Errorf("%w: playbook %q does not exist in the synced project revision", ErrInvalid, t.Playbook)
	}
	surveyFields, strict, err := s.surveyFor(t, res)
	if err != nil {
		return Run{}, err
	}
	p, err := mergeParams(t, ov, surveyFields, strict)
	if err != nil {
		return Run{}, err
	}
	p.ProjectID, p.ProjectRevision = t.ProjectID, sha
	vis, err := s.q.ListTemplateVaultIdentities(ctx, t.ID)
	if err != nil {
		return Run{}, fmt.Errorf("load template vault identities: %w", err)
	}
	p.VaultIdentityIDs = make([]uuid.UUID, 0, len(vis))
	for _, v := range vis {
		p.VaultIdentityIDs = append(p.VaultIdentityIDs, v.ID)
	}

	g, err := s.gs.InventoryGraph(ctx, ws, t.InventoryID)
	if errors.Is(err, policy.ErrNoInventory) {
		return Run{}, fmt.Errorf("%w: the template's inventory no longer exists", ErrInvalid)
	}
	if err != nil {
		return Run{}, fmt.Errorf("load inventory graph: %w", err)
	}
	hosts, err := ResolveLimit(g, p.Limit)
	if err != nil {
		return Run{}, err
	}
	if len(hosts) == 0 {
		return Run{}, fmt.Errorf("%w: the limit matches no hosts of the inventory", ErrInvalid)
	}
	p.Hosts = hosts
	if actor.Principal != nil {
		if err := s.az.AuthorizeHosts(ctx, *actor.Principal, ws, policy.RunExecute, t.InventoryID, hosts); err != nil {
			return Run{}, err
		}
	}

	if err := p.SealSecretVars(s.sp, ws); err != nil {
		return Run{}, err
	}
	ovStored := ov
	ovStored.ExtraVars = maskVars(ov.ExtraVars, p.SecretVars)
	ovJSON, err := json.Marshal(ovStored)
	if err != nil {
		return Run{}, fmt.Errorf("encode overrides: %w", err)
	}
	pJSON, err := json.Marshal(p)
	if err != nil {
		return Run{}, fmt.Errorf("encode params: %w", err)
	}
	var by *uuid.UUID
	if actor.UserID != uuid.Nil {
		by = &actor.UserID
	}
	row, err := s.q.EnqueueRun(ctx, dbgen.EnqueueRunParams{
		WorkspaceID: ws, TemplateID: toPg(&t.ID), TemplateName: t.Name, ProjectID: toPg(&t.ProjectID),
		ProjectRevision: sha, Trigger: trigger, TriggeredBy: toPg(by), ScheduleID: toPg(scheduleID),
		Overrides: ovJSON, Params: pJSON,
	})
	if err != nil {
		return Run{}, fmt.Errorf("enqueue run: %w", mapErr(err))
	}
	s.notifyList(ctx, row.ID)
	return toRun(row)
}

// surveyFor picks the survey that validates the extra variables: the
// template's own survey (strict, required fields enforced) or else the
// scanned argument_specs of the playbook's roles (lenient).
func (s *Service) surveyFor(t dbgen.Template, res scan.Result) ([]scan.Field, bool, error) {
	if len(t.Survey) > 0 {
		f, err := ParseSurvey(t.Survey)
		if err != nil {
			return nil, false, fmt.Errorf("template survey: %w", err)
		}
		if len(f) > 0 {
			return f, true, nil
		}
	}
	return scannedSurvey(res, t.Playbook), false, nil
}

func decodeObject(raw []byte) (map[string]any, error) {
	m := map[string]any{}
	if len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// mergeParams applies the overrides to the template defaults.
func mergeParams(t dbgen.Template, ov Overrides, survey []scan.Field, strict bool) (Params, error) {
	reject := func(what string) error {
		return fmt.Errorf("%w: the template does not allow overriding %s", ErrInvalid, what)
	}
	p := Params{
		Playbook: t.Playbook, InventoryID: t.InventoryID, Limit: t.DefaultLimit,
		Tags: t.DefaultTags, SkipTags: t.DefaultSkipTags, Check: t.CheckMode, Diff: t.DiffMode,
		Verbosity: int(t.Verbosity), SSHSecretID: fromPg(t.SshSecretID), BecomeSecretID: fromPg(t.BecomeSecretID),
	}
	var err error
	if p.ExtraVars, err = decodeObject(t.ExtraVars); err != nil {
		return Params{}, err
	}
	if p.EnvVars, err = decodeObject(t.EnvVars); err != nil {
		return Params{}, err
	}
	if ov.Limit != nil {
		if !t.AllowLimitOverride {
			return Params{}, reject("the limit")
		}
		p.Limit = strings.TrimSpace(*ov.Limit)
	}
	if ov.Tags != nil || ov.SkipTags != nil {
		if !t.AllowTagsOverride {
			return Params{}, reject("tags")
		}
		if ov.Tags != nil {
			p.Tags = strings.TrimSpace(*ov.Tags)
		}
		if ov.SkipTags != nil {
			p.SkipTags = strings.TrimSpace(*ov.SkipTags)
		}
	}
	if ov.Check != nil {
		if !t.AllowCheckOverride {
			return Params{}, reject("check mode")
		}
		p.Check = *ov.Check
	}
	if ov.Diff != nil {
		if !t.AllowDiffOverride {
			return Params{}, reject("diff mode")
		}
		p.Diff = *ov.Diff
	}
	if ov.Verbosity != nil {
		if !t.AllowVerbosityOverride {
			return Params{}, reject("the verbosity")
		}
		if *ov.Verbosity < 0 || *ov.Verbosity > 4 {
			return Params{}, fmt.Errorf("%w: verbosity must be between 0 and 4", ErrInvalid)
		}
		p.Verbosity = *ov.Verbosity
	}
	if len(ov.ExtraVars) > 0 {
		// Without allow_vars_override only the answers of the template survey
		// may be set; any other variable stays under the template's control.
		surveyNames := map[string]bool{}
		if strict {
			for _, f := range survey {
				surveyNames[f.Name] = true
			}
		}
		for k, v := range ov.ExtraVars {
			if !t.AllowVarsOverride && !surveyNames[k] {
				return Params{}, reject("extra variables (only survey fields may be set)")
			}
			p.ExtraVars[k] = v
		}
	}
	if err := ValidatePattern(p.Limit); err != nil {
		return Params{}, err
	}
	if err := ApplySurvey(survey, p.ExtraVars, strict); err != nil {
		return Params{}, err
	}
	if strict {
		p.SecretVars = SecretFieldNames(survey)
	}
	return p, nil
}

// Run is the API view of a run.
type Run struct {
	ID                uuid.UUID       `json:"id"`
	TemplateID        *uuid.UUID      `json:"template_id"`
	TemplateName      string          `json:"template_name"`
	ProjectID         *uuid.UUID      `json:"project_id"`
	ProjectRevision   string          `json:"project_revision"`
	Status            string          `json:"status"`
	Trigger           string          `json:"trigger"`
	TriggeredBy       *uuid.UUID      `json:"triggered_by"`
	ScheduleID        *uuid.UUID      `json:"schedule_id"`
	Overrides         json.RawMessage `json:"overrides"`
	Params            Params          `json:"params"`
	WorkerID          string          `json:"worker_id"`
	CreatedAt         time.Time       `json:"created_at"`
	StartedAt         *time.Time      `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at"`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at"`
	ExitCode          *int            `json:"exit_code"`
	HostsOK           int             `json:"hosts_ok"`
	HostsChanged      int             `json:"hosts_changed"`
	HostsFailed       int             `json:"hosts_failed"`
	HostsUnreachable  int             `json:"hosts_unreachable"`
}

// HostResult is the per-host outcome of a run (RUN-4).
type HostResult struct {
	Host         string          `json:"host"`
	OK           int             `json:"ok"`
	Changed      int             `json:"changed"`
	Failed       int             `json:"failed"`
	Unreachable  int             `json:"unreachable"`
	Skipped      int             `json:"skipped"`
	Rescued      int             `json:"rescued"`
	Ignored      int             `json:"ignored"`
	ChangedTasks json.RawMessage `json:"changed_tasks"`
}

// RunDetail is a run with its host results.
type RunDetail struct {
	Run
	HostResults []HostResult `json:"host_results"`
}

// toRun converts a row; params and overrides are returned masked.
func toRun(r dbgen.Run) (Run, error) {
	p, err := DecodeParams(r.Params)
	if err != nil {
		return Run{}, err
	}
	p = p.Masked()
	if p.Hosts == nil {
		p.Hosts = []string{}
	}
	out := Run{
		ID: r.ID, TemplateID: fromPg(r.TemplateID), TemplateName: r.TemplateName, ProjectID: fromPg(r.ProjectID),
		ProjectRevision: r.ProjectRevision, Status: r.Status, Trigger: r.Trigger, TriggeredBy: fromPg(r.TriggeredBy),
		ScheduleID: fromPg(r.ScheduleID), Overrides: json.RawMessage(r.Overrides), Params: p, WorkerID: r.WorkerID,
		CreatedAt: r.CreatedAt.Time, StartedAt: tsPtr(r.StartedAt), FinishedAt: tsPtr(r.FinishedAt),
		CancelRequestedAt: tsPtr(r.CancelRequestedAt),
		HostsOK:           int(r.HostsOk), HostsChanged: int(r.HostsChanged), HostsFailed: int(r.HostsFailed),
		HostsUnreachable: int(r.HostsUnreachable),
	}
	if len(out.Overrides) == 0 {
		out.Overrides = json.RawMessage("{}")
	}
	if r.ExitCode.Valid {
		c := int(r.ExitCode.Int32)
		out.ExitCode = &c
	}
	return out, nil
}

// GetRun returns a run with its host results.
func (s *Service) GetRun(ctx context.Context, ws, id uuid.UUID) (RunDetail, error) {
	r, err := s.q.GetRun(ctx, dbgen.GetRunParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return RunDetail{}, mapErr(err)
	}
	run, err := toRun(r)
	if err != nil {
		return RunDetail{}, err
	}
	rows, err := s.q.ListRunHostResults(ctx, id)
	if err != nil {
		return RunDetail{}, fmt.Errorf("list host results: %w", err)
	}
	d := RunDetail{Run: run, HostResults: make([]HostResult, 0, len(rows))}
	for _, h := range rows {
		ct := json.RawMessage(h.ChangedTasks)
		if len(ct) == 0 {
			ct = json.RawMessage("[]")
		}
		d.HostResults = append(d.HostResults, HostResult{
			Host: h.Host, OK: int(h.Ok), Changed: int(h.Changed), Failed: int(h.Failed), Unreachable: int(h.Unreachable),
			Skipped: int(h.Skipped), Rescued: int(h.Rescued), Ignored: int(h.Ignored), ChangedTasks: ct,
		})
	}
	return d, nil
}

// Filter selects runs for ListRuns. Empty fields do not filter.
type Filter struct {
	TemplateID *uuid.UUID
	Status     string
	Trigger    string
	UserID     *uuid.UUID
	Host       string
	Cursor     string
	Limit      int
	// Visible, if set, drops runs the caller may not see (scoped memberships).
	Visible func(Run) bool
}

// Page is one page of runs; NextCursor is empty on the last page.
type Page struct {
	Items      []Run  `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// Page size bounds and the number of rows examined per call when Visible filters.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	maxScanRows     = 2000
)

var validStatus = map[string]bool{StatusQueued: true, StatusRunning: true, StatusSucceeded: true, StatusFailed: true, StatusCancelled: true, StatusLost: true}
var validTrigger = map[string]bool{TriggerManual: true, TriggerSchedule: true, TriggerWebhook: true, TriggerAPI: true}

func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("%w: invalid cursor", ErrInvalid)
	}
	return t, id, nil
}

// ListRuns returns the run history, newest first, with keyset pagination.
func (s *Service) ListRuns(ctx context.Context, ws uuid.UUID, f Filter) (Page, error) {
	if f.Status != "" && !validStatus[f.Status] {
		return Page{}, fmt.Errorf("%w: unknown status", ErrInvalid)
	}
	if f.Trigger != "" && !validTrigger[f.Trigger] {
		return Page{}, fmt.Errorf("%w: unknown trigger", ErrInvalid)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	arg := dbgen.ListRunsParams{
		WorkspaceID: ws, TemplateID: toPg(f.TemplateID), TriggeredBy: toPg(f.UserID), PageSize: int32(limit + 1), //nolint:gosec // bounded above
	}
	if f.Status != "" {
		arg.Status = pgtype.Text{String: f.Status, Valid: true}
	}
	if f.Trigger != "" {
		arg.Trigger = pgtype.Text{String: f.Trigger, Valid: true}
	}
	if f.Host != "" {
		arg.Host = pgtype.Text{String: f.Host, Valid: true}
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
		arg.CursorCreatedAt = pgtype.Timestamptz{Time: t, Valid: true}
		arg.CursorID = pgtype.UUID{Bytes: id, Valid: true}
	}
	page := Page{Items: []Run{}}
	scanned := 0
	for {
		rows, err := s.q.ListRuns(ctx, arg)
		if err != nil {
			return Page{}, fmt.Errorf("list runs: %w", err)
		}
		for _, r := range rows {
			scanned++
			run, err := toRun(r)
			if err != nil {
				return Page{}, err
			}
			if f.Visible != nil && !f.Visible(run) {
				continue
			}
			if len(page.Items) == limit {
				last := page.Items[len(page.Items)-1]
				page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
				return page, nil
			}
			page.Items = append(page.Items, run)
		}
		if len(rows) < int(arg.PageSize) {
			return page, nil
		}
		if scanned >= maxScanRows {
			// Too many invisible rows: hand back a cursor at the last examined row.
			last := rows[len(rows)-1]
			page.NextCursor = encodeCursor(last.CreatedAt.Time, last.ID)
			return page, nil
		}
		last := rows[len(rows)-1]
		arg.CursorCreatedAt = last.CreatedAt
		arg.CursorID = pgtype.UUID{Bytes: last.ID, Valid: true}
	}
}

// CancelRun cancels a queued run immediately and asks the worker to stop a
// running one (cancel is delivered with the next heartbeat). Finished runs
// yield ErrConflict.
func (s *Service) CancelRun(ctx context.Context, ws, id uuid.UUID) (Run, error) {
	r, err := s.q.CancelQueuedRun(ctx, dbgen.CancelQueuedRunParams{ID: id, WorkspaceID: ws})
	if errors.Is(err, pgx.ErrNoRows) {
		r, err = s.q.RequestRunCancel(ctx, dbgen.RequestRunCancelParams{ID: id, WorkspaceID: ws})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.q.GetRun(ctx, dbgen.GetRunParams{ID: id, WorkspaceID: ws}); gerr != nil {
			return Run{}, mapErr(gerr)
		}
		return Run{}, fmt.Errorf("%w: the run has already finished", ErrConflict)
	}
	if err != nil {
		return Run{}, fmt.Errorf("cancel run: %w", err)
	}
	s.notifyList(ctx, r.ID)
	return toRun(r)
}
