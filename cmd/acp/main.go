// Command acp is the single binary of the Ansible Control Plane.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
	"github.com/BxnnyG/ansible-control-plane/internal/server"
	"github.com/BxnnyG/ansible-control-plane/internal/version"
	"github.com/BxnnyG/ansible-control-plane/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: acp <serve|worker|version|migrate|admin|db>")
	}
	switch args[0] {
	case "version":
		fmt.Printf("acp %s (%s)\n%s\n", version.Version, version.Commit, version.Attribution)
		return nil
	case "serve", "worker":
	case "admin":
	case "migrate", "db":
		fmt.Println("not implemented")
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if args[0] == "admin" {
		return runAdmin(ctx, args[1:], cfg, os.Stdin, os.Stdout, os.Stderr)
	}
	if args[0] == "worker" {
		return worker.Run(ctx, cfg, log)
	}

	log.Info("starting", "version", version.Version, "commit", version.Commit)
	// Fail fast: the master key is required even before login is configured.
	master, err := secrets.LoadMasterKey(cfg.MasterKeyFile, "ACP_MASTER_KEY")
	if err != nil {
		return fmt.Errorf("master key (ACP_MASTER_KEY_FILE or ACP_MASTER_KEY): %w", err)
	}
	defer clear(master)
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	var opts []server.Option
	switch {
	case cfg.AuthEnabled() && cfg.PublicURL != "":
		svc, err := newAuth(cfg, log, pool, master)
		if err != nil {
			return err
		}
		q := dbgen.New(pool)
		opts = append(opts, server.WithAuth(svc), server.WithPolicy(policy.New(q, nil, svc.Require, log), q))
		go auth.RunSessionCleanup(ctx, q, auth.DefaultCleanupInterval, log)
		log.Info("login enabled", "oidc", cfg.OIDCEnabled(), "breakglass", cfg.BreakGlassEnabled)
	case cfg.AuthEnabled():
		log.Warn("login disabled: ACP_PUBLIC_URL is not set")
	default:
		log.Warn("login disabled: OIDC not configured and ACP_BREAKGLASS_ENABLED=false")
	}
	return server.Run(ctx, cfg, log, pool, opts...)
}

func newAuth(cfg config.Config, log *slog.Logger, pool *pgxpool.Pool, master []byte) (*auth.Service, error) {
	sp, err := secrets.NewLocalProvider(master)
	if err != nil {
		return nil, fmt.Errorf("secrets provider: %w", err)
	}
	// Derive a purpose-specific key; the master key itself is never used directly.
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("acp-oidc-state-key"))
	return auth.New(auth.Options{
		Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, Scopes: cfg.OIDCScopes,
		RolesClaim: cfg.OIDCRolesClaim, PublicURL: cfg.PublicURL,
		IdleTimeout: cfg.SessionIdle, AbsoluteTimeout: cfg.SessionAbsolute,
		StateKey: mac.Sum(nil), Store: dbgen.New(pool), Log: log,
		BreakGlass: cfg.BreakGlassEnabled, Secrets: sp,
		RoleSync: auth.NewIdPRoleSyncer(dbgen.New(pool), log),
	})
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
