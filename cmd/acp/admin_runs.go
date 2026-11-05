package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const (
	templateUsage = "usage: acp admin template <list [--workspace SLUG] | create [--workspace SLUG] --name NAME --project SLUG|ID --playbook PATH --inventory NAME|ID [--ssh-key-secret NAME] [--become-secret NAME] [--vault-identity LABEL]... [--limit PATTERN] [--max-concurrent N]>"
	runUsage      = "usage: acp admin run <start [--workspace SLUG] --template NAME|ID [--limit P] [--tags T] [--check] [--diff] [-e key=value]... [--wait] | show [--workspace SLUG] [--log] [--tail N] RUN_ID | list [--workspace SLUG] [--template NAME|ID] [--status S] [--limit N]>"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// runsArgs are the validated flags of `acp admin template` and `acp admin run`.
type runsArgs struct {
	Group, Sub, Workspace string

	// template create
	Name, Project, Playbook, Inventory string
	SSHSecret, BecomeSecret            string
	VaultIdentities                    stringList
	MaxConcurrent                      int

	// run start / show / list
	Template, Limit, Tags, Status  string
	Check, Diff, CheckSet, DiffSet bool
	ExtraVars                      stringList
	Wait, Log                      bool
	Tail, Count                    int
	RunID                          uuid.UUID
}

// parseRunsArgs parses and validates the flags; it does not touch the database.
func parseRunsArgs(group string, args []string, stderr io.Writer) (runsArgs, error) {
	usage := templateUsage
	if group == "run" {
		usage = runUsage
	}
	if len(args) == 0 {
		return runsArgs{}, errors.New(usage)
	}
	a := runsArgs{Group: group, Sub: args[0]}
	fs := flag.NewFlagSet(group+" "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "default", "workspace slug")
	switch group + " " + a.Sub {
	case "template list":
	case "template create":
		fs.StringVar(&a.Name, "name", "", "template name")
		fs.StringVar(&a.Project, "project", "", "project slug or id")
		fs.StringVar(&a.Playbook, "playbook", "", "playbook path in the repository")
		fs.StringVar(&a.Inventory, "inventory", "", "inventory name, slug or id")
		fs.StringVar(&a.SSHSecret, "ssh-key-secret", "", "name of the SSH key (or password) secret")
		fs.StringVar(&a.BecomeSecret, "become-secret", "", "name of the become password secret")
		fs.Var(&a.VaultIdentities, "vault-identity", "vault identity label (repeatable)")
		fs.StringVar(&a.Limit, "limit", "", "default limit pattern")
		fs.IntVar(&a.MaxConcurrent, "max-concurrent", 0, "maximum concurrent runs (default 1)")
	case "run start":
		fs.StringVar(&a.Template, "template", "", "template name, slug or id")
		fs.StringVar(&a.Limit, "limit", "", "limit pattern override")
		fs.StringVar(&a.Tags, "tags", "", "tags override")
		fs.BoolVar(&a.Check, "check", false, "check mode")
		fs.BoolVar(&a.Diff, "diff", false, "diff mode")
		fs.Var(&a.ExtraVars, "e", "extra variable key=value (repeatable)")
		fs.BoolVar(&a.Wait, "wait", false, "follow the log until the run finishes; exit non-zero on failure")
	case "run show":
		fs.BoolVar(&a.Log, "log", false, "print the run log")
		fs.IntVar(&a.Tail, "tail", 0, "print only the last N log lines")
	case "run list":
		fs.StringVar(&a.Template, "template", "", "template name, slug or id")
		fs.StringVar(&a.Status, "status", "", "status filter")
		fs.IntVar(&a.Count, "limit", 20, "maximum number of runs")
	default:
		return runsArgs{}, fmt.Errorf("unknown %s command %q\n%s", group, a.Sub, usage)
	}
	// Allow the run id before or after the flags (`run show ID --log`).
	rest := args[1:]
	var pos []string
	for {
		if err := fs.Parse(rest); err != nil {
			return runsArgs{}, err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "check":
			a.CheckSet = true
		case "diff":
			a.DiffSet = true
		}
	})
	if group+" "+a.Sub == "run show" {
		if len(pos) != 1 {
			return runsArgs{}, errors.New("run show needs exactly one run id")
		}
		id, err := uuid.Parse(pos[0])
		if err != nil {
			return runsArgs{}, fmt.Errorf("invalid run id %q", pos[0])
		}
		a.RunID = id
	} else if len(pos) > 0 {
		return runsArgs{}, fmt.Errorf("unexpected argument %q", pos[0])
	}
	if strings.TrimSpace(a.Workspace) == "" {
		return runsArgs{}, errors.New("--workspace must not be empty")
	}
	switch group + " " + a.Sub {
	case "template create":
		for _, r := range []struct{ v, n string }{{a.Name, "name"}, {a.Project, "project"}, {a.Playbook, "playbook"}, {a.Inventory, "inventory"}} {
			if strings.TrimSpace(r.v) == "" {
				return runsArgs{}, fmt.Errorf("--%s is required", r.n)
			}
		}
		if a.MaxConcurrent < 0 || a.MaxConcurrent > 100 {
			return runsArgs{}, errors.New("--max-concurrent must be between 1 and 100")
		}
	case "run start":
		if a.Template == "" {
			return runsArgs{}, errors.New("--template is required")
		}
		if _, err := parseExtraVars(a.ExtraVars); err != nil {
			return runsArgs{}, err
		}
	case "run show":
		if a.Tail < 0 {
			return runsArgs{}, errors.New("--tail must not be negative")
		}
		if a.Tail > 0 {
			a.Log = true
		}
	case "run list":
		if a.Count <= 0 || a.Count > runs.MaxPageSize {
			return runsArgs{}, fmt.Errorf("--limit must be between 1 and %d", runs.MaxPageSize)
		}
	}
	return a, nil
}

// parseExtraVars splits key=value pairs; values stay strings.
func parseExtraVars(kv []string) (map[string]any, error) {
	if len(kv) == 0 {
		return nil, nil
	}
	m := make(map[string]any, len(kv))
	for _, s := range kv {
		k, v, ok := strings.Cut(s, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid -e %q: want key=value", s)
		}
		m[strings.TrimSpace(k)] = v
	}
	return m, nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// stripANSI removes terminal escape sequences from log text.
func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// tailLines returns the last n lines of s (all of it if n <= 0).
func tailLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func isFinal(status string) bool {
	switch status {
	case runs.StatusSucceeded, runs.StatusFailed, runs.StatusCancelled, runs.StatusLost:
		return true
	}
	return false
}

func tsStr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// runsEnv bundles the dependencies of the template and run commands.
type runsEnv struct {
	q   *dbgen.Queries
	svc *runs.Service
	inv *inventory.Service
	prj *projects.Service
	ws  uuid.UUID
}

func (e *runsEnv) audit(ctx context.Context, action, targetType, targetID string, details map[string]any) error {
	b, _ := json.Marshal(details)
	if _, err := e.q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: action, TargetType: targetType, TargetID: targetID,
		WorkspaceID: pgtype.UUID{Bytes: e.ws, Valid: true}, Details: b,
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}

func (e *runsEnv) project(ctx context.Context, ref string) (uuid.UUID, error) {
	list, err := e.prj.List(ctx, e.ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range list {
		if p.Slug == ref || p.ID.String() == ref {
			return p.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("%w: %q", admin.ErrNoSuchProject, ref)
}

func (e *runsEnv) inventory(ctx context.Context, ref string) (uuid.UUID, error) {
	list, err := e.inv.List(ctx, e.ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list inventories: %w", err)
	}
	for _, i := range list {
		if i.Name == ref || i.Slug == ref || i.ID.String() == ref {
			return i.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("inventory not found: %q", ref)
}

func (e *runsEnv) secret(ctx context.Context, name string) (uuid.UUID, error) {
	list, err := e.q.ListSecretsMeta(ctx, e.ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list secrets: %w", err)
	}
	for _, s := range list {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("secret not found: %q", name)
}

func (e *runsEnv) vault(ctx context.Context, label string) (uuid.UUID, error) {
	list, err := e.q.ListVaultIdentities(ctx, e.ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list vault identities: %w", err)
	}
	for _, v := range list {
		if v.Label == label {
			return v.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("vault identity not found: %q", label)
}

func (e *runsEnv) template(ctx context.Context, ref string) (runs.Template, error) {
	list, err := e.svc.ListTemplates(ctx, e.ws)
	if err != nil {
		return runs.Template{}, err
	}
	for _, t := range list {
		if t.Name == ref || t.Slug == ref || t.ID.String() == ref {
			return t, nil
		}
	}
	return runs.Template{}, fmt.Errorf("template not found: %q", ref)
}

// runAdminRuns implements `acp admin template` and `acp admin run`. Secret
// values and secret survey variables are never printed.
func runAdminRuns(ctx context.Context, group string, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseRunsArgs(group, args, stderr)
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
	e := &runsEnv{q: q, prj: prj, inv: inv, ws: ws, svc: runs.New(runs.Options{DB: pool, Graphs: inv, Sealer: sp})}
	switch group + " " + a.Sub {
	case "template list":
		return e.templateList(ctx, stdout)
	case "template create":
		return e.templateCreate(ctx, a, stdout)
	case "run start":
		return e.runStart(ctx, a, stdout, stderr)
	case "run show":
		return e.runShow(ctx, a, stdout)
	default:
		return e.runList(ctx, a, stdout)
	}
}

func (e *runsEnv) templateList(ctx context.Context, stdout io.Writer) error {
	list, err := e.svc.ListTemplates(ctx, e.ws)
	if err != nil {
		return err
	}
	for _, t := range list {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\tplaybook=%s\tmax_concurrent=%d\n", t.ID, t.Slug, t.Name, t.Playbook, t.MaxConcurrent)
	}
	return nil
}

func (e *runsEnv) templateCreate(ctx context.Context, a runsArgs, stdout io.Writer) error {
	pid, err := e.project(ctx, a.Project)
	if err != nil {
		return err
	}
	iid, err := e.inventory(ctx, a.Inventory)
	if err != nil {
		return err
	}
	in := runs.TemplateInput{Name: &a.Name, ProjectID: &pid, InventoryID: &iid, Playbook: &a.Playbook}
	if a.SSHSecret != "" {
		id, err := e.secret(ctx, a.SSHSecret)
		if err != nil {
			return err
		}
		in.SSHSecretID = projects.Field[uuid.UUID]{Set: true, Value: &id}
	}
	if a.BecomeSecret != "" {
		id, err := e.secret(ctx, a.BecomeSecret)
		if err != nil {
			return err
		}
		in.BecomeSecretID = projects.Field[uuid.UUID]{Set: true, Value: &id}
	}
	if len(a.VaultIdentities) > 0 {
		ids := make([]uuid.UUID, 0, len(a.VaultIdentities))
		for _, l := range a.VaultIdentities {
			id, err := e.vault(ctx, l)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		in.VaultIdentityIDs = &ids
	}
	if a.Limit != "" {
		in.DefaultLimit = &a.Limit
	}
	if a.MaxConcurrent > 0 {
		in.MaxConcurrent = &a.MaxConcurrent
	}
	t, err := e.svc.CreateTemplate(ctx, e.ws, in)
	if err != nil {
		return err
	}
	if err := e.audit(ctx, "template.create", "template", t.ID.String(), map[string]any{"source": "cli", "name": t.Name, "playbook": t.Playbook}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "template_id: %s\nslug: %s\nname: %s\n", t.ID, t.Slug, t.Name)
	return nil
}

func (e *runsEnv) runStart(ctx context.Context, a runsArgs, stdout, stderr io.Writer) error {
	t, err := e.template(ctx, a.Template)
	if err != nil {
		return err
	}
	vars, err := parseExtraVars(a.ExtraVars)
	if err != nil {
		return err
	}
	ov := runs.Overrides{ExtraVars: vars}
	if a.Limit != "" {
		ov.Limit = &a.Limit
	}
	if a.Tags != "" {
		ov.Tags = &a.Tags
	}
	if a.CheckSet {
		ov.Check = &a.Check
	}
	if a.DiffSet {
		ov.Diff = &a.Diff
	}
	// The CLI has workspace authority: trigger "api" without a principal,
	// no triggered_by user; the audit entry records actor "cli".
	run, err := e.svc.Enqueue(ctx, e.ws, t.ID, ov, runs.TriggerAPI, runs.Actor{}, nil)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	if err := e.audit(ctx, "run.start", "run", run.ID.String(), map[string]any{"source": "cli", "template": t.Name, "extra_var_keys": keys}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "run_id: %s\n", run.ID)
	if !a.Wait {
		return nil
	}
	return e.follow(ctx, run.ID, stdout, stderr)
}

// follow prints log chunks until the run is final; a non-succeeded run is an error.
func (e *runsEnv) follow(ctx context.Context, id uuid.UUID, stdout, stderr io.Writer) error {
	var last int64
	for {
		d, err := e.svc.GetRun(ctx, e.ws, id)
		if err != nil {
			return err
		}
		// Read the status before the chunks so that the final drain is complete.
		done := isFinal(d.Status)
		for {
			chunks, err := e.q.ListRunLogChunks(ctx, dbgen.ListRunLogChunksParams{RunID: id, Seq: last, Limit: 500})
			if err != nil {
				return fmt.Errorf("read log: %w", err)
			}
			for _, c := range chunks {
				_, _ = io.WriteString(stdout, stripANSI(c.Data))
				last = c.Seq
			}
			if len(chunks) < 500 {
				break
			}
		}
		if done {
			_, _ = fmt.Fprintf(stdout, "\nstatus: %s\n", d.Status)
			if d.Status != runs.StatusSucceeded {
				return fmt.Errorf("run %s finished with status %s", id, d.Status)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (e *runsEnv) readLog(ctx context.Context, id uuid.UUID) (string, error) {
	var sb strings.Builder
	var last int64
	for {
		chunks, err := e.q.ListRunLogChunks(ctx, dbgen.ListRunLogChunksParams{RunID: id, Seq: last, Limit: 500})
		if err != nil {
			return "", fmt.Errorf("read log: %w", err)
		}
		for _, c := range chunks {
			sb.WriteString(c.Data)
			last = c.Seq
		}
		if len(chunks) < 500 {
			return stripANSI(sb.String()), nil
		}
	}
}

func (e *runsEnv) runShow(ctx context.Context, a runsArgs, stdout io.Writer) error {
	d, err := e.svc.GetRun(ctx, e.ws, a.RunID)
	if err != nil {
		return err
	}
	exit := "-"
	if d.ExitCode != nil {
		exit = fmt.Sprint(*d.ExitCode)
	}
	by := "-"
	if d.TriggeredByName != nil {
		by = *d.TriggeredByName
	}
	_, _ = fmt.Fprintf(stdout, "run_id: %s\ntemplate: %s\nstatus: %s\ntrigger: %s\ntriggered_by: %s\nrevision: %s\ncreated: %s\nstarted: %s\nfinished: %s\nexit_code: %s\nhosts: ok=%d changed=%d failed=%d unreachable=%d\n",
		d.ID, d.TemplateName, d.Status, d.Trigger, by, d.ProjectRevision, tsStr(&d.CreatedAt), tsStr(d.StartedAt), tsStr(d.FinishedAt), exit,
		d.HostsOK, d.HostsChanged, d.HostsFailed, d.HostsUnreachable)
	for _, h := range d.HostResults {
		_, _ = fmt.Fprintf(stdout, "host\t%s\tok=%d\tchanged=%d\tfailed=%d\tunreachable=%d\tskipped=%d\n", h.Host, h.OK, h.Changed, h.Failed, h.Unreachable, h.Skipped)
	}
	if !a.Log {
		return nil
	}
	text, err := e.readLog(ctx, a.RunID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "--- log ---")
	_, _ = io.WriteString(stdout, tailLines(text, a.Tail))
	return nil
}

func (e *runsEnv) runList(ctx context.Context, a runsArgs, stdout io.Writer) error {
	f := runs.Filter{Status: a.Status, Limit: a.Count}
	if a.Template != "" {
		t, err := e.template(ctx, a.Template)
		if err != nil {
			return err
		}
		f.TemplateID = &t.ID
	}
	page, err := e.svc.ListRuns(ctx, e.ws, f)
	if err != nil {
		return err
	}
	for _, r := range page.Items {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\tok=%d changed=%d failed=%d unreachable=%d\n", r.ID, r.Status, r.TemplateName, r.Trigger, tsStr(&r.CreatedAt), r.HostsOK, r.HostsChanged, r.HostsFailed, r.HostsUnreachable)
	}
	return nil
}
