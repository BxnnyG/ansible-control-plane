// Package httpx is the only HTTP client connectors may use. It is strictly
// read-only: every method except GET and HEAD is refused at the transport
// level (CON-8, ADR-0009), so a connector bug cannot modify a remote system.
package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Defaults for timeout and response size.
const (
	DefaultTimeout = 30 * time.Second
	DefaultMaxBody = 16 << 20
	mask           = "***"
)

// Errors returned by the client.
var (
	// ErrMethodNotAllowed is returned for every method other than GET and HEAD.
	ErrMethodNotAllowed = errors.New("httpx: only GET and HEAD are allowed")
	// ErrTooLarge is returned when a response exceeds the size limit.
	ErrTooLarge = errors.New("httpx: response too large")
)

// StatusError is returned for non-2xx responses. The body is never included.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("unexpected HTTP status %d", e.Code) }

// Auth selects how requests are authenticated. At most one mode is used:
// Bearer, then Basic, then Header.
type Auth struct {
	Bearer              string
	BasicUser, BasicPwd string
	HeaderName, HeaderV string // custom header, e.g. "Authorization" with a scheme prefix
}

func (a Auth) secrets() []string {
	var out []string
	for _, s := range []string{a.Bearer, a.BasicUser, a.BasicPwd, a.HeaderV} {
		if len(s) >= 4 {
			out = append(out, s)
		}
	}
	if a.BasicUser != "" || a.BasicPwd != "" {
		out = append(out, base64.StdEncoding.EncodeToString([]byte(a.BasicUser+":"+a.BasicPwd)))
	}
	return out
}

func (a Auth) apply(h http.Header) {
	switch {
	case a.Bearer != "":
		h.Set("Authorization", "Bearer "+a.Bearer)
	case a.BasicUser != "" || a.BasicPwd != "":
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.BasicUser+":"+a.BasicPwd)))
	case a.HeaderName != "":
		h.Set(a.HeaderName, a.HeaderV)
	}
}

// Options configure one client per connector instance.
type Options struct {
	BaseURL       string // http(s)://host[:port][/prefix]
	CAPEM         []byte // optional additional trust anchors
	SkipTLSVerify bool   // explicit opt-out; verification is on by default
	Timeout       time.Duration
	MaxBody       int64
	Auth          Auth
	// Transport replaces the underlying round tripper (tests); the read-only
	// guard still wraps it.
	Transport http.RoundTripper
}

// Client performs read-only requests against one base URL.
type Client struct {
	base    *url.URL
	hc      *http.Client
	auth    Auth
	maxBody int64
}

type readOnly struct{ next http.RoundTripper }

func (t readOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, ErrMethodNotAllowed
	}
	return t.next.RoundTrip(r)
}

// New validates the options and builds a client.
func New(o Options) (*Client, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("httpx: base URL must be http(s)://host[:port][/prefix] without credentials")
	}
	u.RawQuery, u.Fragment = "", ""
	rt := o.Transport
	if rt == nil {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.SkipTLSVerify} //nolint:gosec // explicit per-instance opt-out
		if len(o.CAPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(o.CAPEM) {
				return nil, errors.New("httpx: CA PEM contains no certificate")
			}
			cfg.RootCAs = pool
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = cfg
		tr.Proxy = nil
		rt = tr
	}
	timeout, maxBody := o.Timeout, o.MaxBody
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxBody <= 0 {
		maxBody = DefaultMaxBody
	}
	c := &Client{base: u, auth: o.Auth, maxBody: maxBody}
	c.hc = &http.Client{
		Timeout:   timeout,
		Transport: readOnly{next: rt},
		// Never follow redirects: they could leak credentials to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

// redact removes credential material from an error message.
func (c *Client) redact(err error) error {
	msg := err.Error()
	for _, s := range c.auth.secrets() {
		msg = strings.ReplaceAll(msg, s, mask)
	}
	// Drop the query string of url.Error: it may carry tokens.
	var ue *url.Error
	if errors.As(err, &ue) {
		if u, perr := url.Parse(ue.URL); perr == nil && u.RawQuery != "" {
			msg = strings.ReplaceAll(msg, u.RawQuery, mask)
		}
	}
	return &maskedError{msg: msg, err: err}
}

type maskedError struct {
	msg string
	err error
}

func (e *maskedError) Error() string { return e.msg }
func (e *maskedError) Unwrap() error { return e.err }

func (c *Client) resolve(p string, q url.Values) (string, error) {
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") {
		return "", errors.New("httpx: path must be relative to the base URL")
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/" + strings.TrimLeft(p, "/")
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func (c *Client) do(ctx context.Context, method, p string, q url.Values) (*http.Response, error) {
	target, err := c.resolve(p, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, c.redact(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "acp-connector")
	c.auth.apply(req.Header)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.redact(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		return nil, &StatusError{Code: resp.StatusCode}
	}
	return resp, nil
}

// Get performs a GET and returns the body (limited to MaxBody).
func (c *Client) Get(ctx context.Context, p string, q url.Values) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, p, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
	if err != nil {
		return nil, c.redact(err)
	}
	if int64(len(b)) > c.maxBody {
		return nil, ErrTooLarge
	}
	return b, nil
}

// GetJSON performs a GET and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, p string, q url.Values, out any) error {
	b, err := c.Get(ctx, p, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Head performs a HEAD request and returns the status code of a 2xx answer.
func (c *Client) Head(ctx context.Context, p string, q url.Values) (int, error) {
	resp, err := c.do(ctx, http.MethodHead, p, q)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
