// Package server implements the HTTP(S) server of the app.
package server

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/version"
)

// Static holds the embedded web build. The placeholder keeps the embed valid
// when web/build has not been built; see static/ for details.
//
//go:embed all:static
var staticFS embed.FS

// Pinger checks backend readiness.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Router builds the HTTP handler.
func Router(log *slog.Logger, db Pinger, static fs.FS) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.Recoverer, accessLog(log))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			log.Warn("readiness check failed", "error", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Handle("/metrics", promhttp.Handler())
	r.Handle("/*", spa(static))
	return r
}

// DefaultStatic returns the embedded static files rooted at static/.
func DefaultStatic() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return staticFS
	}
	return sub
}

func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != "/" {
			if f, err := static.Open(p[1:]); err == nil {
				_ = f.Close()
				files.ServeHTTP(w, r)
				return
			}
		}
		if f, err := static.Open("index.html"); err == nil {
			_ = f.Close()
			r.URL.Path = "/"
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>ACP</title><h1>Ansible Control Plane</h1><p>Web UI not built.</p><small>" +
			version.Attribution + "</small>"))
	})
}

func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			if r.URL.Path == "/healthz" {
				return
			}
			log.Info("request", "method", r.Method, "path", r.URL.Path,
				"status", ww.Status(), "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// Run serves HTTP (and HTTPS when configured) until ctx is cancelled.
// With TLS enabled, the plain HTTP port keeps serving /healthz, /readyz and
// /metrics and redirects everything else to HTTPS.
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, db Pinger) error {
	handler := Router(log, db, DefaultStatic())
	errc := make(chan error, 2)
	var servers []*http.Server

	start := func(s *http.Server, useTLS bool) {
		servers = append(servers, s)
		go func() {
			var err error
			if useTLS {
				err = s.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
			} else {
				err = s.ListenAndServe()
			}
			if !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	httpHandler := handler
	if cfg.TLSEnabled() {
		httpHandler = redirectExceptOps(handler, cfg.HTTPSAddr)
		start(&http.Server{Addr: cfg.HTTPSAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, true)
		log.Info("https listening", "addr", cfg.HTTPSAddr)
	}
	start(&http.Server{Addr: cfg.HTTPAddr, Handler: httpHandler, ReadHeaderTimeout: 10 * time.Second}, false)
	log.Info("http listening", "addr", cfg.HTTPAddr, "tls", cfg.TLSEnabled())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return nil
}

func redirectExceptOps(next http.Handler, httpsAddr string) http.Handler {
	port := httpsAddr
	for i := len(httpsAddr) - 1; i >= 0; i-- {
		if httpsAddr[i] == ':' {
			port = httpsAddr[i+1:]
			break
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		for i := len(host) - 1; i >= 0; i-- {
			if host[i] == ':' {
				host = host[:i]
				break
			}
		}
		http.Redirect(w, r, "https://"+host+":"+port+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}
