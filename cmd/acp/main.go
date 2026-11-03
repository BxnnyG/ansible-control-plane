// Command acp is the single binary of the Ansible Control Plane.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/db"
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
	case "migrate", "admin", "db":
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

	if args[0] == "worker" {
		return worker.Run(ctx, cfg, log)
	}

	log.Info("starting", "version", version.Version, "commit", version.Commit)
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	return server.Run(ctx, cfg, log, pool)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
