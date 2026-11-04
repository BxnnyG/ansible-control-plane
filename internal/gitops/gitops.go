// Package gitops wraps the git CLI for project sync and write-back.
// It performs no DB or HTTP access; credentials are materialized only for the
// duration of a single git invocation (ADR-0012).
package gitops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNonFastForward is returned when the remote rejects a push because it is not a fast-forward.
	ErrNonFastForward = errors.New("gitops: push rejected: non-fast-forward")
	// ErrConflict is returned when the remote rejects a push for another ref-level reason.
	ErrConflict = errors.New("gitops: push rejected: conflict")
	// ErrNothingToCommit is returned by Commit when the selected files contain no changes.
	ErrNothingToCommit = errors.New("gitops: nothing to commit")
	// ErrInvalidInput is returned for rejected URLs, refs, paths or hosts.
	ErrInvalidInput = errors.New("gitops: invalid input")
)

// AuthKind selects the authentication method of a Remote.
type AuthKind int

// Supported authentication kinds.
const (
	AuthNone AuthKind = iota
	AuthSSH
	AuthHTTPS
)

// Auth holds credentials for one remote. Use the constructors.
type Auth struct {
	kind       AuthKind
	privateKey []byte
	knownHosts []byte
	user       string
	token      string
}

// None returns an unauthenticated Auth.
func None() Auth { return Auth{kind: AuthNone} }

// SSHKey authenticates with a PEM private key; knownHosts is in known_hosts format.
func SSHKey(privateKeyPEM, knownHosts []byte) Auth {
	return Auth{kind: AuthSSH, privateKey: privateKeyPEM, knownHosts: knownHosts}
}

// HTTPS authenticates with basic auth via an HTTP header (never via the URL).
func HTTPS(user, token string) Auth { return Auth{kind: AuthHTTPS, user: user, token: token} }

// Remote describes a repository location and how to authenticate.
type Remote struct {
	URL  string
	Auth Auth
}

// Client runs git commands. The zero value is usable.
type Client struct {
	// TempBase is the base directory for temporary credential files (default os.TempDir()).
	TempBase string
	// Timeout is the per-command timeout (default 2 minutes).
	Timeout time.Duration
	// GitBin overrides the git binary (default "git").
	GitBin string
	// AllowFileTransport permits file:// URLs and local paths. Intended for tests only.
	AllowFileTransport bool
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 2 * time.Minute
}

func (c *Client) gitBin() string {
	if c.GitBin != "" {
		return c.GitBin
	}
	return "git"
}

type invocation struct {
	env     []string
	cleanup func()
	secrets []string
	home    string
}

// prepare materializes credentials and builds a locked-down environment.
func (c *Client) prepare(r *Remote) (*invocation, error) {
	base := c.TempBase
	if base == "" {
		base = os.TempDir()
	}
	tmp, err := os.MkdirTemp(base, "acp-git-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	inv := &invocation{cleanup: func() { _ = os.RemoveAll(tmp) }}
	fail := func(err error) (*invocation, error) { inv.cleanup(); return nil, err }

	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return fail(fmt.Errorf("create home: %w", err))
	}
	cfg := [][2]string{
		{"credential.helper", ""},
		{"core.hooksPath", os.DevNull},
		{"commit.gpgsign", "false"},
		{"core.askPass", ""},
	}
	protocols := "ssh:https:http"
	if c.AllowFileTransport {
		protocols += ":file"
		cfg = append(cfg, [2]string{"protocol.file.allow", "always"})
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ALLOW_PROTOCOL=" + protocols,
		"GIT_LITERAL_PATHSPECS=1",
	}

	if r != nil {
		switch r.Auth.kind {
		case AuthSSH:
			keyPath := filepath.Join(tmp, "id")
			khPath := filepath.Join(tmp, "known_hosts")
			key := r.Auth.privateKey
			if !bytes.HasSuffix(key, []byte("\n")) {
				key = append(append([]byte{}, key...), '\n')
			}
			if err := os.WriteFile(keyPath, key, 0o600); err != nil {
				return fail(fmt.Errorf("write key: %w", err))
			}
			if err := os.WriteFile(khPath, r.Auth.knownHosts, 0o600); err != nil {
				return fail(fmt.Errorf("write known_hosts: %w", err))
			}
			env = append(env, "GIT_SSH_COMMAND=ssh -F /dev/null -i "+shQuote(keyPath)+
				" -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o BatchMode=yes -o UserKnownHostsFile="+shQuote(khPath))
		case AuthHTTPS:
			enc := base64.StdEncoding.EncodeToString([]byte(r.Auth.user + ":" + r.Auth.token))
			cfg = append(cfg, [2]string{"http.extraHeader", "Authorization: Basic " + enc})
			inv.secrets = append(inv.secrets, r.Auth.token, enc)
		}
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(cfg)))
	for i, kv := range cfg {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	inv.env = env
	inv.home = home
	return inv, nil
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var urlCredRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`)

// redact removes URL credentials and the given secrets from s.
func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return urlCredRe.ReplaceAllString(s, "${1}***@")
}

// run executes git with the given args; extraEnv is appended to the environment.
func (c *Client) run(ctx context.Context, dir string, r *Remote, extraEnv []string, args ...string) (string, error) {
	inv, err := c.prepare(r)
	if err != nil {
		return "", err
	}
	defer inv.cleanup()
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, c.gitBin(), args...) //nolint:gosec // args are validated by callers
	cmd.Dir = dir
	if dir == "" {
		cmd.Dir = inv.home // never inherit a surrounding repository
	}
	cmd.Env = append(inv.env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(redact(stderr.String(), inv.secrets...))
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], ctx.Err(), msg)
		}
		return "", &GitError{Op: args[0], Stderr: msg, err: err}
	}
	return stdout.String(), nil
}

// GitError is a failed git invocation with redacted stderr.
type GitError struct {
	Op     string
	Stderr string
	err    error
}

func (e *GitError) Error() string { return fmt.Sprintf("git %s: %v: %s", e.Op, e.err, e.Stderr) }

// Unwrap returns the underlying exec error.
func (e *GitError) Unwrap() error { return e.err }
