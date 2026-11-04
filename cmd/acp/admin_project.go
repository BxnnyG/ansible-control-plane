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
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const projectUsage = "usage: acp admin project <list --workspace SLUG | sync --workspace SLUG --project SLUG|ID>"

// projectArgs are the validated flags of `acp admin project`.
type projectArgs struct {
	Sub       string
	Workspace string
	Project   string
}

// parseProjectArgs parses and validates the flags; it does not touch the
// database.
func parseProjectArgs(args []string, stderr io.Writer) (projectArgs, error) {
	if len(args) == 0 {
		return projectArgs{}, errors.New(projectUsage)
	}
	a := projectArgs{Sub: args[0]}
	fs := flag.NewFlagSet("project "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch a.Sub {
	case "list":
	case "sync":
		fs.StringVar(&a.Project, "project", "", "project slug or id")
	default:
		return projectArgs{}, fmt.Errorf("unknown project command %q\n%s", a.Sub, projectUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return projectArgs{}, err
	}
	if fs.NArg() > 0 {
		return projectArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return projectArgs{}, errors.New("--workspace is required")
	}
	if a.Sub == "sync" && a.Project == "" {
		return projectArgs{}, errors.New("--project is required")
	}
	return a, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runAdminProject implements `acp admin project`. Credentials are never
// printed; errors come from the projects service and are already redacted.
func runAdminProject(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseProjectArgs(args, stderr)
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
	ops := &admin.ProjectOps{Q: q, Projects: prj}

	if a.Sub == "list" {
		list, err := ops.ListProjects(ctx, a.Workspace)
		if err != nil {
			return err
		}
		for _, p := range list {
			at, interval := "-", "-"
			if p.LastSyncAt != nil {
				at = p.LastSyncAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			if p.SyncIntervalSeconds != nil {
				interval = fmt.Sprintf("%ds", *p.SyncIntervalSeconds)
			}
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Slug, p.URL, p.DefaultRef, orDash(p.LastSyncStatus), orDash(p.LastSyncSHA), at, interval)
		}
		return nil
	}
	res, err := ops.SyncProject(ctx, a.Workspace, a.Project)
	if err != nil {
		return err
	}
	s := res.Scan
	_, _ = fmt.Fprintf(stdout, "project: %s\nstatus: %s\nsha: %s\nrevision_id: %s\nplaybooks: %d\nroles: %d\ninventories: %d\nvars_files: %d\nvault_files: %d\nwarnings: %d\n",
		res.Project.Slug, res.Status, res.SHA, res.RevisionID, s.Playbooks, s.Roles, s.Inventories, s.VarsFiles, s.VaultFiles, s.Warnings)
	return nil
}
