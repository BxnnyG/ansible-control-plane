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

	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	sem "github.com/BxnnyG/ansible-control-plane/internal/migrate/semaphore"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/schedules"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const migrateUsage = "usage: acp migrate semaphore --backup-dir DIR [--source-dsn DSN | ACP_MIGRATE_SOURCE_DSN] [--workspace SLUG] [--inventory-map ID=SLUG]... [--dry-run | --apply | --compare [--check-runs]] [--json]"

// inventoryMapFlag collects repeated --inventory-map <semaphore-id>=<acp-slug>.
type inventoryMapFlag map[int64]string

func (f inventoryMapFlag) String() string { return "" }

func (f inventoryMapFlag) Set(v string) error {
	id, slug, ok := strings.Cut(v, "=")
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if !ok || err != nil || n <= 0 || strings.TrimSpace(slug) == "" {
		return errors.New("expected <semaphore-id>=<acp-inventory-slug>")
	}
	f[n] = strings.TrimSpace(slug)
	return nil
}

// migrateArgs are the validated flags of `acp migrate semaphore`.
type migrateArgs struct {
	BackupDir    string
	DSN          string // never printed
	Workspace    string
	InventoryMap map[int64]string
	DryRun       bool // default mode: report only
	Apply        bool
	Compare      bool
	CheckRuns    bool
	JSON         bool
}

func parseMigrateArgs(args []string, lookup func(string) (string, bool), stderr io.Writer) (migrateArgs, error) {
	if len(args) == 0 || args[0] != "semaphore" {
		return migrateArgs{}, errors.New(migrateUsage)
	}
	a := migrateArgs{InventoryMap: inventoryMapFlag{}}
	fs := flag.NewFlagSet("migrate semaphore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.BackupDir, "backup-dir", "", "verified Semaphore backup directory (required)")
	fs.StringVar(&a.DSN, "source-dsn", "", "MySQL DSN of the restored backup (or ACP_MIGRATE_SOURCE_DSN)")
	fs.StringVar(&a.Workspace, "workspace", "default", "target workspace slug")
	fs.Var(inventoryMapFlag(a.InventoryMap), "inventory-map", "<semaphore-id>=<acp-inventory-slug> (repeatable)")
	fs.BoolVar(&a.DryRun, "dry-run", true, "print the mapping report without writing (default)")
	fs.BoolVar(&a.Apply, "apply", false, "import the planned objects (idempotent)")
	fs.BoolVar(&a.Compare, "compare", false, "compare source and ACP object counts; exit code 1 on mismatch")
	fs.BoolVar(&a.CheckRuns, "check-runs", false, "with --compare: queue one check-mode run per migrated template")
	fs.BoolVar(&a.JSON, "json", false, "print the report as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return migrateArgs{}, err
	}
	if fs.NArg() > 0 {
		return migrateArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	dryExplicit := false
	fs.Visit(func(f *flag.Flag) { dryExplicit = dryExplicit || f.Name == "dry-run" })
	switch {
	case a.Apply && a.Compare:
		return migrateArgs{}, errors.New("--apply and --compare are mutually exclusive")
	case (a.Apply || a.Compare) && dryExplicit && a.DryRun:
		return migrateArgs{}, errors.New("--dry-run cannot be combined with --apply or --compare")
	case a.CheckRuns && !a.Compare:
		return migrateArgs{}, errors.New("--check-runs requires --compare")
	case !a.Apply && !a.Compare && !a.DryRun:
		return migrateArgs{}, errors.New("nothing to do: use --dry-run, --apply or --compare")
	}
	a.DryRun = !a.Apply && !a.Compare
	if a.BackupDir == "" {
		return migrateArgs{}, errors.New("--backup-dir is required")
	}
	if a.DSN == "" {
		a.DSN, _ = lookup("ACP_MIGRATE_SOURCE_DSN")
	}
	if strings.TrimSpace(a.DSN) == "" {
		return migrateArgs{}, errors.New("--source-dsn or ACP_MIGRATE_SOURCE_DSN is required")
	}
	return a, nil
}

// runMigrate implements `acp migrate`. The DSN and all key material stay out
// of every message; only names, types, logins and fingerprints are printed.
func runMigrate(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseMigrateArgs(args, os.LookupEnv, stderr)
	if err != nil {
		return err
	}
	backup, err := sem.OpenBackup(a.BackupDir)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "backup verified: %d file(s) in %s\n", backup.Files, backup.Dir)

	src, err := sem.OpenMySQL(a.DSN)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	snap, err := src.Load(ctx)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	tgt, err := loadMigrateTarget(ctx, dbgen.New(pool), a.Workspace)
	if err != nil {
		return err
	}
	keys := sem.DescribeKeys(snap.Keys, backup.EncryptionKey)
	rep := sem.Map(snap, keys, tgt, sem.Options{InventoryMap: a.InventoryMap})
	if a.DryRun {
		return printMigrate(stdout, a.JSON, rep, rep.WriteText)
	}

	q := dbgen.New(pool)
	ws, err := q.GetWorkspaceBySlug(ctx, a.Workspace)
	if err != nil {
		return fmt.Errorf("workspace %q not found: %w", a.Workspace, err)
	}
	sp, err := secrets.NewLocalProviderFromFile(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}
	inv := inventory.New(inventory.Options{DB: pool})
	rs := runs.New(runs.Options{DB: pool, Graphs: inv, Sealer: sp})
	ap := &sem.Applier{
		Q: q, Secrets: secrets.NewService(q, sp), Inventories: inv, Runs: rs,
		Schedules: schedules.New(schedules.Options{DB: pool, Runs: rs}),
		Workspace: ws.ID, Slug: ws.Slug, EncKey: backup.EncryptionKey, Mappings: tgt.Mappings,
	}
	if a.Compare {
		res, err := ap.Compare(ctx, snap, rep, a.CheckRuns)
		if err != nil {
			return err
		}
		if err := printMigrate(stdout, a.JSON, res, res.WriteText); err != nil {
			return err
		}
		if !res.Match {
			return errors.New("comparison found missing objects")
		}
		return nil
	}
	res, err := ap.Apply(ctx, snap, rep)
	if res != nil {
		if perr := printMigrate(stdout, a.JSON, res, res.WriteText); perr != nil && err == nil {
			err = perr
		}
	}
	if err != nil {
		return err
	}
	if res.Failed() {
		return fmt.Errorf("apply finished with %d conflict(s) and %d failure(s); see the report", res.Counts[sem.ResConflict], res.Counts[sem.ResFailed])
	}
	return nil
}

// printMigrate writes v as JSON or renders it as text.
func printMigrate(w io.Writer, asJSON bool, v any, text func(io.Writer)) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(w)
	return nil
}

// loadMigrateTarget reads the ACP side of the comparison.
func loadMigrateTarget(ctx context.Context, q *dbgen.Queries, workspace string) (sem.Target, error) {
	ws, err := q.GetWorkspaceBySlug(ctx, workspace)
	if err != nil {
		return sem.Target{}, fmt.Errorf("workspace %q not found: %w", workspace, err)
	}
	tgt := sem.Target{Workspace: ws.Slug, Mappings: map[string]string{}}
	projects, err := q.ListProjects(ctx, ws.ID)
	if err != nil {
		return tgt, fmt.Errorf("list projects: %w", err)
	}
	for _, p := range projects {
		tgt.Projects = append(tgt.Projects, sem.ACPProject{ID: p.ID.String(), Slug: p.Slug, URL: p.Url, DefaultRef: p.DefaultRef})
	}
	invs, err := q.ListInventories(ctx, ws.ID)
	if err != nil {
		return tgt, fmt.Errorf("list inventories: %w", err)
	}
	idx := map[string]int{}
	for _, i := range invs {
		idx[i.Slug] = len(tgt.Inventories)
		tgt.Inventories = append(tgt.Inventories, sem.ACPInventory{Slug: i.Slug, Mode: i.Mode})
	}
	hosts, err := q.ListWorkspaceInventoryHostIdentifiers(ctx, ws.ID)
	if err != nil {
		return tgt, fmt.Errorf("list inventory hosts: %w", err)
	}
	for _, h := range hosts {
		inv := &tgt.Inventories[idx[h.InventorySlug]]
		inv.Hosts = append(inv.Hosts, h.HostName)
		if h.AnsibleHost != "" {
			inv.Hosts = append(inv.Hosts, h.AnsibleHost)
		}
	}
	maps, err := q.ListMigrationMappings(ctx, dbgen.ListMigrationMappingsParams{Source: "semaphore", WorkspaceID: ws.ID})
	if err != nil {
		return tgt, fmt.Errorf("list migration mappings: %w", err)
	}
	for _, m := range maps {
		tgt.Mappings[m.Kind+":"+m.SourceID] = m.AcpID.String()
	}
	return tgt, nil
}
