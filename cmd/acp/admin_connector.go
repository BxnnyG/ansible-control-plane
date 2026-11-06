package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const connectorUsage = "usage: acp admin connector <list --workspace SLUG | types | create --workspace SLUG --type T --name N [--url URL] [--secret NAME|ID | --secret-file PATH|- --secret-type TYPE] [--interval 5m] [--ca-file PATH] [--insecure-skip-verify] [--timeout SECONDS] [--set key=value ...] [--disabled] | update --workspace SLUG --connector NAME|ID [--url URL] [--secret NAME|ID | --secret-file PATH|- [--secret-type TYPE]] [--interval 5m] [--ca-file PATH] [--insecure-skip-verify=BOOL] [--timeout SECONDS] [--set key=value ...] [--unset key ...] [--enable | --disable] | delete --workspace SLUG --connector NAME|ID [--keep-secret] | test|sync --workspace SLUG --connector NAME|ID>"

type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ",") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

// connectorArgs are the validated flags of `acp admin connector`.
type connectorArgs struct {
	Sub, Workspace, Connector string
	Type, Name, URL           string
	Secret, SecretFile        string
	SecretType                string
	Interval                  time.Duration
	CAFile                    string
	InsecureSkipVerify        bool
	TimeoutSeconds            int
	Set                       []string
	Unset                     []string
	Disabled                  bool
	Enable                    bool
	KeepSecret                bool
	// Given records which flags were set explicitly (used by update).
	Given map[string]bool
}

// parseConnectorArgs parses and validates flags without touching the database.
// Secret values are never accepted on the command line, only via file or stdin.
func parseConnectorArgs(args []string, stderr io.Writer) (connectorArgs, error) {
	if len(args) == 0 {
		return connectorArgs{}, errors.New(connectorUsage)
	}
	a := connectorArgs{Sub: args[0]}
	fs := flag.NewFlagSet("connector "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sets, unsets setFlags
	switch a.Sub {
	case "types":
	case "list":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	case "test", "sync", "delete":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
		fs.StringVar(&a.Connector, "connector", "", "connector name or id")
		if a.Sub == "delete" {
			fs.BoolVar(&a.KeepSecret, "keep-secret", false, "do not delete the connector's secret")
		}
	case "create":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
		fs.StringVar(&a.Type, "type", "", "connector type")
		fs.StringVar(&a.Name, "name", "", "connector name")
		fs.StringVar(&a.URL, "url", "", "base URL")
		fs.StringVar(&a.Secret, "secret", "", "existing secret (name or id)")
		fs.StringVar(&a.SecretFile, "secret-file", "", "file with the secret value to store, or - for stdin")
		fs.StringVar(&a.SecretType, "secret-type", "api_token", "type of the secret created from --secret-file")
		fs.DurationVar(&a.Interval, "interval", connectors.DefaultInterval, "sync interval")
		fs.StringVar(&a.CAFile, "ca-file", "", "PEM file with an additional CA certificate")
		fs.BoolVar(&a.InsecureSkipVerify, "insecure-skip-verify", false, "skip TLS verification (insecure)")
		fs.IntVar(&a.TimeoutSeconds, "timeout", 0, "request timeout in seconds")
		fs.Var(&sets, "set", "type specific option key=value (repeatable)")
		fs.BoolVar(&a.Disabled, "disabled", false, "create the connector disabled")
	case "update":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
		fs.StringVar(&a.Connector, "connector", "", "connector name or id")
		fs.StringVar(&a.URL, "url", "", "base URL")
		fs.StringVar(&a.Secret, "secret", "", "existing secret (name or id)")
		fs.StringVar(&a.SecretFile, "secret-file", "", "file with the new secret value, or - for stdin")
		fs.StringVar(&a.SecretType, "secret-type", "", "type of a secret created by --secret-file (default: api_token)")
		fs.DurationVar(&a.Interval, "interval", 0, "sync interval")
		fs.StringVar(&a.CAFile, "ca-file", "", "PEM file with additional CA certificates")
		fs.BoolVar(&a.InsecureSkipVerify, "insecure-skip-verify", false, "skip TLS verification (insecure)")
		fs.IntVar(&a.TimeoutSeconds, "timeout", 0, "request timeout in seconds")
		fs.Var(&sets, "set", "type specific option key=value (repeatable)")
		fs.Var(&unsets, "unset", "remove a config key (repeatable)")
		fs.BoolVar(&a.Enable, "enable", false, "enable the connector")
		fs.BoolVar(&a.Disabled, "disable", false, "disable the connector")
	default:
		return connectorArgs{}, fmt.Errorf("unknown connector command %q\n%s", a.Sub, connectorUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return connectorArgs{}, err
	}
	if fs.NArg() > 0 {
		return connectorArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	a.Set, a.Unset = sets, unsets
	a.Given = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { a.Given[f.Name] = true })
	if a.Sub == "types" {
		return a, nil
	}
	if a.Workspace == "" {
		return connectorArgs{}, errors.New("--workspace is required")
	}
	switch a.Sub {
	case "test", "sync", "delete":
		if a.Connector == "" {
			return connectorArgs{}, errors.New("--connector is required")
		}
	case "update":
		if a.Connector == "" {
			return connectorArgs{}, errors.New("--connector is required")
		}
		if a.Secret != "" && a.SecretFile != "" {
			return connectorArgs{}, errors.New("use either --secret or --secret-file")
		}
		if a.Given["secret-type"] && a.SecretFile == "" {
			return connectorArgs{}, errors.New("--secret-type needs --secret-file")
		}
		if a.Enable && a.Disabled {
			return connectorArgs{}, errors.New("use either --enable or --disable")
		}
		if a.Given["interval"] && a.Interval < connectors.MinInterval {
			return connectorArgs{}, fmt.Errorf("--interval must be at least %s", connectors.MinInterval)
		}
		if len(a.Given) <= 2 {
			return connectorArgs{}, errors.New("nothing to update")
		}
	case "create":
		if a.Type == "" || a.Name == "" {
			return connectorArgs{}, errors.New("--type and --name are required")
		}
		if a.Secret != "" && a.SecretFile != "" {
			return connectorArgs{}, errors.New("use either --secret or --secret-file")
		}
		if a.Interval < connectors.MinInterval {
			return connectorArgs{}, fmt.Errorf("--interval must be at least %s", connectors.MinInterval)
		}
	}
	return a, nil
}

// buildConfig turns the TLS flags and --set pairs into the typed config map,
// using the spec to coerce values.
func buildConfig(a connectorArgs, spec connectors.Spec, readFile func(string) ([]byte, error)) (map[string]any, error) {
	cfg := map[string]any{}
	kinds := map[string]string{connectors.CfgPriority: connectors.FieldInt}
	for _, f := range spec.Fields {
		kinds[f.Key] = f.Kind
	}
	if a.CAFile != "" {
		b, err := readFile(a.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		cfg[connectors.CfgCAPEM] = string(b)
	}
	if a.InsecureSkipVerify {
		cfg[connectors.CfgTLSSkipVerify] = true
	}
	if a.TimeoutSeconds > 0 {
		cfg[connectors.CfgTimeoutSecs] = a.TimeoutSeconds
	}
	for _, kv := range a.Set {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q: want key=value", kv)
		}
		switch kinds[k] {
		case connectors.FieldBool:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("--set %s: not a boolean", k)
			}
			cfg[k] = b
		case connectors.FieldInt:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("--set %s: not an integer", k)
			}
			cfg[k] = n
		default:
			cfg[k] = v
		}
	}
	return cfg, nil
}

func readSecretValue(path string, in io.Reader) ([]byte, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(io.LimitReader(in, secrets.MaxValueSize+1))
	} else {
		b, err = os.ReadFile(path) //nolint:gosec // operator supplied path
	}
	if err != nil {
		return nil, fmt.Errorf("read secret: %w", err)
	}
	b = []byte(strings.TrimRight(string(b), "\r\n"))
	if len(b) == 0 {
		return nil, errors.New("secret value is empty")
	}
	return b, nil
}

func findSecret(ctx context.Context, sec *secrets.Service, ws uuid.UUID, ref string) (uuid.UUID, error) {
	list, err := sec.List(ctx, ws)
	if err != nil {
		return uuid.Nil, err
	}
	for _, m := range list {
		if m.ID.String() == ref || m.Name == ref {
			return m.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("secret %q not found", ref)
}

func findConnector(ctx context.Context, svc *connectors.Service, ws uuid.UUID, ref string) (dbgen.Connector, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return svc.Get(ctx, ws, id)
	}
	return svc.GetByName(ctx, ws, ref)
}

// runAdminConnector implements `acp admin connector`. Secret values are read
// from a file or stdin and never printed.
func runAdminConnector(ctx context.Context, args []string, cfg config.Config, in io.Reader, stdout, stderr io.Writer) error {
	a, err := parseConnectorArgs(args, stderr)
	if err != nil {
		return err
	}
	reg := newConnectorRegistry()
	if a.Sub == "types" {
		for _, s := range reg.Specs() {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\tsecret=%v\n", s.Type, s.Label, s.SecretTypes)
		}
		return nil
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
	fleetSt := fleet.NewStore(pool, nil)
	svc := connectors.New(connectors.Options{DB: pool, Registry: reg, Secrets: sec, SecretWriter: sec, PostSync: fleetSt, Alerts: fleetSt})
	ws, err := (&admin.InventoryOps{Q: q}).Workspace(ctx, a.Workspace)
	if err != nil {
		return err
	}

	switch a.Sub {
	case "list":
		list, err := svc.List(ctx, ws)
		if err != nil {
			return err
		}
		for _, c := range list {
			last := "-"
			if c.LastSyncAt.Valid {
				last = c.LastSyncAt.Time.UTC().Format("2006-01-02T15:04:05Z")
			}
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\tenabled=%v\tinterval=%ds\tlast=%s\t%s\tfailures=%d\t%s\n",
				c.ID, c.Type, c.Name, c.Enabled, c.IntervalSeconds, last, orDash(c.LastStatus), c.BackoffCount, orDash(c.LastError))
		}
		return nil
	case "create":
		spec, ok := reg.Spec(a.Type)
		if !ok {
			return fmt.Errorf("%w: %q", connectors.ErrUnknownType, a.Type)
		}
		cfgMap, err := buildConfig(a, spec, os.ReadFile)
		if err != nil {
			return err
		}
		in2 := connectors.CreateInput{Type: a.Type, Name: a.Name, BaseURL: a.URL, Config: cfgMap,
			IntervalSeconds: int(a.Interval / time.Second), Enabled: !a.Disabled}
		switch {
		case a.Secret != "":
			id, err := findSecret(ctx, sec, ws, a.Secret)
			if err != nil {
				return err
			}
			in2.SecretID = &id
		case a.SecretFile != "":
			val, err := readSecretValue(a.SecretFile, in)
			if err != nil {
				return err
			}
			m, err := sec.Create(ctx, ws, secrets.CreateInput{Name: "connector-" + a.Name, Type: a.SecretType, Value: val})
			if err != nil {
				return fmt.Errorf("store secret: %w", err)
			}
			in2.SecretID = &m.ID
		}
		c, generated, err := svc.CreateGenerating(ctx, ws, in2)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "connector: %s\nid: %s\ntype: %s\n", c.Name, c.ID, c.Type)
		if generated != "" { // shown once, never stored in plaintext
			_, _ = fmt.Fprintf(stdout, "webhook path: /api/hooks/alerts/%s\nsecret (shown once): %s\n", c.ID, generated)
		}
		return nil
	}
	c, err := findConnector(ctx, svc, ws, a.Connector)
	if err != nil {
		return err
	}
	if a.Sub == "update" {
		spec, ok := reg.Spec(c.Type)
		if !ok {
			return fmt.Errorf("%w: %q", connectors.ErrUnknownType, c.Type)
		}
		up, err := buildUpdate(a, spec, c.Config, os.ReadFile)
		if err != nil {
			return err
		}
		if a.Secret != "" {
			id, err := findSecret(ctx, sec, ws, a.Secret)
			if err != nil {
				return err
			}
			up.SecretID = &id
		}
		secretNote := ""
		if a.SecretFile != "" {
			val, err := readSecretValue(a.SecretFile, in)
			if err != nil {
				return err
			}
			if secretNote, err = applySecretFile(ctx, sec, ws, c, a.SecretType, val, &up); err != nil {
				return err
			}
		}
		u, err := svc.Update(ctx, ws, c.ID, up)
		if err != nil {
			return err
		}
		if secretNote != "" {
			if c.SecretID.Valid && u.Enabled { // in-place replace: sync with the new credential
				if err := svc.RequestSync(ctx, ws, c.ID); err != nil {
					return err
				}
			}
			cliAudit(ctx, q, ws, "connector.secret_replace", c.ID.String(), map[string]any{"name": c.Name, "mode": secretNote})
			_, _ = fmt.Fprintf(stdout, "secret: %s\n", secretNote)
		}
		if len(a.Given) > 2+btoi(a.Given["secret-file"])+btoi(a.Given["secret-type"]) {
			cliAudit(ctx, q, ws, "connector.update", c.ID.String(), map[string]any{"name": c.Name, "type": c.Type})
		}
		_, _ = fmt.Fprintf(stdout, "connector: %s\nid: %s\nenabled: %v\n", u.Name, u.ID, u.Enabled)
		return nil
	}
	if a.Sub == "delete" {
		pol := connectors.DeleteSecretUnused
		if a.KeepSecret {
			pol = connectors.KeepSecret
		}
		gone, err := svc.DeleteConnector(ctx, ws, c.ID, pol)
		if err != nil {
			return err
		}
		cliAudit(ctx, q, ws, "connector.delete", c.ID.String(), map[string]any{"name": c.Name, "type": c.Type, "secret_deleted": gone})
		_, _ = fmt.Fprintf(stdout, "deleted: %s\nsecret deleted: %v\n", c.Name, gone)
		return nil
	}
	if a.Sub == "test" {
		if err := svc.Test(ctx, ws, c.ID); err != nil {
			return fmt.Errorf("test failed: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "connector: %s\nstatus: ok\n", c.Name)
		return nil
	}
	counts, err := svc.Run(ctx, c, connectors.TriggerManual)
	if err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "connector: %s\nstatus: ok\nnetworks: %d\nobservations: %d\nalerts: %d\n", c.Name, counts.Networks, counts.Observations, counts.Alerts)
	return nil
}

// buildUpdate turns explicitly given update flags into an UpdateInput. Config
// changes are merged into the stored config; --unset removes keys and
// --insecure-skip-verify=false clears the skip flag.
func buildUpdate(a connectorArgs, spec connectors.Spec, stored []byte, readFile func(string) ([]byte, error)) (connectors.UpdateInput, error) {
	var up connectors.UpdateInput
	if a.Given["url"] {
		up.BaseURL = &a.URL
	}
	if a.Given["interval"] {
		n := int(a.Interval / time.Second)
		up.IntervalSeconds = &n
	}
	switch {
	case a.Enable:
		t := true
		up.Enabled = &t
	case a.Disabled:
		f := false
		up.Enabled = &f
	}
	if !a.Given["ca-file"] && !a.Given["insecure-skip-verify"] && !a.Given["timeout"] && len(a.Set) == 0 && len(a.Unset) == 0 {
		return up, nil
	}
	cfg := map[string]any{}
	if len(stored) > 0 {
		if err := json.Unmarshal(stored, &cfg); err != nil {
			return up, fmt.Errorf("decode stored config: %w", err)
		}
	}
	for _, k := range a.Unset {
		delete(cfg, k)
	}
	if a.Given["insecure-skip-verify"] && !a.InsecureSkipVerify {
		delete(cfg, connectors.CfgTLSSkipVerify)
	}
	add, err := buildConfig(a, spec, readFile)
	if err != nil {
		return up, err
	}
	for k, v := range add {
		cfg[k] = v
	}
	up.Config = cfg
	return up, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cliAudit records a CLI action; failures are reported on stderr only.
func cliAudit(ctx context.Context, q *dbgen.Queries, ws uuid.UUID, action, id string, details map[string]any) {
	b, _ := json.Marshal(details)
	if _, err := q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: action, TargetType: "connector", TargetID: id,
		WorkspaceID: pgtype.UUID{Bytes: ws, Valid: true}, Details: b,
	}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: audit write failed: %v\n", err)
	}
}

// applySecretFile replaces the value of the connector's current secret in
// place, or creates "connector-<name>" and links it through up when the
// connector has none. It returns a short description of what happened.
func applySecretFile(ctx context.Context, sec *secrets.Service, ws uuid.UUID, c dbgen.Connector, typ string, val []byte, up *connectors.UpdateInput) (string, error) {
	if up.SecretID != nil {
		return "", errors.New("use either --secret or --secret-file")
	}
	if c.SecretID.Valid {
		cur, err := sec.Get(ctx, ws, c.SecretID.Bytes)
		if err != nil {
			return "", fmt.Errorf("get current secret: %w", err)
		}
		if typ != "" && typ != cur.Type {
			return "", fmt.Errorf("current secret has type %s; the type cannot be changed (create a new secret and use --secret)", cur.Type)
		}
		if _, err := sec.Update(ctx, ws, cur.ID, secrets.UpdateInput{Value: val}); err != nil {
			return "", fmt.Errorf("replace secret: %w", err)
		}
		return "replaced value of " + cur.Name, nil
	}
	if typ == "" {
		typ = secrets.TypeAPIToken
	}
	m, err := sec.Create(ctx, ws, secrets.CreateInput{Name: "connector-" + c.Name, Type: typ, Value: val})
	if err != nil {
		return "", fmt.Errorf("store secret: %w", err)
	}
	up.SecretID = &m.ID
	return "created and linked " + m.Name, nil
}
