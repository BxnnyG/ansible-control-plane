package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// runAdminFleetAdd implements `acp admin fleet add`: it puts one unmanaged
// fleet host into an inventory group (Git inventories get a write-back
// commit).
func runAdminFleetAdd(ctx context.Context, a fleetArgs, ws uuid.UUID, pool *pgxpool.Pool, cfg config.Config, stdout io.Writer) error {
	q := dbgen.New(pool)
	sp, err := secrets.NewLocalProviderFromFile(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("load master key: %w", err)
	}
	prj := projects.New(projects.Options{Store: q, Secrets: secrets.NewService(q, sp), Git: &gitops.Client{}, Scanner: scan.JSONScanner{}, DataDir: cfg.DataDir})
	inv := inventory.New(inventory.Options{DB: pool, Checkouts: prj, Writer: prj})
	store := fleet.NewStore(pool, nil)

	invs, err := inv.List(ctx, ws)
	if err != nil {
		return err
	}
	var target *inventory.Inventory
	for i := range invs {
		if invs[i].Slug == a.Inventory {
			target = &invs[i]
		}
	}
	if target == nil {
		return fmt.Errorf("inventory %q not found", a.Inventory)
	}
	no := false
	list, err := store.Hosts(ctx, ws, fleet.HostFilter{Managed: &no}, nil)
	if err != nil {
		return err
	}
	var host *fleet.HostView
	for i := range list {
		if list[i].ID.String() == a.Host || strings.EqualFold(list[i].Name, a.Host) {
			if host != nil {
				return fmt.Errorf("host %q is ambiguous, use the id", a.Host)
			}
			host = &list[i]
		}
	}
	if host == nil {
		return fmt.Errorf("no unmanaged fleet host %q", a.Host)
	}
	res, err := fleet.Adder{Fleet: store, Inventories: inv}.AddToInventory(ctx, ws, host.ID, fleet.AddInput{
		InventoryID: target.ID, Group: a.Group, Name: a.Name, Author: projects.Author{Name: a.AuthorName, Email: a.AuthorEmail},
	}, nil)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "host: %s\nansible_host: %s\ngroup_created: %t\nversion: %d\n", res.Host, dash(res.AnsibleHost), res.Result.GroupCreated, res.Result.Version)
	if c := res.Result.Commit; c != nil && !c.NoOp {
		_, _ = fmt.Fprintf(stdout, "commit: %s\nbranch: %s\n", c.SHA, c.Branch)
	}
	return nil
}
