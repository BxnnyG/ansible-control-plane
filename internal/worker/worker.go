// Package worker implements the worker supervisor (ADR-0004, ADR-0016): it
// claims runs from the app's internal API, executes ansible-playbook
// unprivileged and streams logs and events back. It has no database access.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/config"
)

// Run starts the health listener and ACP_WORKER_CONCURRENCY claim loops until
// ctx is done. Running playbooks are cancelled on shutdown and reported.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if cfg.WorkerTokenFile == "" {
		return errors.New("ACP_WORKER_TOKEN_FILE is required")
	}
	raw, err := os.ReadFile(cfg.WorkerTokenFile)
	if err != nil {
		return fmt.Errorf("read worker token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 16 {
		return errors.New("worker token is shorter than 16 characters")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	srv := &http.Server{Addr: cfg.WorkerAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	log.Info("worker health listening", "addr", cfg.WorkerAddr)

	client := &Client{Base: cfg.AppURL, Token: token, HTTP: &http.Client{}}
	host, _ := os.Hostname()
	var id [3]byte
	_, _ = rand.Read(id[:])
	workerID := host + "-" + hex.EncodeToString(id[:])
	runner := &Runner{API: client, Log: log, CacheDir: cfg.WorkerCacheDir}

	var wg sync.WaitGroup
	for i := 0; i < cfg.WorkerConcurrency; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			claimLoop(ctx, client, runner, fmt.Sprintf("%s/%d", workerID, n), log)
		}(i)
	}
	log.Info("worker started", "id", workerID, "concurrency", cfg.WorkerConcurrency)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	wg.Wait()
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	return srv.Shutdown(sctx)
}

// claimLoop claims and executes runs one at a time until ctx is done.
func claimLoop(ctx context.Context, api API, r *Runner, id string, log *slog.Logger) {
	backoff := time.Second
	for ctx.Err() == nil {
		claim, err := api.Claim(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("claim failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if claim == nil {
			continue
		}
		log.Info("run claimed", "run", claim.RunID)
		r.Execute(ctx, *claim)
	}
}
