package remediation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
)

// Errors of the Service. Validation errors wrap ErrInvalid.
var (
	ErrNotFound = errors.New("remediation rule not found")
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("a remediation rule with this name already exists")
	ErrDisabled = errors.New("remediation rule is disabled")
	// ErrNotApplicable means the alert is resolved or the rule does not match it.
	ErrNotApplicable = errors.New("the rule does not apply to this alert")
	// ErrAlertNotFound means the alert does not exist or is not visible.
	ErrAlertNotFound = errors.New("alert not found")
)

// NotInInventoryError lists selected hosts that are linked only to inventories
// other than the template's. It wraps ErrInvalid.
type NotInInventoryError struct{ Hosts []string }

func (e *NotInInventoryError) Error() string {
	return fmt.Sprintf("%v: hosts not in the template's inventory: %s", ErrInvalid, strings.Join(e.Hosts, ", "))
}

// Unwrap lets errors.Is(err, ErrInvalid) match.
func (e *NotInInventoryError) Unwrap() error { return ErrInvalid }

// Fleet is the fleet read model; *fleet.Store implements it.
type Fleet interface {
	Hosts(ctx context.Context, ws uuid.UUID, f fleet.HostFilter, visible fleet.Visible) ([]fleet.HostView, error)
	Alerts(ctx context.Context, ws uuid.UUID, f fleet.AlertFilter, visible fleet.Visible) ([]fleet.AlertView, error)
}

// Runner is the part of *runs.Service the Service uses.
type Runner interface {
	GetTemplate(ctx context.Context, ws, id uuid.UUID) (runs.Template, error)
	Enqueue(ctx context.Context, ws, templateID uuid.UUID, ov runs.Overrides, trigger string, actor runs.Actor, scheduleID *uuid.UUID) (runs.Run, error)
}

// Options configures a Service.
type Options struct {
	DB     dbgen.DBTX
	Fleet  Fleet
	Runs   Runner
	Graphs policy.GraphSource
}

// Service stores remediation rules and starts fixes and bulk runs on request.
type Service struct {
	q  *dbgen.Queries
	fl Fleet
	rn Runner
	gs policy.GraphSource
}

// New builds a Service.
func New(o Options) *Service {
	return &Service{q: dbgen.New(o.DB), fl: o.Fleet, rn: o.Runs, gs: o.Graphs}
}

// Rule is the API view of a remediation rule.
type Rule struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	ConnectorType *string    `json:"connector_type"`
	ServiceRegex  string     `json:"service_regex"`
	SummaryRegex  string     `json:"summary_regex"`
	MinSeverity   string     `json:"min_severity"`
	TemplateID    uuid.UUID  `json:"template_id"`
	Template      string     `json:"template"`
	Target        string     `json:"target"`
	Group         string     `json:"group"`
	CheckFirst    bool       `json:"check_first"`
	CreatedBy     *uuid.UUID `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Input is the writable part of a rule (create and full-replace update).
type Input struct {
	Name          string    `json:"name"`
	Enabled       *bool     `json:"enabled"`
	ConnectorType *string   `json:"connector_type"`
	ServiceRegex  string    `json:"service_regex"`
	SummaryRegex  string    `json:"summary_regex"`
	MinSeverity   string    `json:"min_severity"`
	TemplateID    uuid.UUID `json:"template_id"`
	Target        string    `json:"target"`
	Group         string    `json:"group"`
	CheckFirst    bool      `json:"check_first"`
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func pgText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// pgUUID maps the zero UUID (operator CLI) to NULL.
func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: u != uuid.Nil} }

func (in Input) criteria() Criteria {
	return Criteria{ConnectorType: in.ConnectorType, ServiceRegex: in.ServiceRegex, SummaryRegex: in.SummaryRegex, MinSeverity: in.MinSeverity}
}

// normalize validates the input and returns the cleaned copy.
func (s *Service) normalize(ctx context.Context, ws uuid.UUID, in Input) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Group = strings.TrimSpace(in.Group)
	if in.Name == "" || len(in.Name) > 120 {
		return in, fmt.Errorf("%w: name is required (max 120 characters)", ErrInvalid)
	}
	if in.MinSeverity == "" {
		in.MinSeverity = "info"
	}
	if in.ConnectorType != nil {
		t := strings.TrimSpace(*in.ConnectorType)
		if t == "" {
			in.ConnectorType = nil
		} else {
			in.ConnectorType = &t
		}
	}
	if _, err := NewMatcher(in.criteria()); err != nil {
		return in, err
	}
	switch in.Target {
	case TargetHost, TargetNetwork:
		if in.Group != "" {
			return in, fmt.Errorf("%w: group is only valid with target group", ErrInvalid)
		}
	case TargetGroup:
		if in.Group == "" {
			return in, fmt.Errorf("%w: group is required with target group", ErrInvalid)
		}
		if err := runs.ValidatePattern(in.Group); err != nil || strings.ContainsAny(in.Group, "*?[!&,:~") {
			return in, fmt.Errorf("%w: group must be a plain group name", ErrInvalid)
		}
	default:
		return in, fmt.Errorf("%w: target must be host, group or network", ErrInvalid)
	}
	t, err := s.rn.GetTemplate(ctx, ws, in.TemplateID)
	if errors.Is(err, runs.ErrNotFound) {
		return in, fmt.Errorf("%w: template not found in this workspace", ErrInvalid)
	}
	if err != nil {
		return in, fmt.Errorf("load template: %w", err)
	}
	if !t.AllowLimitOverride {
		return in, fmt.Errorf("%w: the template must allow limit overrides", ErrInvalid)
	}
	if in.CheckFirst && !t.AllowCheckOverride {
		return in, fmt.Errorf("%w: check_first needs a template that allows check mode overrides", ErrInvalid)
	}
	return in, nil
}

func toRule(r dbgen.RemediationRule, template string) Rule {
	out := Rule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, ConnectorType: textPtr(r.ConnectorType), ServiceRegex: r.ServiceRegex,
		SummaryRegex: r.SummaryRegex, MinSeverity: r.MinSeverity, TemplateID: r.TemplateID, Template: template, Target: r.Target,
		Group: r.GroupName, CheckFirst: r.CheckFirst, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC()}
	if r.CreatedBy.Valid {
		id := uuid.UUID(r.CreatedBy.Bytes)
		out.CreatedBy = &id
	}
	return out
}

func mapErr(err error) error {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.As(err, &pe) && pe.Code == "23505":
		return ErrConflict
	}
	return err
}

// List returns the workspace's rules by name.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Rule, error) {
	rows, err := s.q.ListRemediationRules(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list remediation rules: %w", err)
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRule(dbgen.RemediationRule{
			ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, Enabled: r.Enabled, ConnectorType: r.ConnectorType,
			ServiceRegex: r.ServiceRegex, SummaryRegex: r.SummaryRegex, MinSeverity: r.MinSeverity, TemplateID: r.TemplateID,
			Target: r.Target, GroupName: r.GroupName, CheckFirst: r.CheckFirst, CreatedBy: r.CreatedBy, UpdatedBy: r.UpdatedBy,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.TemplateName))
	}
	return out, nil
}

// Get returns one rule.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Rule, error) {
	r, err := s.q.GetRemediationRule(ctx, dbgen.GetRemediationRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Rule{}, mapErr(err)
	}
	return s.withTemplate(ctx, ws, r), nil
}

func (s *Service) withTemplate(ctx context.Context, ws uuid.UUID, r dbgen.RemediationRule) Rule {
	name := ""
	if t, err := s.rn.GetTemplate(ctx, ws, r.TemplateID); err == nil {
		name = t.Name
	}
	return toRule(r, name)
}

// Create stores a rule.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in Input, by uuid.UUID) (Rule, error) {
	in, err := s.normalize(ctx, ws, in)
	if err != nil {
		return Rule{}, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	r, err := s.q.CreateRemediationRule(ctx, dbgen.CreateRemediationRuleParams{
		WorkspaceID: ws, Name: in.Name, Enabled: enabled, ConnectorType: pgText(in.ConnectorType), ServiceRegex: in.ServiceRegex,
		SummaryRegex: in.SummaryRegex, MinSeverity: in.MinSeverity, TemplateID: in.TemplateID, Target: in.Target,
		GroupName: in.Group, CheckFirst: in.CheckFirst, CreatedBy: pgUUID(by),
	})
	if err != nil {
		return Rule{}, fmt.Errorf("create remediation rule: %w", mapErr(err))
	}
	return s.withTemplate(ctx, ws, r), nil
}

// Update replaces all writable fields of a rule.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in Input, by uuid.UUID) (Rule, error) {
	cur, err := s.q.GetRemediationRule(ctx, dbgen.GetRemediationRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Rule{}, mapErr(err)
	}
	in, err = s.normalize(ctx, ws, in)
	if err != nil {
		return Rule{}, err
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	r, err := s.q.UpdateRemediationRule(ctx, dbgen.UpdateRemediationRuleParams{
		ID: id, WorkspaceID: ws, Name: in.Name, Enabled: enabled, ConnectorType: pgText(in.ConnectorType), ServiceRegex: in.ServiceRegex,
		SummaryRegex: in.SummaryRegex, MinSeverity: in.MinSeverity, TemplateID: in.TemplateID, Target: in.Target,
		GroupName: in.Group, CheckFirst: in.CheckFirst, UpdatedBy: pgUUID(by),
	})
	if err != nil {
		return Rule{}, fmt.Errorf("update remediation rule: %w", mapErr(err))
	}
	return s.withTemplate(ctx, ws, r), nil
}

// Delete removes a rule.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteRemediationRule(ctx, dbgen.DeleteRemediationRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete remediation rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Preview returns the open alerts an (unsaved) rule would match; it is the
// regex test of the rule editor and starts nothing.
func (s *Service) Preview(ctx context.Context, ws uuid.UUID, c Criteria) ([]fleet.AlertView, error) {
	m, err := NewMatcher(c)
	if err != nil {
		return nil, err
	}
	alerts, err := s.fl.Alerts(ctx, ws, fleet.AlertFilter{State: fleet.AlertOpen}, nil)
	if err != nil {
		return nil, err
	}
	out := []fleet.AlertView{}
	for _, a := range alerts {
		if m.Matches(a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// RuleRef is a rule that can fix an alert.
type RuleRef struct {
	ID         uuid.UUID `json:"rule_id"`
	Name       string    `json:"name"`
	Template   string    `json:"template"`
	Target     string    `json:"target"`
	Group      string    `json:"group"`
	CheckFirst bool      `json:"check_first"`
}

// Applicable returns, per alert id, the enabled rules that match the open
// alert. Alerts without an applicable rule are absent. The alerts are not
// required to be linked to a host; linking is checked when a fix is started.
func (s *Service) Applicable(ctx context.Context, ws uuid.UUID, alerts []fleet.AlertView) (map[uuid.UUID][]RuleRef, error) {
	rules, err := s.List(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]RuleRef{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		m, err := NewMatcher(Criteria{ConnectorType: r.ConnectorType, ServiceRegex: r.ServiceRegex, SummaryRegex: r.SummaryRegex, MinSeverity: r.MinSeverity})
		if err != nil {
			continue // stored rules are validated on save
		}
		for _, a := range alerts {
			if a.HostID != nil && m.Matches(a) {
				out[a.ID] = append(out[a.ID], RuleRef{ID: r.ID, Name: r.Name, Template: r.Template, Target: r.Target, Group: r.Group, CheckFirst: r.CheckFirst})
			}
		}
	}
	return out, nil
}

// Phases of a remediation run.
const (
	PhaseCheck = "check"
	PhaseApply = "apply"
)

// Result describes the run started by Remediate or BulkRun.
type Result struct {
	Run   runs.Run `json:"run"`
	Phase string   `json:"phase,omitempty"` // check or apply (remediation only)
	Limit string   `json:"limit"`
	Hosts []string `json:"hosts"`
	// Skipped lists selected fleet hosts that were left out (bulk run only).
	Skipped []Skipped `json:"skipped,omitempty"`
}

// Skipped is a selected host that is not part of the run.
type Skipped struct {
	HostID uuid.UUID `json:"host_id"`
	Name   string    `json:"name"`
	Reason string    `json:"reason"`
}

func (s *Service) networks(ctx context.Context, ws uuid.UUID) ([]Net, error) {
	rows, err := s.q.ListNetworks(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	out := make([]Net, 0, len(rows))
	for _, n := range rows {
		out = append(out, Net{ID: n.ID, Prefix: n.Cidr})
	}
	return out, nil
}

// trigger is manual for interactive actors; the operator CLI passes no
// principal and has workspace authority (trigger api), as `acp admin run start`.
func trigger(a runs.Actor) string {
	if a.Principal == nil {
		return runs.TriggerAPI
	}
	return runs.TriggerManual
}

func provenance(v map[string]any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Remediate starts the fix of an open alert with a rule. With check_first and
// no skipCheck it queues a check-mode run; the user starts the real run
// afterwards (skipCheck true). visible narrows the alert to the caller's fleet
// scope. The runs service authorizes the effective host set against the
// caller's scopes before anything is queued (policy.ErrForbidden otherwise).
func (s *Service) Remediate(ctx context.Context, ws, alertID, ruleID uuid.UUID, skipCheck bool, actor runs.Actor, visible fleet.Visible) (Result, error) {
	rule, err := s.q.GetRemediationRule(ctx, dbgen.GetRemediationRuleParams{ID: ruleID, WorkspaceID: ws})
	if err != nil {
		return Result{}, mapErr(err)
	}
	if !rule.Enabled {
		return Result{}, ErrDisabled
	}
	alerts, err := s.fl.Alerts(ctx, ws, fleet.AlertFilter{ID: &alertID}, visible)
	if err != nil {
		return Result{}, err
	}
	if len(alerts) != 1 {
		return Result{}, ErrAlertNotFound
	}
	alert := alerts[0]
	m, err := NewMatcher(Criteria{ConnectorType: textPtr(rule.ConnectorType), ServiceRegex: rule.ServiceRegex, SummaryRegex: rule.SummaryRegex, MinSeverity: rule.MinSeverity})
	if err != nil {
		return Result{}, err
	}
	if !m.Matches(alert) {
		return Result{}, ErrNotApplicable
	}
	if alert.HostID == nil {
		return Result{}, fmt.Errorf("%w: the alert is not linked to a fleet host", ErrInvalid)
	}
	tpl, err := s.rn.GetTemplate(ctx, ws, rule.TemplateID)
	if err != nil {
		return Result{}, fmt.Errorf("load template: %w", err)
	}
	all, err := s.fl.Hosts(ctx, ws, fleet.HostFilter{}, nil)
	if err != nil {
		return Result{}, err
	}
	var host *fleet.HostView
	for i := range all {
		if all[i].ID == *alert.HostID {
			host = &all[i]
		}
	}
	if host == nil {
		return Result{}, ErrAlertNotFound
	}
	nets, err := s.networks(ctx, ws)
	if err != nil {
		return Result{}, err
	}
	limit, hosts, err := Resolve(rule.Target, rule.GroupName, tpl.InventoryID, *host, all, nets, func() (policy.Graph, error) {
		g, err := s.gs.InventoryGraph(ctx, ws, tpl.InventoryID)
		if err != nil {
			return policy.Graph{}, fmt.Errorf("load inventory graph: %w", err)
		}
		return g, nil
	})
	if err != nil {
		return Result{}, err
	}
	phase := PhaseApply
	ov := runs.Overrides{Limit: &limit}
	if rule.CheckFirst && !skipCheck {
		check := true
		phase, ov.Check = PhaseCheck, &check
	}
	actor.Remediation = provenance(map[string]any{
		"source": "remediation", "rule_id": rule.ID, "rule": rule.Name, "alert_id": alert.ID, "phase": phase,
	})
	run, err := s.rn.Enqueue(ctx, ws, rule.TemplateID, ov, trigger(actor), actor, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{Run: run, Phase: phase, Limit: limit, Hosts: hosts}, nil
}

// MaxBulkHosts bounds the hosts of one bulk run request.
const MaxBulkHosts = 500

// BulkInput is a fleet bulk action request.
type BulkInput struct {
	HostIDs    []uuid.UUID
	TemplateID uuid.UUID
	CheckMode  bool
}

// BulkRun starts one run of the template over the inventory hosts linked to
// the selected fleet hosts. Fleet hosts without an inventory link are skipped
// and reported; hosts linked only to other inventories fail the request with a
// NotInInventoryError. The effective host set is authorized by the runs
// service before the run is queued.
func (s *Service) BulkRun(ctx context.Context, ws uuid.UUID, in BulkInput, actor runs.Actor, visible fleet.Visible) (Result, error) {
	if len(in.HostIDs) == 0 || len(in.HostIDs) > MaxBulkHosts {
		return Result{}, fmt.Errorf("%w: select between 1 and %d hosts", ErrInvalid, MaxBulkHosts)
	}
	tpl, err := s.rn.GetTemplate(ctx, ws, in.TemplateID)
	if errors.Is(err, runs.ErrNotFound) {
		return Result{}, fmt.Errorf("%w: template not found in this workspace", ErrInvalid)
	}
	if err != nil {
		return Result{}, fmt.Errorf("load template: %w", err)
	}
	if !tpl.AllowLimitOverride {
		return Result{}, fmt.Errorf("%w: the template must allow limit overrides", ErrInvalid)
	}
	if in.CheckMode && !tpl.CheckMode && !tpl.AllowCheckOverride {
		return Result{}, fmt.Errorf("%w: the template does not allow check mode", ErrInvalid)
	}
	all, err := s.fl.Hosts(ctx, ws, fleet.HostFilter{}, visible)
	if err != nil {
		return Result{}, err
	}
	byID := make(map[uuid.UUID]fleet.HostView, len(all))
	for _, h := range all {
		byID[h.ID] = h
	}
	names := map[string]bool{}
	var skipped []Skipped
	var elsewhere []string
	seen := map[uuid.UUID]bool{}
	for _, id := range in.HostIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		h, ok := byID[id]
		switch {
		case !ok:
			skipped = append(skipped, Skipped{HostID: id, Reason: "host not found"})
		case len(h.Ansible) == 0:
			skipped = append(skipped, Skipped{HostID: id, Name: h.Name, Reason: "not linked to an inventory host"})
		default:
			own := linkedNames(h, tpl.InventoryID)
			if len(own) == 0 {
				elsewhere = append(elsewhere, h.Name)
			}
			for _, n := range own {
				names[n] = true
			}
		}
	}
	if len(elsewhere) > 0 {
		return Result{}, &NotInInventoryError{Hosts: elsewhere}
	}
	if len(names) == 0 {
		return Result{}, fmt.Errorf("%w: none of the selected hosts is linked to an inventory host", ErrInvalid)
	}
	hosts := make([]string, 0, len(names))
	for n := range names {
		hosts = append(hosts, n)
	}
	sort.Strings(hosts)
	limit := JoinLimit(hosts)
	ov := runs.Overrides{Limit: &limit}
	if in.CheckMode {
		check := true
		ov.Check = &check
	}
	actor.Remediation = provenance(map[string]any{"source": "bulk", "hosts": len(hosts), "check": in.CheckMode})
	run, err := s.rn.Enqueue(ctx, ws, in.TemplateID, ov, trigger(actor), actor, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{Run: run, Limit: limit, Hosts: hosts, Skipped: skipped}, nil
}
