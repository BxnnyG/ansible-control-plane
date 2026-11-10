package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/fleet"
)

const fleetUsage = "usage: acp admin fleet <merge | hosts | unmanaged [--include-untracked] | conflicts [--state open|resolved|all] | network <list | track | untrack> [--network NAME|ID] | add --host NAME|ID --inventory SLUG --group GROUP [--name NAME --author-name NAME --author-email EMAIL]> --workspace SLUG"

type fleetArgs struct {
	Sub, Workspace, State        string
	Host, Inventory, Group, Name string
	AuthorName, AuthorEmail      string
	Action, Network              string // network <list|track|untrack> --network NAME|ID
	IncludeUntracked             bool
}

func parseFleetArgs(args []string, stderr io.Writer) (fleetArgs, error) {
	if len(args) == 0 {
		return fleetArgs{}, errors.New(fleetUsage)
	}
	a := fleetArgs{Sub: args[0]}
	if a.Sub == "network" {
		if len(args) < 2 || (args[1] != "list" && args[1] != "track" && args[1] != "untrack") {
			return fleetArgs{}, errors.New(fleetUsage)
		}
		a.Action, args = args[1], args[1:]
	}
	fs := flag.NewFlagSet("fleet "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch a.Sub {
	case "merge", "hosts":
	case "unmanaged":
		fs.BoolVar(&a.IncludeUntracked, "include-untracked", false, "also list hosts whose only networks are untracked")
	case "network":
		fs.StringVar(&a.Network, "network", "", "network name (external key, description or CIDR) or id")
	case "add":
		fs.StringVar(&a.Host, "host", "", "fleet host name or id")
		fs.StringVar(&a.Inventory, "inventory", "", "target inventory slug")
		fs.StringVar(&a.Group, "group", "", "target group")
		fs.StringVar(&a.Name, "name", "", "inventory host name (default: fleet host name)")
		fs.StringVar(&a.AuthorName, "author-name", "ACP admin", "Git author name (git inventories)")
		fs.StringVar(&a.AuthorEmail, "author-email", "acp-admin@localhost", "Git author email (git inventories)")
	case "conflicts":
		fs.StringVar(&a.State, "state", "open", "open, resolved or all")
	default:
		return fleetArgs{}, fmt.Errorf("unknown fleet command %q\n%s", a.Sub, fleetUsage)
	}
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	if err := fs.Parse(args[1:]); err != nil {
		return fleetArgs{}, err
	}
	if fs.NArg() > 0 {
		return fleetArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return fleetArgs{}, errors.New("--workspace is required")
	}
	if a.Sub == "network" && a.Action != "list" && a.Network == "" {
		return fleetArgs{}, errors.New("--network is required")
	}
	if a.Sub == "network" && a.Action == "list" && a.Network != "" {
		return fleetArgs{}, errors.New("--network is not used by list")
	}
	if a.Sub == "add" && (a.Host == "" || a.Inventory == "" || a.Group == "") {
		return fleetArgs{}, errors.New("--host, --inventory and --group are required")
	}
	if a.Sub == "conflicts" && a.State != "open" && a.State != "resolved" && a.State != "all" {
		return fleetArgs{}, errors.New("--state must be open, resolved or all")
	}
	return a, nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatHost renders one host line (read-only, no secrets).
func formatHost(h dbgen.Host, keys []dbgen.HostKey) string {
	var f fleet.Fields
	_ = json.Unmarshal(h.Fields, &f)
	var macs []string
	for _, k := range keys {
		if k.Kind == fleet.KeyMAC {
			macs = append(macs, k.Value)
		}
	}
	hostSources := map[string]bool{}
	for _, ss := range f.Sources {
		for _, s := range ss {
			hostSources[s.ConnectorName] = true
		}
	}
	var src []string
	for s := range hostSources {
		src = append(src, s)
	}
	sort.Strings(src)
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s\tlast=%s\tsources=%s", h.ID, dash(h.Name), dash(strings.Join(macs, ",")),
		dash(strings.Join(f.IPs, ",")), dash(f.Kind), h.LastSeen.Time.UTC().Format("2006-01-02T15:04:05Z"), dash(strings.Join(src, ",")))
}

// formatUnmanaged renders one unmanaged host line.
func formatUnmanaged(h fleet.HostView) string {
	return fmt.Sprintf("%s\t%s\t%s\t%s\tonline=%s\talerts=%d", h.ID, dash(h.Name), dash(strings.Join(h.IPs, ",")), dash(h.Kind), h.Online, h.OpenAlerts)
}

func formatConflict(c dbgen.HostConflict) string {
	ack := "-"
	if c.AcknowledgedAt.Valid {
		ack = c.AcknowledgedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	ids := make([]string, len(c.HostIds))
	for i, id := range c.HostIds {
		ids[i] = id.String()
	}
	return fmt.Sprintf("%s\t%s\t%s\tack=%s\t%s\thosts=%s", c.ID, c.Type, c.State, ack, c.Fingerprint, strings.Join(ids, ","))
}

// runAdminFleet implements `acp admin fleet`. Output is read-only and
// contains no secrets; merge rebuilds hosts and conflicts of a workspace.
func runAdminFleet(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseFleetArgs(args, stderr)
	if err != nil {
		return err
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
	switch a.Sub {
	case "unmanaged":
		no := false
		list, err := fleet.NewStore(pool, nil).Hosts(ctx, ws, fleet.HostFilter{Managed: &no, ExcludeUntracked: !a.IncludeUntracked}, nil)
		if err != nil {
			return err
		}
		for _, h := range list {
			_, _ = fmt.Fprintln(stdout, formatUnmanaged(h))
		}
	case "network":
		return runAdminFleetNetwork(ctx, a, ws, pool, stdout)
	case "add":
		return runAdminFleetAdd(ctx, a, ws, pool, cfg, stdout)
	case "merge":
		s, err := fleet.NewStore(pool, nil).Merge(ctx, ws)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "hosts: %d\ncreated: %d\ndeleted: %d\nconflicts_open: %d\nconflicts_opened: %d\nconflicts_resolved: %d\n",
			s.Hosts, s.Created, s.Deleted, s.ConflictsOpen, s.ConflictsOpened, s.ConflictsResolved)
	case "hosts":
		hosts, err := q.ListHosts(ctx, ws)
		if err != nil {
			return err
		}
		keyRows, err := q.ListHostKeys(ctx, ws)
		if err != nil {
			return err
		}
		keys := map[uuid.UUID][]dbgen.HostKey{}
		for _, k := range keyRows {
			keys[k.HostID] = append(keys[k.HostID], k)
		}
		for _, h := range hosts {
			_, _ = fmt.Fprintln(stdout, formatHost(h, keys[h.ID]))
		}
	case "conflicts":
		list, err := q.ListHostConflicts(ctx, ws)
		if err != nil {
			return err
		}
		for _, c := range list {
			if a.State == "all" || c.State == a.State {
				_, _ = fmt.Fprintln(stdout, formatConflict(c))
			}
		}
	}
	return nil
}
