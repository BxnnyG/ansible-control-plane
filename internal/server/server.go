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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/config"
	"github.com/BxnnyG/ansible-control-plane/internal/policy"
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

// Option customizes the router.
type Option func(*routerOpts)

type routerOpts struct {
	auth        *auth.Service
	az          *policy.Authorizer
	store       APIStore
	secrets     SecretsAPI
	projects    ProjectsAPI
	inventories InventoriesAPI
	vaultIDs    VaultIdentitiesAPI
	vaultOps    VaultOpsAPI
	runs        RunsAPI
	publicURL   string
}

// WithAuth mounts the OIDC/session endpoints (/api/auth/*, /api/v1/me).
func WithAuth(a *auth.Service) Option { return func(o *routerOpts) { o.auth = a } }

// Router builds the HTTP handler.
func Router(log *slog.Logger, db Pinger, static fs.FS, opts ...Option) http.Handler {
	var ro routerOpts
	for _, o := range opts {
		o(&ro)
	}
	r := chi.NewRouter()
	// Note: no RealIP middleware; trusted proxy handling is added explicitly later.
	r.Use(middleware.Recoverer, accessLog(log))
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
	if ro.auth != nil {
		ro.auth.Routes(r)
	}
	if ro.az != nil {
		var info AuthInfo
		if ro.auth != nil {
			info = ro.auth
		}
		apiRoutes(r, ro.az, ro.store, info, log)
		if ro.secrets != nil {
			secretRoutes(r, ro.az, ro.store, ro.secrets, log)
		}
		if ro.projects != nil {
			projectRoutes(r, ro.az, ro.store, ro.projects, log)
			webhookRoutes(r, ro.az, ro.store, ro.projects, ro.publicURL, log)
		}
		if ro.runs != nil {
			runRoutes(r, ro.az, ro.store, ro.runs, log)
		}
		if ro.inventories != nil {
			inventoryRoutes(r, ro.az, ro.store, ro.inventories, log)
		}
		if ro.vaultIDs != nil && ro.vaultOps != nil {
			vaultRoutes(r, ro.az, ro.store, ro.vaultIDs, ro.vaultOps, log)
		}
	}
	// Unknown API paths must not fall through to the SPA.
	r.HandleFunc("/api/*", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
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
func Run(ctx context.Context, cfg config.Config, log *slog.Logger, db Pinger, opts ...Option) error {
	handler := Router(log, db, DefaultStatic(), opts...)
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
		if strings.HasPrefix(cfg.PublicURL, "https://") {
			httpHandler = redirectExceptOps(handler, cfg.PublicURL)
		}
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

// redirectExceptOps keeps /healthz, /readyz, /metrics on plain HTTP and
// redirects everything else to the configured public HTTPS URL. The target
// host never comes from the request, which prevents open redirects.
func redirectExceptOps(next http.Handler, publicURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}
		target := publicURL + "/" + strings.TrimLeft(r.URL.Path, "/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // host is fixed by configuration
	})
}
