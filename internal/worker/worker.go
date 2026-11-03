// Package worker implements the worker supervisor stub.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/config"
)

// Run starts the health listener and the (stub) job loop until ctx is done.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
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

	client := &http.Client{Timeout: 5 * time.Second}
	url := strings.TrimRight(cfg.AppURL, "/") + "/healthz"
	tick := time.NewTicker(cfg.WorkerPoll)
	defer tick.Stop()
	for {
		log.Info("waiting for jobs")
		pollApp(ctx, client, url, log)
		select {
		case err := <-errc:
			return err
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
			defer cancel()
			return srv.Shutdown(sctx)
		case <-tick.C:
		}
	}
}

func pollApp(ctx context.Context, c *http.Client, url string, log *slog.Logger) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Error("build request", "error", err)
		return
	}
	resp, err := c.Do(req)
	if err != nil {
		log.Warn("app unreachable", "error", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn("app unhealthy", "status", resp.StatusCode)
	}
}
