package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/notify"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const notificationUsage = "usage: acp admin notification <channel list --workspace SLUG | channel create --workspace SLUG --type webhook|matrix|smtp|gotify|ntfy --name N [--config-file PATH|- | --set key=value ...] [--secret NAME|ID | --secret-file PATH|- [--secret-type TYPE]] [--ca-file PATH] [--insecure-skip-verify] [--disabled] | channel update --workspace SLUG --channel NAME|ID [--name N] [--config-file PATH|-] [--set key=value ...] [--unset key ...] [--secret NAME|ID | --secret-file PATH|- [--secret-type TYPE] | --clear-secret] [--ca-file PATH] [--insecure-skip-verify=BOOL] [--enable | --disable] | channel delete --workspace SLUG --channel NAME|ID [--delete-secret] | channel test --workspace SLUG --channel NAME|ID | rule list --workspace SLUG | rule create --workspace SLUG --channel NAME|ID --event KIND ... [--template NAME|ID] [--disabled] | rule delete --workspace SLUG --rule ID | delivery list --workspace SLUG --channel NAME|ID [--status pending|sent|failed] [--limit N]>"

// notificationArgs are the validated flags of `acp admin notification`.
type notificationArgs struct {
	Group, Sub, Workspace     string
	Channel, Rule, Type, Name string
	ConfigFile                string
	Set                       []string
	Unset                     []string
	Secret, SecretFile        string
	SecretType                string
	ClearSecret               bool
	CAFile                    string
	InsecureSkipVerify        bool
	Disabled, Enable, Disable bool
	Events                    []string
	Template                  string
	Status                    string
	DeleteSecret              bool
	Limit                     int
	Given                     map[string]bool
}

// parseNotificationArgs parses and validates flags without touching the
// database. Secret values are accepted only via file or stdin.
func parseNotificationArgs(args []string, stderr io.Writer) (notificationArgs, error) {
	if len(args) < 2 {
		return notificationArgs{}, errors.New(notificationUsage)
	}
	a := notificationArgs{Group: args[0], Sub: args[1]}
	fs := flag.NewFlagSet("notification "+a.Group+" "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sets, unsets, events setFlags
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch a.Group + " " + a.Sub {
	case "channel list", "rule list":
	case "channel create", "channel update":
		fs.StringVar(&a.Name, "name", "", "channel name")
		fs.StringVar(&a.ConfigFile, "config-file", "", "JSON file with the channel config, or - for stdin")
		fs.Var(&sets, "set", "config option key=value, value parsed as JSON when valid (repeatable)")
		fs.StringVar(&a.Secret, "secret", "", "existing secret (name or id)")
		fs.StringVar(&a.SecretFile, "secret-file", "", "file with the secret value to store, or - for stdin")
		fs.StringVar(&a.SecretType, "secret-type", "", "type of a secret created by --secret-file (default: api_token)")
		fs.StringVar(&a.CAFile, "ca-file", "", "PEM file with additional CA certificates")
		fs.BoolVar(&a.InsecureSkipVerify, "insecure-skip-verify", false, "skip TLS verification (insecure)")
		if a.Sub == "create" {
			fs.StringVar(&a.Type, "type", "", "channel type")
			fs.BoolVar(&a.Disabled, "disabled", false, "create the channel disabled")
		} else {
			fs.StringVar(&a.Channel, "channel", "", "channel name or id")
			fs.Var(&unsets, "unset", "remove a config key (repeatable)")
			fs.BoolVar(&a.ClearSecret, "clear-secret", false, "remove the credential reference")
			fs.BoolVar(&a.Enable, "enable", false, "enable the channel")
			fs.BoolVar(&a.Disable, "disable", false, "disable the channel")
		}
	case "channel delete", "channel test":
		fs.StringVar(&a.Channel, "channel", "", "channel name or id")
		if a.Sub == "delete" {
			fs.BoolVar(&a.DeleteSecret, "delete-secret", false, "also delete the channel's secret if nothing else references it")
		}
	case "rule create":
		fs.StringVar(&a.Channel, "channel", "", "channel name or id")
		fs.Var(&events, "event", "event kind: run.failed, run.recovered, run.drift, run.finished (repeatable)")
		fs.StringVar(&a.Template, "template", "", "restrict to one template (name, slug or id)")
		fs.BoolVar(&a.Disabled, "disabled", false, "create the rule disabled")
	case "rule delete":
		fs.StringVar(&a.Rule, "rule", "", "rule id")
	case "delivery list":
		fs.StringVar(&a.Channel, "channel", "", "channel name or id")
		fs.StringVar(&a.Status, "status", "", "filter: pending, sent or failed")
		fs.IntVar(&a.Limit, "limit", 50, "maximum number of rows (1-200)")
	default:
		return a, errors.New(notificationUsage)
	}
	if err := fs.Parse(args[2:]); err != nil {
		return a, err
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	a.Set, a.Unset, a.Events = sets, unsets, events
	a.Given = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { a.Given[f.Name] = true })
	if a.Workspace == "" {
		return a, errors.New("--workspace is required")
	}
	need := func(v, flagName string) error {
		if v == "" {
			return fmt.Errorf("--%s is required", flagName)
		}
		return nil
	}
	switch a.Group + " " + a.Sub {
	case "channel create":
		for _, c := range []struct{ v, n string }{{a.Type, "type"}, {a.Name, "name"}} {
			if err := need(c.v, c.n); err != nil {
				return a, err
			}
		}
		if !notify.ValidType(a.Type) {
			return a, fmt.Errorf("unknown channel type %q", a.Type)
		}
	case "channel update", "channel delete", "channel test", "rule create", "delivery list":
		if err := need(a.Channel, "channel"); err != nil {
			return a, err
		}
	case "rule delete":
		if err := need(a.Rule, "rule"); err != nil {
			return a, err
		}
		if _, err := uuid.Parse(a.Rule); err != nil {
			return a, errors.New("--rule must be a rule id")
		}
	}
	if a.Group == "channel" && (a.Sub == "create" || a.Sub == "update") {
		if a.Secret != "" && a.SecretFile != "" || (a.ClearSecret && (a.Secret != "" || a.SecretFile != "")) {
			return a, errors.New("use only one of --secret, --secret-file and --clear-secret")
		}
		if a.Enable && a.Disable {
			return a, errors.New("use either --enable or --disable")
		}
		if a.ConfigFile == "-" && a.SecretFile == "-" {
			return a, errors.New("--config-file and --secret-file cannot both read stdin")
		}
		for _, kv := range a.Set {
			if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
				return a, fmt.Errorf("--set %q: expected key=value", kv)
			}
		}
	}
	if a.Group == "rule" && a.Sub == "create" {
		if len(a.Events) == 0 {
			return a, errors.New("at least one --event is required")
		}
		for _, e := range a.Events {
			if !notify.ValidKind(e) {
				return a, fmt.Errorf("unknown event %q", e)
			}
		}
	}
	if a.Group == "delivery" {
		switch a.Status {
		case "", "pending", "sent", "failed":
		default:
			return a, errors.New("--status must be pending, sent or failed")
		}
		if a.Limit < 1 || a.Limit > 200 {
			return a, errors.New("--limit must be between 1 and 200")
		}
	}
	return a, nil
}

// applyConfigFlags merges --config-file, --set and --unset into base.
func applyConfigFlags(a notificationArgs, base json.RawMessage, in io.Reader, readFile func(string) ([]byte, error)) (json.RawMessage, error) {
	cfg := map[string]any{}
	if a.ConfigFile != "" {
		var raw []byte
		var err error
		if a.ConfigFile == "-" {
			raw, err = io.ReadAll(io.LimitReader(in, 64<<10))
		} else {
			raw, err = readFile(a.ConfigFile)
		}
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("config file is not a JSON object: %w", err)
		}
	} else if len(base) > 0 {
		if err := json.Unmarshal(base, &cfg); err != nil {
			return nil, fmt.Errorf("stored config: %w", err)
		}
	}
	for _, kv := range a.Set {
		k, v, _ := strings.Cut(kv, "=")
		var parsed any
		if json.Unmarshal([]byte(v), &parsed) != nil {
			parsed = v
		}
		cfg[k] = parsed
	}
	for _, k := range a.Unset {
		delete(cfg, k)
	}
	return json.Marshal(cfg)
}

type notifyEnv struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
	svc  *notify.Service
	sec  *secrets.Service
	ws   uuid.UUID
}

func (e *notifyEnv) audit(ctx context.Context, action, targetType, id string, details map[string]any) {
	cliAuditTarget(ctx, e.q, e.ws, action, targetType, id, details)
}

func (e *notifyEnv) channel(ctx context.Context, ref string) (notify.ChannelView, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return e.svc.GetChannel(ctx, e.ws, id)
	}
	list, err := e.svc.ListChannels(ctx, e.ws)
	if err != nil {
		return notify.ChannelView{}, err
	}
	for _, c := range list {
		if c.Name == ref {
			return c, nil
		}
	}
	return notify.ChannelView{}, fmt.Errorf("channel %q not found", ref)
}

func (e *notifyEnv) template(ctx context.Context, ref string) (uuid.UUID, error) {
	list, err := e.q.ListTemplates(ctx, e.ws)
	if err != nil {
		return uuid.Nil, err
	}
	for _, t := range list {
		if t.ID.String() == ref || t.Slug == ref || t.Name == ref {
			return t.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("template %q not found", ref)
}

// resolveSecret returns the secret to reference after applying the secret
// flags to cur (the channel's current secret, if any). A --secret-file value
// replaces the current secret in place or creates "notification-<name>".
func (e *notifyEnv) resolveSecret(ctx context.Context, a notificationArgs, cur *uuid.UUID, name string, in io.Reader) (*uuid.UUID, error) {
	switch {
	case a.ClearSecret:
		return nil, nil
	case a.Secret != "":
		id, err := findSecret(ctx, e.sec, e.ws, a.Secret)
		return &id, err
	case a.SecretFile != "":
		val, err := readSecretValue(a.SecretFile, in)
		if err != nil {
			return nil, err
		}
		if cur != nil {
			meta, err := e.sec.Get(ctx, e.ws, *cur)
			if err != nil {
				return nil, fmt.Errorf("get current secret: %w", err)
			}
			if a.SecretType != "" && a.SecretType != meta.Type {
				return nil, fmt.Errorf("current secret has type %s; the type cannot be changed (create a new secret and use --secret)", meta.Type)
			}
			if _, err := e.sec.Update(ctx, e.ws, *cur, secrets.UpdateInput{Value: val}); err != nil {
				return nil, fmt.Errorf("replace secret: %w", err)
			}
			return cur, nil
		}
		typ := a.SecretType
		if typ == "" {
			typ = secrets.TypeAPIToken
		}
		m, err := e.sec.Create(ctx, e.ws, secrets.CreateInput{Name: "notification-" + name, Type: typ, Value: val})
		if err != nil {
			return nil, fmt.Errorf("store secret: %w", err)
		}
		return &m.ID, nil
	}
	return cur, nil
}

// runAdminNotification implements `acp admin notification`. Secret values are
// read from a file or stdin and never printed.
func runAdminNotification(ctx context.Context, args []string, cfg config.Config, in io.Reader, stdout, stderr io.Writer) error {
	a, err := parseNotificationArgs(args, stderr)
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
	sec := secrets.NewService(q, sp)
	ws, err := (&admin.InventoryOps{Q: q}).Workspace(ctx, a.Workspace)
	if err != nil {
		return err
	}
	e := &notifyEnv{pool: pool, q: q, sec: sec, ws: ws, svc: notify.NewService(notify.ServiceOptions{DB: pool, Secrets: sec, PublicURL: cfg.PublicURL})}
	switch a.Group + " " + a.Sub {
	case "channel list":
		list, err := e.svc.ListChannels(ctx, ws)
		if err != nil {
			return err
		}
		for _, c := range list {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\tenabled=%v\tsecret=%s\n", c.ID, c.Type, c.Name, c.Enabled, idOrDash(c.SecretID))
		}
	case "channel create":
		return e.channelCreate(ctx, a, in, stdout)
	case "channel update":
		return e.channelUpdate(ctx, a, in, stdout)
	case "channel delete":
		c, err := e.channel(ctx, a.Channel)
		if err != nil {
			return err
		}
		if err := e.svc.DeleteChannel(ctx, ws, c.ID); err != nil {
			return err
		}
		e.audit(ctx, "notification.channel.delete", "notification_channel", c.ID.String(), nil)
		_, _ = fmt.Fprintf(stdout, "deleted channel %s\n", c.Name)
		if a.DeleteSecret && c.SecretID != nil {
			m, err := e.sec.Get(ctx, ws, *c.SecretID)
			if err != nil {
				return fmt.Errorf("channel deleted, but secret lookup failed: %w", err)
			}
			if err := deleteUnreferencedSecret(ctx, e.pool, e.sec, ws, m); err != nil {
				return fmt.Errorf("channel deleted, secret kept: %w", err)
			}
			e.audit(ctx, "secret.delete", "secret", m.ID.String(), map[string]any{"name": m.Name, "type": m.Type})
			_, _ = fmt.Fprintf(stdout, "deleted secret %s\n", m.Name)
		}
	case "channel test":
		c, err := e.channel(ctx, a.Channel)
		if err != nil {
			return err
		}
		sendErr := e.svc.SendTest(ctx, ws, c.ID)
		e.audit(ctx, "notification.channel.test", "notification_channel", c.ID.String(), map[string]any{"ok": sendErr == nil})
		if sendErr != nil {
			return fmt.Errorf("test send failed: %w", sendErr)
		}
		_, _ = fmt.Fprintf(stdout, "test message sent via %s\n", c.Name)
	case "rule list":
		list, err := e.svc.ListRules(ctx, ws)
		if err != nil {
			return err
		}
		for _, r := range list {
			_, _ = fmt.Fprintf(stdout, "%s\tchannel=%s\tevents=%s\ttemplate=%s\tenabled=%v\n", r.ID, r.ChannelID, strings.Join(r.Events, ","), idOrDash(r.TemplateID), r.Enabled)
		}
	case "rule create":
		c, err := e.channel(ctx, a.Channel)
		if err != nil {
			return err
		}
		ri := notify.RuleInput{ChannelID: c.ID, Events: a.Events, Enabled: !a.Disabled}
		if a.Template != "" {
			tid, err := e.template(ctx, a.Template)
			if err != nil {
				return err
			}
			ri.TemplateID = &tid
		}
		r, err := e.svc.CreateRule(ctx, ws, ri, uuid.Nil)
		if err != nil {
			return err
		}
		e.audit(ctx, "notification.rule.create", "notification_rule", r.ID.String(), map[string]any{"channel_id": r.ChannelID, "events": r.Events})
		_, _ = fmt.Fprintf(stdout, "created rule %s\n", r.ID)
	case "rule delete":
		id, _ := uuid.Parse(a.Rule)
		if err := e.svc.DeleteRule(ctx, ws, id); err != nil {
			return err
		}
		e.audit(ctx, "notification.rule.delete", "notification_rule", id.String(), nil)
		_, _ = fmt.Fprintf(stdout, "deleted rule %s\n", id)
	case "delivery list":
		c, err := e.channel(ctx, a.Channel)
		if err != nil {
			return err
		}
		fetch := a.Limit
		if a.Status != "" {
			fetch = 200
		}
		list, err := e.svc.ListDeliveries(ctx, ws, c.ID, fetch)
		if err != nil {
			return err
		}
		n := 0
		for _, d := range list {
			if a.Status != "" && d.Status != a.Status {
				continue
			}
			if n++; n > a.Limit {
				break
			}
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\trun=%s\tattempts=%d\tcreated=%s\t%s\n", d.ID, d.Kind, d.Status, d.RunID, d.Attempts,
				d.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), orDash(d.LastError))
		}
	}
	return nil
}

func idOrDash(id *uuid.UUID) string {
	if id == nil {
		return "-"
	}
	return id.String()
}

func (e *notifyEnv) channelCreate(ctx context.Context, a notificationArgs, in io.Reader, stdout io.Writer) error {
	cfg, err := applyConfigFlags(a, nil, in, os.ReadFile)
	if err != nil {
		return err
	}
	ci := notify.ChannelInput{Name: a.Name, Type: a.Type, Config: cfg, TLSSkipVerify: a.InsecureSkipVerify, Enabled: !a.Disabled}
	if a.CAFile != "" {
		pem, err := os.ReadFile(a.CAFile) //nolint:gosec // operator supplied path
		if err != nil {
			return fmt.Errorf("read CA file: %w", err)
		}
		ci.TLSCAPEM = string(pem)
	}
	if ci.SecretID, err = e.resolveSecret(ctx, a, nil, a.Name, in); err != nil {
		return err
	}
	c, err := e.svc.CreateChannel(ctx, e.ws, ci, uuid.Nil)
	if err != nil {
		return err
	}
	e.audit(ctx, "notification.channel.create", "notification_channel", c.ID.String(), map[string]any{"name": c.Name, "type": c.Type})
	_, _ = fmt.Fprintf(stdout, "created channel %s (%s)\n", c.Name, c.ID)
	return nil
}

func (e *notifyEnv) channelUpdate(ctx context.Context, a notificationArgs, in io.Reader, stdout io.Writer) error {
	cur, err := e.channel(ctx, a.Channel)
	if err != nil {
		return err
	}
	ci := notify.ChannelInput{Name: cur.Name, Config: cur.Config, SecretID: cur.SecretID, TLSCAPEM: cur.TLSCAPEM,
		TLSSkipVerify: cur.TLSSkipVerify, Enabled: cur.Enabled}
	if a.Name != "" {
		ci.Name = a.Name
	}
	if ci.Config, err = applyConfigFlags(a, cur.Config, in, os.ReadFile); err != nil {
		return err
	}
	if a.Given["insecure-skip-verify"] {
		ci.TLSSkipVerify = a.InsecureSkipVerify
	}
	if a.CAFile != "" {
		pem, err := os.ReadFile(a.CAFile) //nolint:gosec // operator supplied path
		if err != nil {
			return fmt.Errorf("read CA file: %w", err)
		}
		ci.TLSCAPEM = string(pem)
	}
	if a.Enable {
		ci.Enabled = true
	}
	if a.Disable {
		ci.Enabled = false
	}
	if ci.SecretID, err = e.resolveSecret(ctx, a, cur.SecretID, ci.Name, in); err != nil {
		return err
	}
	c, err := e.svc.UpdateChannel(ctx, e.ws, cur.ID, ci, uuid.Nil)
	if err != nil {
		return err
	}
	e.audit(ctx, "notification.channel.update", "notification_channel", c.ID.String(), map[string]any{"name": c.Name, "enabled": c.Enabled})
	_, _ = fmt.Fprintf(stdout, "updated channel %s\n", c.Name)
	return nil
}
