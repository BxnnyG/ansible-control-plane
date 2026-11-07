package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/BxnnyG/ansible-control-plane/internal/admin"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//nolint:gosec // usage text, not a credential
const secretUsage = "usage: acp admin secret <list --workspace SLUG | delete --workspace SLUG --secret NAME|ID>"

// secretArgs are the validated flags of `acp admin secret`.
type secretArgs struct {
	Sub, Workspace, Secret string
}

func parseSecretArgs(args []string, stderr io.Writer) (secretArgs, error) {
	if len(args) < 1 {
		return secretArgs{}, errors.New(secretUsage)
	}
	a := secretArgs{Sub: args[0]}
	fs := flag.NewFlagSet("secret "+a.Sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.Workspace, "workspace", "", "workspace slug")
	switch a.Sub {
	case "list":
	case "delete":
		fs.StringVar(&a.Secret, "secret", "", "secret name or id")
	default:
		return a, errors.New(secretUsage)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return a, err
	}
	if fs.NArg() > 0 {
		return a, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if a.Workspace == "" {
		return a, errors.New("--workspace is required")
	}
	if a.Sub == "delete" && a.Secret == "" {
		return a, errors.New("--secret is required")
	}
	return a, nil
}

// secretReferences lists "table.column (count)" for every foreign key that
// points at the secret. It reads the catalog so new referencing tables are
// covered automatically.
func secretReferences(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT c.conrelid::regclass::text, a.attname
FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
WHERE c.contype = 'f' AND c.confrelid = 'secrets'::regclass AND array_length(c.conkey, 1) = 1
ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list secret references: %w", err)
	}
	type ref struct{ table, col string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.col); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range refs {
		var n int
		// regclass::text yields an already-quoted identifier; the column is quoted here.
		q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", r.table, pgx.Identifier{r.col}.Sanitize())
		if err := pool.QueryRow(ctx, q, id).Scan(&n); err != nil {
			return nil, fmt.Errorf("count references in %s: %w", r.table, err)
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%s.%s (%d)", r.table, r.col, n))
		}
	}
	return out, nil
}

// deleteUnreferencedSecret deletes the secret unless something still uses it.
func deleteUnreferencedSecret(ctx context.Context, pool *pgxpool.Pool, svc *secrets.Service, ws uuid.UUID, m secrets.Meta) error {
	refs, err := secretReferences(ctx, pool, m.ID)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("secret %q is still referenced by %s: %w", m.Name, strings.Join(refs, ", "), secrets.ErrConflict)
	}
	return svc.Delete(ctx, ws, m.ID)
}

func findSecretMeta(ctx context.Context, svc *secrets.Service, ws uuid.UUID, ref string) (secrets.Meta, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return svc.Get(ctx, ws, id)
	}
	list, err := svc.List(ctx, ws)
	if err != nil {
		return secrets.Meta{}, err
	}
	for _, m := range list {
		if m.Name == ref {
			return m, nil
		}
	}
	return secrets.Meta{}, fmt.Errorf("secret %q not found", ref)
}

// runAdminSecret implements `acp admin secret`. Values are never read or printed.
func runAdminSecret(ctx context.Context, args []string, cfg config.Config, stdout, stderr io.Writer) error {
	a, err := parseSecretArgs(args, stderr)
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
	svc := secrets.NewService(q, sp)
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
		for _, m := range list {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\tcreated=%s\n", m.ID, m.Name, m.Type, m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
	case "delete":
		m, err := findSecretMeta(ctx, svc, ws, a.Secret)
		if err != nil {
			return err
		}
		if err := deleteUnreferencedSecret(ctx, pool, svc, ws, m); err != nil {
			return err
		}
		cliAuditTarget(ctx, q, ws, "secret.delete", "secret", m.ID.String(), map[string]any{"name": m.Name, "type": m.Type})
		_, _ = fmt.Fprintf(stdout, "deleted secret %s\n", m.Name)
	}
	return nil
}
