package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/grouprules"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const groupRuleUsage = "usage: acp admin group-rule <list --workspace SLUG | preview --workspace SLUG --rule NAME|ID | apply --workspace SLUG --rule NAME|ID [--author-name NAME --author-email EMAIL]>"

type groupRuleArgs struct {
	Sub, Workspace, Rule, AuthorName, AuthorEmail string
}

func parseGroupRuleArgs(args []string, stderr io.Writer) (groupRuleArgs, error) {
	if len(args) == 0 {
		return groupRuleArgs{}, errors.New(groupRuleUsage)
	}
	a := groupRuleArgs{Sub: args[0]}
	fs := flag.NewFlagSet("group-rule "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch a.Sub {
	case "list":
	case "preview":
		fs.StringVar(&a.Rule, "rule", "", "rule name or id")
	case "apply":
		fs.StringVar(&a.Rule, "rule", "", "rule name or id")
		fs.StringVar(&a.AuthorName, "author-name", "ACP admin", "Git author name (git inventories)")
		fs.StringVar(&a.AuthorEmail, "author-email", "acp-admin@localhost", "Git author email (git inventories)")
	default:
		return groupRuleArgs{}, fmt.Errorf("unknown group-rule command %q\n%s", a.Sub, groupRuleUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return groupRuleArgs{}, err
	}
	if fs.NArg() > 0 {
		return groupRuleArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return groupRuleArgs{}, errors.New("--workspace is required")
	}
	if a.Sub != "list" && a.Rule == "" {
		return groupRuleArgs{}, errors.New("--rule is required")
	}
	return a, nil
}

func findGroupRule(ctx context.Context, svc *grouprules.Service, ws uuid.UUID, ref string) (grouprules.Rule, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return svc.Get(ctx, ws, id)
	}
	list, err := svc.List(ctx, ws)
	if err != nil {
		return grouprules.Rule{}, err
	}
	for _, r := range list {
		if r.Name == ref {
			return r, nil
		}
	}
	return grouprules.Rule{}, grouprules.ErrNotFound
}

// formatPlan renders a plan as lines; host names only, no variables.
func formatPlan(p grouprules.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "matched: %d\ngroup_create: %t\nkeep: %d\n", p.Matched, p.GroupCreate, p.Keep)
	for _, a := range p.Add {
		fmt.Fprintf(&b, "add: %s create_host=%t ansible_host=%s\n", a.Name, a.CreateHost, dash(a.AnsibleHost))
	}
	for _, r := range p.Remove {
		fmt.Fprintf(&b, "remove: %s\n", r)
	}
	for _, u := range p.Untrack {
		fmt.Fprintf(&b, "untrack: %s\n", u)
	}
	for _, s := range p.Skipped {
		fmt.Fprintf(&b, "skipped: %s (%s)\n", dash(s.Name), s.Reason)
	}
	return b.String()
}

// runAdminGroupRule implements `acp admin group-rule`. Apply is explicit and
// goes through the same service as the REST API (transactional, git inventories
// via write-back). Apply writes an audit entry with actor "cli".
func runAdminGroupRule(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseGroupRuleArgs(args, stderr)
	if err != nil {
		return err
	}
	sp, err := secrets.NewLocalProviderFromFile(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	q := dbgen.New(pool)
	prj := projects.New(projects.Options{Store: q, Secrets: secrets.NewService(q, sp), Git: &gitops.Client{}, Scanner: scan.JSONScanner{}, DataDir: cfg.DataDir})
	inv := inventory.New(inventory.Options{DB: pool, Checkouts: prj, Writer: prj})
	ws, err := (&admin.InventoryOps{Q: q}).Workspace(ctx, a.Workspace)
	if err != nil {
		return err
	}
	svc := grouprules.New(grouprules.Options{DB: pool, Fleet: fleet.NewStore(pool, nil), Inventories: inv})
	if a.Sub == "list" {
		list, err := svc.List(ctx, ws)
		if err != nil {
			return err
		}
		for _, r := range list {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\tinventory=%s\tgroup=%s\tmode=%s\tenabled=%t\n", r.ID, r.Name, r.Kind, r.Params, r.InventoryID, r.Group, r.Mode, r.Enabled)
		}
		return nil
	}
	rule, err := findGroupRule(ctx, svc, ws, a.Rule)
	if err != nil {
		return fmt.Errorf("group rule %q: %w", a.Rule, err)
	}
	if a.Sub == "preview" {
		pv, err := svc.Preview(ctx, ws, rule.ID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "rule: %s\ninventory_mode: %s\n%s", rule.Name, pv.InventoryMode, formatPlan(pv.Plan))
		return nil
	}
	out, err := svc.Apply(ctx, ws, rule.ID, grouprules.ApplyInput{Author: projects.Author{Name: a.AuthorName, Email: a.AuthorEmail}})
	if err != nil {
		return err
	}
	details := map[string]any{"source": "cli", "inventory_id": rule.InventoryID, "group": rule.Group, "mode": rule.Mode,
		"group_created": out.Result.GroupCreated, "hosts_created": out.Result.HostsCreated,
		"added": out.Result.Added, "removed": out.Result.Removed}
	if c := out.Result.Commit; c != nil && !c.NoOp {
		details["commit"] = map[string]any{"branch": c.Branch, "sha": c.SHA, "paths": c.Paths}
	}
	cliAuditTarget(ctx, q, ws, "group_rule.apply", "group_rule", rule.ID.String(), details)
	_, _ = fmt.Fprintf(stdout, "rule: %s\ninventory_mode: %s\ngroup_created: %t\nhosts_created: %d\nadded: %d\nremoved: %d\nversion: %d\n",
		rule.Name, out.InventoryMode, out.Result.GroupCreated, len(out.Result.HostsCreated), len(out.Result.Added), len(out.Result.Removed), out.Result.Version)
	if c := out.Result.Commit; c != nil && !c.NoOp {
		_, _ = fmt.Fprintf(stdout, "commit: %s\nbranch: %s\n", c.SHA, c.Branch)
	}
	return nil
}
