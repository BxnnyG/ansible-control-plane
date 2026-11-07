package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/remediation"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const remediationUsage = "usage: acp admin remediation <list --workspace SLUG | create --workspace SLUG --name NAME --template NAME|ID --target host|group|network [--group NAME] [--connector-type TYPE] [--service-regex RE] [--summary-regex RE] [--min-severity SEV] [--check-first] [--disabled]>" +
	"\n       acp admin alert remediate --workspace SLUG --alert ID --rule NAME|ID [--skip-check]"

type remediationArgs struct {
	Group                                   string // remediation or alert
	Sub, Workspace, Name, Template, Target  string
	RuleGroup, ConnectorType                string
	ServiceRegex, SummaryRegex, MinSeverity string
	CheckFirst, Disabled, SkipCheck         bool
	Alert, Rule                             string
}

func parseRemediationArgs(group string, args []string, stderr io.Writer) (remediationArgs, error) {
	if len(args) == 0 {
		return remediationArgs{}, errors.New(remediationUsage)
	}
	a := remediationArgs{Group: group, Sub: args[0]}
	fs := flag.NewFlagSet(group+" "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch group + " " + a.Sub {
	case "remediation list":
	case "remediation create":
		fs.StringVar(&a.Name, "name", "", "rule name")
		fs.StringVar(&a.Template, "template", "", "template name or id")
		fs.StringVar(&a.Target, "target", "", "limit target: host, group or network")
		fs.StringVar(&a.RuleGroup, "group", "", "inventory group (target group)")
		fs.StringVar(&a.ConnectorType, "connector-type", "", "only alerts of this connector type")
		fs.StringVar(&a.ServiceRegex, "service-regex", "", "RE2 matched against the alert service")
		fs.StringVar(&a.SummaryRegex, "summary-regex", "", "RE2 matched against the alert summary")
		fs.StringVar(&a.MinSeverity, "min-severity", "info", "minimum severity: critical, warning, unknown or info")
		fs.BoolVar(&a.CheckFirst, "check-first", false, "start with a check-mode run")
		fs.BoolVar(&a.Disabled, "disabled", false, "create the rule disabled")
	case "alert remediate":
		fs.StringVar(&a.Alert, "alert", "", "alert id")
		fs.StringVar(&a.Rule, "rule", "", "rule name or id")
		fs.BoolVar(&a.SkipCheck, "skip-check", false, "start the real run even if the rule asks for a check first")
	default:
		return remediationArgs{}, fmt.Errorf("unknown %s command %q\n%s", group, a.Sub, remediationUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return remediationArgs{}, err
	}
	if fs.NArg() > 0 {
		return remediationArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return remediationArgs{}, errors.New("--workspace is required")
	}
	switch {
	case a.Group == "remediation" && a.Sub == "create" && (a.Name == "" || a.Template == "" || a.Target == ""):
		return remediationArgs{}, errors.New("--name, --template and --target are required")
	case a.Group == "alert" && (a.Alert == "" || a.Rule == ""):
		return remediationArgs{}, errors.New("--alert and --rule are required")
	}
	return a, nil
}

// runAdminRemediation implements `acp admin remediation` and `acp admin alert`.
// A fix is an explicit operator action with workspace authority (no scope
// check, like `acp admin run start`); create and remediate write audit
// entries with actor "cli".
func runAdminRemediation(ctx context.Context, group string, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseRemediationArgs(group, args, stderr)
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
	ws, err := (&admin.InventoryOps{Q: q}).Workspace(ctx, a.Workspace)
	if err != nil {
		return err
	}
	inv := inventory.New(inventory.Options{DB: pool})
	rs := runs.New(runs.Options{DB: pool, Graphs: inv, Sealer: sp})
	svc := remediation.New(remediation.Options{DB: pool, Fleet: fleet.NewStore(pool, nil), Runs: rs, Graphs: inv})
	switch group + " " + a.Sub {
	case "remediation list":
		list, err := svc.List(ctx, ws)
		if err != nil {
			return err
		}
		for _, r := range list {
			ct := "*"
			if r.ConnectorType != nil {
				ct = *r.ConnectorType
			}
			_, _ = fmt.Fprintf(stdout, "%s\t%s\ttemplate=%s\ttarget=%s\tgroup=%s\tconnector=%s\tmin_severity=%s\tcheck_first=%t\tenabled=%t\n",
				r.ID, r.Name, r.Template, r.Target, dash(r.Group), ct, r.MinSeverity, r.CheckFirst, r.Enabled)
		}
		return nil
	case "remediation create":
		tpl, err := findTemplate(ctx, rs, ws, a.Template)
		if err != nil {
			return err
		}
		in := remediation.Input{Name: a.Name, ServiceRegex: a.ServiceRegex, SummaryRegex: a.SummaryRegex, MinSeverity: a.MinSeverity,
			TemplateID: tpl, Target: a.Target, Group: a.RuleGroup, CheckFirst: a.CheckFirst}
		if a.ConnectorType != "" {
			in.ConnectorType = &a.ConnectorType
		}
		if a.Disabled {
			off := false
			in.Enabled = &off
		}
		r, err := svc.Create(ctx, ws, in, uuid.Nil)
		if err != nil {
			return err
		}
		cliAuditTarget(ctx, q, ws, "remediation.rule.create", "remediation_rule", r.ID.String(),
			map[string]any{"source": "cli", "name": r.Name, "template_id": tpl, "target": a.Target, "group": a.RuleGroup, "enabled": !a.Disabled})
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", r.ID, r.Name)
		return nil
	}
	alertID, err := uuid.Parse(a.Alert)
	if err != nil {
		return fmt.Errorf("--alert: not a UUID")
	}
	rule, err := findRemediationRule(ctx, svc, ws, a.Rule)
	if err != nil {
		return err
	}
	res, err := svc.Remediate(ctx, ws, alertID, rule.ID, a.SkipCheck, runs.Actor{}, nil)
	if err != nil {
		return err
	}
	cliAuditTarget(ctx, q, ws, "remediation.run", "run", res.Run.ID.String(), map[string]any{
		"source": "cli", "rule_id": rule.ID, "alert_id": alertID, "phase": res.Phase, "template_id": res.Run.TemplateID,
		"limit": res.Limit, "hosts": len(res.Hosts), "check": res.Run.Params.Check, "skip_check": a.SkipCheck})
	_, _ = fmt.Fprintf(stdout, "run: %s\nphase: %s\nlimit: %s\nhosts: %d\n", res.Run.ID, res.Phase, res.Limit, len(res.Hosts))
	return nil
}

func findTemplate(ctx context.Context, rs *runs.Service, ws uuid.UUID, ref string) (uuid.UUID, error) {
	list, err := rs.ListTemplates(ctx, ws)
	if err != nil {
		return uuid.Nil, err
	}
	for _, t := range list {
		if t.Name == ref || t.ID.String() == ref {
			return t.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("template %q not found", ref)
}

func findRemediationRule(ctx context.Context, svc *remediation.Service, ws uuid.UUID, ref string) (remediation.Rule, error) {
	list, err := svc.List(ctx, ws)
	if err != nil {
		return remediation.Rule{}, err
	}
	for _, r := range list {
		if r.Name == ref || r.ID.String() == ref {
			return r, nil
		}
	}
	return remediation.Rule{}, fmt.Errorf("remediation rule %q not found", ref)
}
