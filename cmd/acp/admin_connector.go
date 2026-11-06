package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const connectorUsage = "usage: acp admin connector <list --workspace SLUG | types | create --workspace SLUG --type T --name N [--url URL] [--secret NAME|ID | --secret-file PATH|- --secret-type TYPE] [--interval 5m] [--ca-file PATH] [--insecure-skip-verify] [--timeout SECONDS] [--set key=value ...] [--disabled] | test|sync --workspace SLUG --connector NAME|ID>"

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
	Disabled                  bool
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
	var sets setFlags
	switch a.Sub {
	case "types":
	case "list":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	case "test", "sync":
		fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
		fs.StringVar(&a.Connector, "connector", "", "connector name or id")
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
	default:
		return connectorArgs{}, fmt.Errorf("unknown connector command %q\n%s", a.Sub, connectorUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return connectorArgs{}, err
	}
	if fs.NArg() > 0 {
		return connectorArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	a.Set = sets
	if a.Sub == "types" {
		return a, nil
	}
	if a.Workspace == "" {
		return connectorArgs{}, errors.New("--workspace is required")
	}
	switch a.Sub {
	case "test", "sync":
		if a.Connector == "" {
			return connectorArgs{}, errors.New("--connector is required")
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
	svc := connectors.New(connectors.Options{DB: pool, Registry: reg, Secrets: sec})
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
		c, err := svc.Create(ctx, ws, in2)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "connector: %s\nid: %s\ntype: %s\n", c.Name, c.ID, c.Type)
		return nil
	}
	c, err := findConnector(ctx, svc, ws, a.Connector)
	if err != nil {
		return err
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
