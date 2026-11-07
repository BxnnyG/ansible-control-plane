package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// WorkerAPIHandler returns the handler of the internal worker listener: only
// /api/internal/worker/* is served (bearer-token protected, outside the user
// permission model), everything else answers 404.
func WorkerAPIHandler(api http.Handler, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/internal/worker/", http.StripPrefix("/api/internal/worker", api))
	mux.Handle("/metrics", promhttp.Handler()) // internal-only listener, never published
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
	return accessLog(log)(mux)
}

// StartWorkerAPI serves the worker API on addr over plain HTTP until ctx is
// cancelled (ADR-0016). The listener is bound before returning, so a port
// conflict surfaces as an error. Later serve errors are sent to errc.
func StartWorkerAPI(ctx context.Context, addr string, api http.Handler, grace time.Duration, log *slog.Logger, errc chan<- error) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("worker API listen: %w", err)
	}
	// No write timeout: the claim endpoint long-polls and bundles may be large.
	srv := &http.Server{Handler: WorkerAPIHandler(api, log), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("worker API: %w", err)
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("worker API listening (internal only)", "addr", addr)
	return nil
}
