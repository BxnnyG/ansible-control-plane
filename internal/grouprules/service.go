package grouprules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
)

// Errors of the Service. Validation errors wrap ErrInvalid.
var (
	ErrNotFound    = errors.New("group rule not found")
	ErrConflict    = errors.New("a group rule with this name already exists")
	ErrDisabled    = errors.New("group rule is disabled")
	ErrRuleChanged = errors.New("group rule was changed while applying")
)

// DB is the database access of the Service; *pgxpool.Pool implements it.
type DB interface {
	dbgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Fleet lists the merged fleet hosts; *fleet.Store implements it.
type Fleet interface {
	Hosts(ctx context.Context, ws uuid.UUID, f fleet.HostFilter, visible fleet.Visible) ([]fleet.HostView, error)
}

// Inventories is the part of *inventory.Service the Service uses.
type Inventories interface {
	Get(ctx context.Context, ws, id uuid.UUID) (inventory.Inventory, error)
	GroupState(ctx context.Context, ws, id uuid.UUID, group string) (inventory.GroupState, error)
	ApplyGroupChange(ctx context.Context, ws, id uuid.UUID, in inventory.GroupApply) (inventory.GroupChangeResult, error)
}

// Options configures a Service.
type Options struct {
	DB          DB
	Fleet       Fleet
	Inventories Inventories
}

// Service stores group rules, previews and applies them.
type Service struct {
	db  DB
	q   *dbgen.Queries
	fl  Fleet
	inv Inventories
}

// New builds a Service.
func New(o Options) *Service {
	return &Service{db: o.DB, q: dbgen.New(o.DB), fl: o.Fleet, inv: o.Inventories}
}

// Rule is the API view of a group rule.
type Rule struct {
	ID            uuid.UUID       `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Params        json.RawMessage `json:"params"`
	InventoryID   uuid.UUID       `json:"inventory_id"`
	Group         string          `json:"group"`
	Mode          string          `json:"mode"`
	Enabled       bool            `json:"enabled"`
	LastAppliedAt *time.Time      `json:"last_applied_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func toRule(r dbgen.GroupRule) Rule {
	v := Rule{ID: r.ID, Name: r.Name, Kind: r.Kind, Params: r.Params, InventoryID: r.InventoryID, Group: r.GroupName,
		Mode: r.Mode, Enabled: r.Enabled, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC()}
	if r.LastAppliedAt.Valid {
		t := r.LastAppliedAt.Time.UTC()
		v.LastAppliedAt = &t
	}
	if len(v.Params) == 0 {
		v.Params = json.RawMessage("{}")
	}
	return v
}

// Input creates or replaces a rule's settings. Enabled defaults to true and
// Mode to add-only on create.
type Input struct {
	Name        string
	Kind        string
	Params      json.RawMessage
	InventoryID uuid.UUID
	Group       string
	Mode        string
	Enabled     *bool
}

// Update changes a rule; nil fields stay as they are.
type Update struct {
	Name        *string
	Kind        *string
	Params      *json.RawMessage
	InventoryID *uuid.UUID
	Group       *string
	Mode        *string
	Enabled     *bool
}

type clean struct {
	name, kind, mode, group string
	params                  []byte
	inv                     uuid.UUID
	enabled                 bool
}

func (s *Service) validate(ctx context.Context, ws uuid.UUID, c clean) (clean, error) {
	c.name = strings.TrimSpace(c.name)
	if c.name == "" || len(c.name) > 100 {
		return c, invalid("name must be 1-100 characters")
	}
	_, norm, err := ParseMatcher(c.kind, c.params)
	if err != nil {
		return c, err
	}
	c.params = norm
	switch c.mode {
	case ModeAddOnly, ModeSync:
	default:
		return c, invalid("mode must be %s or %s", ModeAddOnly, ModeSync)
	}
	if c.group == inventory.GroupAll || c.group == inventory.GroupUngrouped || !inventory.ValidGroupName(c.group) {
		return c, invalid("group %q is not a valid target group name", c.group)
	}
	if _, err := s.inv.Get(ctx, ws, c.inv); err != nil {
		if errors.Is(err, inventory.ErrNotFound) {
			return c, invalid("inventory does not exist in this workspace")
		}
		return c, err
	}
	return c, nil
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}

// Create adds a rule.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in Input, by uuid.UUID) (Rule, error) {
	c := clean{name: in.Name, kind: in.Kind, params: in.Params, inv: in.InventoryID, group: in.Group, mode: in.Mode, enabled: true}
	if c.mode == "" {
		c.mode = ModeAddOnly
	}
	if in.Enabled != nil {
		c.enabled = *in.Enabled
	}
	c, err := s.validate(ctx, ws, c)
	if err != nil {
		return Rule{}, err
	}
	row, err := s.q.CreateGroupRule(ctx, dbgen.CreateGroupRuleParams{WorkspaceID: ws, Name: c.name, Kind: c.kind, Params: c.params,
		InventoryID: c.inv, GroupName: c.group, Mode: c.mode, Enabled: c.enabled, CreatedBy: pgtype.UUID{Bytes: by, Valid: by != uuid.Nil}})
	if err != nil {
		return Rule{}, fmt.Errorf("create group rule: %w", mapErr(err))
	}
	return toRule(row), nil
}

// Get returns one rule.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Rule, error) {
	row, err := s.q.GetGroupRule(ctx, dbgen.GetGroupRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Rule{}, mapErr(err)
	}
	return toRule(row), nil
}

// List returns the rules of a workspace by name.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Rule, error) {
	rows, err := s.q.ListGroupRules(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list group rules: %w", err)
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRule(r))
	}
	return out, nil
}

// Update changes a rule. Changing the target inventory or group forgets which
// memberships the rule added (they belonged to the old target).
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in Update) (Rule, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Rule{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := s.q.WithTx(tx)
	cur, err := q.LockGroupRule(ctx, dbgen.LockGroupRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return Rule{}, mapErr(err)
	}
	c := clean{name: cur.Name, kind: cur.Kind, params: cur.Params, inv: cur.InventoryID, group: cur.GroupName, mode: cur.Mode, enabled: cur.Enabled}
	if in.Name != nil {
		c.name = *in.Name
	}
	if in.Kind != nil {
		c.kind = *in.Kind
		if in.Params == nil {
			c.params = nil
		}
	}
	if in.Params != nil {
		c.params = *in.Params
	}
	if in.InventoryID != nil {
		c.inv = *in.InventoryID
	}
	if in.Group != nil {
		c.group = *in.Group
	}
	if in.Mode != nil {
		c.mode = *in.Mode
	}
	if in.Enabled != nil {
		c.enabled = *in.Enabled
	}
	c, err = s.validate(ctx, ws, c)
	if err != nil {
		return Rule{}, err
	}
	row, err := q.UpdateGroupRule(ctx, dbgen.UpdateGroupRuleParams{ID: id, WorkspaceID: ws, Name: c.name, Kind: c.kind, Params: c.params,
		InventoryID: c.inv, GroupName: c.group, Mode: c.mode, Enabled: c.enabled})
	if err != nil {
		return Rule{}, fmt.Errorf("update group rule: %w", mapErr(err))
	}
	if c.inv != cur.InventoryID || c.group != cur.GroupName {
		if err := q.ClearGroupRuleMembers(ctx, id); err != nil {
			return Rule{}, fmt.Errorf("clear tracked members: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Rule{}, fmt.Errorf("commit: %w", err)
	}
	return toRule(row), nil
}

// Delete removes a rule. Hosts it added to inventories stay where they are.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.q.DeleteGroupRule(ctx, dbgen.DeleteGroupRuleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete group rule: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) fleetHosts(ctx context.Context, ws uuid.UUID) ([]Host, error) {
	list, err := s.fl.Hosts(ctx, ws, fleet.HostFilter{SkipStatus: true}, nil)
	if err != nil {
		return nil, fmt.Errorf("list fleet hosts: %w", err)
	}
	out := make([]Host, 0, len(list))
	for _, h := range list {
		out = append(out, Host{ID: h.ID, Name: h.Name, Hostname: h.Hostname, FQDN: h.FQDN, IPs: h.IPs, Tags: h.Tags, Attrs: h.Attrs})
	}
	return out, nil
}

// Preview is the dry run of a rule.
type Preview struct {
	Rule          Rule   `json:"rule"`
	InventoryMode string `json:"inventory_mode"`
	Plan          Plan   `json:"plan"`
}

// Preview computes what applying the rule would change. Nothing is written.
func (s *Service) Preview(ctx context.Context, ws, id uuid.UUID) (Preview, error) {
	rule, err := s.Get(ctx, ws, id)
	if err != nil {
		return Preview{}, err
	}
	m, _, err := ParseMatcher(rule.Kind, rule.Params)
	if err != nil {
		return Preview{}, err
	}
	inv, err := s.inv.Get(ctx, ws, rule.InventoryID)
	if err != nil {
		return Preview{}, fmt.Errorf("load inventory: %w", err)
	}
	st, err := s.inv.GroupState(ctx, ws, rule.InventoryID, rule.Group)
	if err != nil {
		return Preview{}, fmt.Errorf("load group state: %w", err)
	}
	tracked, err := s.q.ListGroupRuleMembers(ctx, id)
	if err != nil {
		return Preview{}, fmt.Errorf("list tracked members: %w", err)
	}
	hosts, err := s.fleetHosts(ctx, ws)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Rule: rule, InventoryMode: inv.Mode,
		Plan: Diff(DiffInput{Matcher: m, Mode: rule.Mode, Hosts: hosts, State: st, Tracked: tracked})}, nil
}

// ApplyInput identifies who applies a rule. Author is used for the Git commit
// of git inventories.
type ApplyInput struct {
	User    uuid.UUID
	Author  projects.Author
	IfMatch *int
}

// Applied is the outcome of Apply.
type Applied struct {
	Rule          Rule                        `json:"rule"`
	InventoryMode string                      `json:"inventory_mode"`
	Plan          Plan                        `json:"plan"`
	Result        inventory.GroupChangeResult `json:"result"`
}

type editor struct {
	ws    uuid.UUID
	rule  Rule
	m     Matcher
	hosts []Host
	user  uuid.UUID
	plan  Plan
}

func (e *editor) Plan(ctx context.Context, q *dbgen.Queries, st inventory.GroupState) (inventory.GroupChange, error) {
	cur, err := q.GetGroupRule(ctx, dbgen.GetGroupRuleParams{ID: e.rule.ID, WorkspaceID: e.ws})
	if err != nil {
		return inventory.GroupChange{}, mapErr(err)
	}
	if !cur.UpdatedAt.Time.Equal(e.rule.UpdatedAt) {
		return inventory.GroupChange{}, ErrRuleChanged
	}
	tracked, err := q.ListGroupRuleMembers(ctx, e.rule.ID)
	if err != nil {
		return inventory.GroupChange{}, fmt.Errorf("list tracked members: %w", err)
	}
	e.plan = Diff(DiffInput{Matcher: e.m, Mode: e.rule.Mode, Hosts: e.hosts, State: st, Tracked: tracked})
	ch := inventory.GroupChange{Remove: e.plan.Remove}
	for _, a := range e.plan.Add {
		ch.Add = append(ch.Add, inventory.HostAdd{Name: a.Name, AnsibleHost: a.AnsibleHost})
	}
	return ch, nil
}

func (e *editor) Done(ctx context.Context, q *dbgen.Queries, res inventory.GroupChangeResult) error {
	for _, n := range res.Added {
		if err := q.AddGroupRuleMember(ctx, dbgen.AddGroupRuleMemberParams{RuleID: e.rule.ID, HostName: n}); err != nil {
			return fmt.Errorf("track member: %w", err)
		}
	}
	gone := append(append([]string{}, res.Removed...), e.plan.Untrack...)
	if len(gone) > 0 {
		if err := q.DeleteGroupRuleMembers(ctx, dbgen.DeleteGroupRuleMembersParams{RuleID: e.rule.ID, HostNames: gone}); err != nil {
			return fmt.Errorf("untrack members: %w", err)
		}
	}
	if err := q.MarkGroupRuleApplied(ctx, dbgen.MarkGroupRuleAppliedParams{ID: e.rule.ID, LastAppliedBy: pgtype.UUID{Bytes: e.user, Valid: e.user != uuid.Nil}}); err != nil {
		return fmt.Errorf("mark applied: %w", err)
	}
	return nil
}

// Apply executes the rule: with the target inventory locked it recomputes the
// diff from the current state, writes it (for git inventories also commits it
// to the write branch, never forced) and records the memberships the rule
// added. Everything happens in one transaction; a git conflict or any error
// leaves the inventory and the tracking untouched.
func (s *Service) Apply(ctx context.Context, ws, id uuid.UUID, in ApplyInput) (Applied, error) {
	rule, err := s.Get(ctx, ws, id)
	if err != nil {
		return Applied{}, err
	}
	if !rule.Enabled {
		return Applied{}, ErrDisabled
	}
	m, _, err := ParseMatcher(rule.Kind, rule.Params)
	if err != nil {
		return Applied{}, err
	}
	inv, err := s.inv.Get(ctx, ws, rule.InventoryID)
	if err != nil {
		return Applied{}, fmt.Errorf("load inventory: %w", err)
	}
	hosts, err := s.fleetHosts(ctx, ws)
	if err != nil {
		return Applied{}, err
	}
	ed := &editor{ws: ws, rule: rule, m: m, hosts: hosts, user: in.User}
	res, err := s.inv.ApplyGroupChange(ctx, ws, rule.InventoryID, inventory.GroupApply{
		Group: rule.Group, Editor: ed, IfMatch: in.IfMatch,
		Commit: inventory.CommitInput{Message: "Apply group rule " + rule.Name + " via ansible-control-plane", Author: in.Author},
	})
	if err != nil {
		return Applied{}, err
	}
	for _, l := range []*[]string{&res.HostsCreated, &res.Added, &res.Removed} {
		if *l == nil {
			*l = []string{}
		}
	}
	if after, gerr := s.Get(ctx, ws, id); gerr == nil {
		rule = after
	}
	return Applied{Rule: rule, InventoryMode: inv.Mode, Plan: ed.plan, Result: res}, nil
}
