package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

// inlineScriptRe matches inline <script> elements (no src attribute) of the
// built SPA entry page: the theme bootstrap and the SvelteKit start script.
var inlineScriptRe = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

// inlineScriptHashes returns CSP source expressions for the inline scripts of
// index.html. The SvelteKit bootstrap contains a per-build random identifier,
// so the hashes are computed from the embedded file at startup instead of
// being hardcoded.
func inlineScriptHashes(static fs.FS) []string {
	raw, err := fs.ReadFile(static, "index.html")
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range inlineScriptRe.FindAllSubmatch(raw, -1) {
		if strings.Contains(string(m[0][:strings.Index(string(m[0]), ">")+1]), "src=") {
			continue
		}
		sum := sha256.Sum256(m[1])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

// contentSecurityPolicy builds the CSP of the app. Scripts come from the same
// origin plus the hashed inline bootstrap scripts; styles allow inline style
// attributes (Svelte and the layout use them); nothing may frame the app.
func contentSecurityPolicy(static fs.FS) string {
	script := append([]string{"'self'"}, inlineScriptHashes(static)...)
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(script, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// securityHeaders sets the baseline browser protections on every response.
// HSTS is only sent on connections the app terminated TLS for.
func securityHeaders(static fs.FS) func(http.Handler) http.Handler {
	csp := contentSecurityPolicy(static)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
			if r.TLS != nil {
				h.Set("Strict-Transport-Security", "max-age=15552000")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerAuth protects a handler with a static bearer token (constant-time compare).
func bearerAuth(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
