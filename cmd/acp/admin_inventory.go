package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const inventoryUsage = "usage: acp admin inventory <import --workspace SLUG --project SLUG|ID --path FILE --name NAME [--mode git|db] [--write-branch BRANCH] | list --workspace SLUG>"

// inventoryArgs are the validated flags of `acp admin inventory`.
type inventoryArgs struct {
	Sub string
	admin.InventoryImportInput
}

// parseInventoryArgs parses and validates the flags; it does not touch the
// database.
func parseInventoryArgs(args []string, stderr io.Writer) (inventoryArgs, error) {
	if len(args) == 0 {
		return inventoryArgs{}, errors.New(inventoryUsage)
	}
	a := inventoryArgs{Sub: args[0]}
	fs := flag.NewFlagSet("inventory "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch a.Sub {
	case "list":
	case "import":
		fs.StringVar(&a.Project, "project", "", "project slug or id")
		fs.StringVar(&a.Path, "path", "", "inventory file path inside the repository")
		fs.StringVar(&a.Name, "name", "", "inventory name")
		fs.StringVar(&a.Mode, "mode", "git", "inventory mode: git or db")
		fs.StringVar(&a.WriteBranch, "write-branch", "", "write-back branch (git mode, default acp/<slug>)")
	default:
		return inventoryArgs{}, fmt.Errorf("unknown inventory command %q\n%s", a.Sub, inventoryUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return inventoryArgs{}, err
	}
	if fs.NArg() > 0 {
		return inventoryArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return inventoryArgs{}, errors.New("--workspace is required")
	}
	if a.Sub == "import" {
		for _, r := range []struct{ v, n string }{{a.Project, "project"}, {a.Path, "path"}, {a.Name, "name"}} {
			if r.v == "" {
				return inventoryArgs{}, fmt.Errorf("--%s is required", r.n)
			}
		}
		if a.Mode != inventory.ModeGit && a.Mode != inventory.ModeDB {
			return inventoryArgs{}, errors.New("--mode must be git or db")
		}
		if a.Mode == inventory.ModeDB && a.WriteBranch != "" {
			return inventoryArgs{}, errors.New("--write-branch is only valid in git mode")
		}
	}
	return a, nil
}

// runAdminInventory implements `acp admin inventory`. Variable values are
// never printed, only identifiers and counts.
func runAdminInventory(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseInventoryArgs(args, stderr)
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
	ops := &admin.InventoryOps{Q: q, Inv: inventory.New(inventory.Options{DB: pool, Checkouts: prj, Writer: prj}), Projects: prj}

	if a.Sub == "list" {
		list, err := ops.ListInventories(ctx, a.Workspace)
		if err != nil {
			return err
		}
		for _, i := range list {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\thosts=%d\tgroups=%d\n", i.ID, i.Slug, i.Name, i.Mode, i.Path, i.Hosts, i.Groups)
		}
		return nil
	}
	res, err := ops.ImportInventory(ctx, a.InventoryImportInput)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "inventory_id: %s\nslug: %s\ncreated: %t\ngroups: %d\nhosts: %d\nsha: %s\n", res.Inventory.ID, res.Inventory.Slug, res.Created, res.Groups, res.Hosts, res.SHA)
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	return nil
}
