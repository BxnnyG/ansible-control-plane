// Package projects manages Git projects of a workspace: CRUD, host key
// pinning (TOFU with confirmation) and on-demand sync (ADR-0013).
package projects

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/gitops"
	"github.com/BxnnyG/ansible-control-plane/internal/metrics"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Errors returned by the service; the HTTP layer maps them to status codes.
var (
	ErrNotFound         = errors.New("project not found")
	ErrConflict         = errors.New("a project with this name or slug already exists")
	ErrInvalid          = errors.New("invalid project")
	ErrHostKeyNotPinned = errors.New("ssh host key is not pinned; scan and confirm the host key first")
	ErrHostKeyMismatch  = errors.New("fingerprint does not match any key presented by the server")
	ErrNotSSH           = errors.New("project url is not an ssh url")
	// ErrInsecureCredential: a password/token credential must not be sent over plain http.
	ErrInsecureCredential = errors.New("credentials require an https or ssh url; http urls cannot carry a credential")
	// ErrCredentialReselect: the remote host or scheme changed, so the credential must be chosen again.
	ErrCredentialReselect = errors.New("the remote host or scheme changed; select the credential again or remove it")
	ErrSyncInProgress     = errors.New("a sync of this project is already running")
)

// RemoteError is a failed remote operation (ls-remote or sync). The message is
// already redacted by gitops and contains no credentials.
type RemoteError struct{ Msg string }

func (e *RemoteError) Error() string { return e.Msg }

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Store is the persistence of the service; *dbgen.Queries implements it.
type Store interface {
	CreateProject(ctx context.Context, arg dbgen.CreateProjectParams) (dbgen.Project, error)
	GetProject(ctx context.Context, arg dbgen.GetProjectParams) (dbgen.Project, error)
	ListProjects(ctx context.Context, workspaceID uuid.UUID) ([]dbgen.Project, error)
	UpdateProject(ctx context.Context, arg dbgen.UpdateProjectParams) (dbgen.Project, error)
	SetProjectKnownHosts(ctx context.Context, arg dbgen.SetProjectKnownHostsParams) (dbgen.Project, error)
	SetProjectSyncResult(ctx context.Context, arg dbgen.SetProjectSyncResultParams) error
	DeleteProject(ctx context.Context, arg dbgen.DeleteProjectParams) (int64, error)
	UpsertProjectRevision(ctx context.Context, arg dbgen.UpsertProjectRevisionParams) (dbgen.ProjectRevision, error)
	ListProjectRevisions(ctx context.Context, arg dbgen.ListProjectRevisionsParams) ([]dbgen.ProjectRevision, error)
	GetLatestProjectRevision(ctx context.Context, projectID uuid.UUID) (dbgen.ProjectRevision, error)
	GetProjectByID(ctx context.Context, id uuid.UUID) (dbgen.Project, error)
	SetProjectWebhook(ctx context.Context, arg dbgen.SetProjectWebhookParams) (dbgen.Project, error)
	ClaimDueProjects(ctx context.Context, limit int32) ([]dbgen.Project, error)
	InsertAudit(ctx context.Context, arg dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error)
}

// Secrets is the part of the secrets service used for Git credentials.
type Secrets interface {
	Get(ctx context.Context, ws, id uuid.UUID) (secrets.Meta, error)
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
	Create(ctx context.Context, ws uuid.UUID, in secrets.CreateInput) (secrets.Meta, error)
	Update(ctx context.Context, ws, id uuid.UUID, in secrets.UpdateInput) (secrets.Meta, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
}

// Git is the part of gitops.Client used by the service.
type Git interface {
	LsRemote(ctx context.Context, r gitops.Remote) error
	Sync(ctx context.Context, dir string, r gitops.Remote, ref string) (string, error)
	ScanHostKey(ctx context.Context, host string, port int) ([]gitops.HostKey, error)
	ValidateURL(u string) error
	FetchBranch(ctx context.Context, dir string, r gitops.Remote, branch string) (string, bool, error)
	HasCommit(ctx context.Context, dir, rev string) bool
	CheckoutDetached(ctx context.Context, dir, rev string) error
	Reset(ctx context.Context, dir, rev string) error
	ChangedPaths(ctx context.Context, dir, from, to string, paths []string) ([]string, error)
	StagedDiff(ctx context.Context, dir string, paths []string) (string, error)
	Commit(ctx context.Context, dir string, files []string, name, email, message string, signoff bool) (string, error)
	Push(ctx context.Context, dir string, r gitops.Remote, branch string) error
	Archive(ctx context.Context, dir, rev, prefix string, w io.Writer) error
}

// Scanner inspects a synced checkout and returns the scan result as JSON.
type Scanner interface {
	Scan(ctx context.Context, dir string) (json.RawMessage, error)
}

// NopScanner returns an empty scan result.
type NopScanner struct{}

// Scan implements Scanner.
func (NopScanner) Scan(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

// Options configures a Service.
type Options struct {
	Store   Store
	Secrets Secrets
	Git     Git
	Scanner Scanner // default NopScanner
	DataDir string  // cache root; clones live in <DataDir>/projects/<id>
	// SyncTimeout bounds one sync including the scan (default 5 minutes).
	SyncTimeout time.Duration
}

// Service implements project management.
type Service struct {
	o     Options
	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
	// hookSem bounds concurrent webhook-triggered syncs; hookWG tracks them.
	hookSem chan struct{}
	hookWG  sync.WaitGroup
}

// New builds a Service.
func New(o Options) *Service {
	if o.Scanner == nil {
		o.Scanner = NopScanner{}
	}
	if o.SyncTimeout <= 0 {
		o.SyncTimeout = 5 * time.Minute
	}
	return &Service{o: o, locks: map[uuid.UUID]*sync.Mutex{}, hookSem: make(chan struct{}, 2)}
}

// Project is the API view of a project. It never contains credentials.
type Project struct {
	ID                  uuid.UUID  `json:"id"`
	Name                string     `json:"name"`
	Slug                string     `json:"slug"`
	URL                 string     `json:"url"`
	DefaultRef          string     `json:"default_ref"`
	CredentialSecretID  *uuid.UUID `json:"credential_secret_id"`
	HTTPSUsername       string     `json:"https_username"`
	SSH                 bool       `json:"ssh"`
	HostKeyPinned       bool       `json:"host_key_pinned"`
	HostKeyFingerprints []string   `json:"host_key_fingerprints"`
	SyncIntervalSeconds *int       `json:"sync_interval_seconds"`
	LastSyncStatus      string     `json:"last_sync_status"`
	LastSyncError       string     `json:"last_sync_error"`
	LastSyncSHA         string     `json:"last_sync_sha"`
	LastSyncAt          *time.Time `json:"last_sync_at"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// Revision is one synced commit with its scan result.
type Revision struct {
	ID       uuid.UUID       `json:"id"`
	SHA      string          `json:"sha"`
	SyncedAt time.Time       `json:"synced_at"`
	Scan     json.RawMessage `json:"scan"`
	Warnings json.RawMessage `json:"warnings"`
}

// HostKeyInfo is a scanned host key offered for confirmation.
type HostKeyInfo struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
}

// Field is an optional, nullable JSON value: Set tells whether the key was
// present, a nil Value means JSON null.
type Field[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *Field[T]) UnmarshalJSON(b []byte) error {
	f.Set = true
	if string(b) == "null" {
		f.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	f.Value = &v
	return nil
}

// CreateInput describes a new project.
type CreateInput struct {
	Name, Slug, URL, DefaultRef, HTTPSUsername string
	CredentialSecretID                         *uuid.UUID
	SyncIntervalSeconds                        *int
	CreatedBy                                  uuid.UUID
}

// UpdateInput changes a project; unset fields stay untouched.
type UpdateInput struct {
	Name, Slug, URL, DefaultRef, HTTPSUsername *string
	CredentialSecretID                         Field[uuid.UUID]
	SyncIntervalSeconds                        Field[int]
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Slugify derives a slug from a name; it returns "" when nothing usable remains.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}

var scpHostRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@([A-Za-z0-9.-]+):[^:]`)

// SSHTarget returns host and port for ssh:// and scp-like URLs.
func SSHTarget(raw string) (host string, port int, ok bool) {
	if strings.HasPrefix(strings.ToLower(raw), "ssh://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", 0, false
		}
		if p := u.Port(); p != "" {
			if _, err := fmt.Sscanf(p, "%d", &port); err != nil {
				return "", 0, false
			}
		}
		return u.Hostname(), port, true
	}
	if m := scpHostRe.FindStringSubmatch(raw); m != nil {
		return m[1], 0, true
	}
	return "", 0, false
}

// remoteIdentity reduces a remote URL to scheme and host:port (lower case) so
// that a credential can be tied to the system it was meant for.
func remoteIdentity(raw string) string {
	if h, p, ok := SSHTarget(raw); ok {
		return fmt.Sprintf("ssh://%s:%d", strings.ToLower(h), p)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid:" + raw
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + host + ":" + port
}

func isHTTP(raw string) bool {
	l := strings.ToLower(raw)
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")
}

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}

func clean(s string, max int, required bool, what string) (string, error) {
	s = strings.TrimSpace(s)
	if (required && s == "") || utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n") {
		return "", invalid("%s must be 1-%d characters", what, max)
	}
	return s, nil
}

// spec is the validated, normalized set of editable fields.
type spec struct {
	name, slug, url, ref, user string
	cred                       *uuid.UUID
	interval                   *int
}

func (s *Service) validate(ctx context.Context, ws uuid.UUID, in spec) (spec, error) {
	var err error
	if in.name, err = clean(in.name, 100, true, "name"); err != nil {
		return in, err
	}
	if in.slug == "" {
		in.slug = Slugify(in.name)
	}
	if !slugRe.MatchString(in.slug) {
		return in, invalid("slug must match %s", slugRe)
	}
	if in.url, err = clean(in.url, 500, true, "url"); err != nil {
		return in, err
	}
	if err := s.o.Git.ValidateURL(in.url); err != nil {
		return in, invalid("url is not acceptable")
	}
	if isHTTP(in.url) {
		u, perr := url.Parse(in.url)
		if perr != nil || u.Host == "" {
			return in, invalid("url is malformed")
		}
		if u.User != nil {
			return in, invalid("url must not contain credentials; use a credential secret")
		}
	}
	if in.ref == "" {
		in.ref = "main"
	}
	if in.ref, err = clean(in.ref, 200, true, "default_ref"); err != nil || strings.HasPrefix(in.ref, "-") || strings.ContainsAny(in.ref, " \t") {
		return in, invalid("default_ref is not acceptable")
	}
	if in.user, err = clean(in.user, 100, false, "https_username"); err != nil {
		return in, err
	}
	if in.interval != nil && (*in.interval < 60 || *in.interval > 30*24*3600) {
		return in, invalid("sync_interval_seconds must be between 60 and 2592000")
	}
	_, _, ssh := SSHTarget(in.url)
	if in.cred == nil {
		if ssh {
			return in, invalid("ssh urls need a credential of type ssh_key")
		}
		return in, nil
	}
	if strings.HasPrefix(strings.ToLower(in.url), "http://") {
		return in, ErrInsecureCredential
	}
	m, err := s.o.Secrets.Get(ctx, ws, *in.cred)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return in, invalid("credential secret not found")
		}
		return in, fmt.Errorf("get credential: %w", err)
	}
	switch {
	case ssh && m.Type != secrets.TypeSSHKey:
		return in, invalid("ssh urls need a credential of type ssh_key")
	case !ssh && m.Type != secrets.TypePassword && m.Type != secrets.TypeToken:
		return in, invalid("this url needs a credential of type password or token")
	case !ssh && m.Type == secrets.TypePassword && in.user == "":
		return in, invalid("https_username is required for password credentials")
	}
	return in, nil
}

// Create validates and stores a project.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (Project, error) {
	sp, err := s.validate(ctx, ws, spec{name: in.Name, slug: in.Slug, url: in.URL, ref: in.DefaultRef,
		user: in.HTTPSUsername, cred: in.CredentialSecretID, interval: in.SyncIntervalSeconds})
	if err != nil {
		return Project{}, err
	}
	row, err := s.o.Store.CreateProject(ctx, dbgen.CreateProjectParams{
		WorkspaceID: ws, Name: sp.name, Slug: sp.slug, Url: sp.url, DefaultRef: sp.ref,
		CredentialSecretID: toPgUUID(sp.cred), HttpsUsername: sp.user, SyncIntervalSeconds: toPgInt(sp.interval),
		CreatedBy: pgtype.UUID{Bytes: in.CreatedBy, Valid: in.CreatedBy != uuid.Nil},
	})
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", mapErr(err))
	}
	return toProject(row), nil
}

func (s *Service) load(ctx context.Context, ws, id uuid.UUID) (dbgen.Project, error) {
	row, err := s.o.Store.GetProject(ctx, dbgen.GetProjectParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return row, mapErr(err)
	}
	return row, nil
}

// Get returns one project.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (Project, error) {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return Project{}, err
	}
	return toProject(row), nil
}

// List returns the projects of a workspace ordered by name.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]Project, error) {
	rows, err := s.o.Store.ListProjects(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	out := make([]Project, len(rows))
	for i, r := range rows {
		out[i] = toProject(r)
	}
	return out, nil
}

// Update changes a project. A changed SSH target clears the pinned host key.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in UpdateInput) (Project, error) {
	cur, err := s.load(ctx, ws, id)
	if err != nil {
		return Project{}, err
	}
	sp := spec{name: cur.Name, slug: cur.Slug, url: cur.Url, ref: cur.DefaultRef, user: cur.HttpsUsername,
		cred: fromPgUUID(cur.CredentialSecretID), interval: fromPgInt(cur.SyncIntervalSeconds)}
	if in.Name != nil {
		sp.name = *in.Name
	}
	if in.Slug != nil {
		sp.slug = *in.Slug
	}
	if in.URL != nil {
		sp.url = *in.URL
	}
	if in.DefaultRef != nil {
		sp.ref = *in.DefaultRef
	}
	if in.HTTPSUsername != nil {
		sp.user = *in.HTTPSUsername
	}
	if in.CredentialSecretID.Set {
		sp.cred = in.CredentialSecretID.Value
	}
	if in.SyncIntervalSeconds.Set {
		sp.interval = in.SyncIntervalSeconds.Value
	}
	if sp.cred != nil && !in.CredentialSecretID.Set && remoteIdentity(cur.Url) != remoteIdentity(sp.url) {
		return Project{}, ErrCredentialReselect
	}
	if sp, err = s.validate(ctx, ws, sp); err != nil {
		return Project{}, err
	}
	known := cur.KnownHosts
	oh, op, ook := SSHTarget(cur.Url)
	nh, np, nok := SSHTarget(sp.url)
	if !nok || !ook || oh != nh || op != np {
		known = ""
	}
	row, err := s.o.Store.UpdateProject(ctx, dbgen.UpdateProjectParams{
		ID: id, WorkspaceID: ws, Name: sp.name, Slug: sp.slug, Url: sp.url, DefaultRef: sp.ref,
		CredentialSecretID: toPgUUID(sp.cred), HttpsUsername: sp.user,
		SyncIntervalSeconds: toPgInt(sp.interval), KnownHosts: known,
	})
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", mapErr(err))
	}
	return toProject(row), nil
}

// Delete removes a project and its cache clone.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	prev, err := s.load(ctx, ws, id)
	if err != nil {
		return err
	}
	l := s.lock(id)
	l.Lock()
	defer l.Unlock()
	n, err := s.o.Store.DeleteProject(ctx, dbgen.DeleteProjectParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	if prev.WebhookSecretID.Valid { // best effort: the secret is unreferenced now
		_ = s.o.Secrets.Delete(ctx, ws, prev.WebhookSecretID.Bytes)
	}
	if err := os.RemoveAll(s.cacheDir(id)); err != nil {
		return fmt.Errorf("remove cache: %w", err)
	}
	s.mu.Lock()
	delete(s.locks, id)
	s.mu.Unlock()
	return nil
}

func (s *Service) lock(id uuid.UUID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

func (s *Service) cacheDir(id uuid.UUID) string {
	return filepath.Join(s.o.DataDir, "projects", id.String())
}

// ScanHostKeys fetches the host keys of an SSH project for confirmation. Nothing is stored.
func (s *Service) ScanHostKeys(ctx context.Context, ws, id uuid.UUID) ([]HostKeyInfo, error) {
	_, keys, err := s.scanKeys(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	out := make([]HostKeyInfo, len(keys))
	for i, k := range keys {
		out[i] = HostKeyInfo{Type: k.Type, Fingerprint: k.Fingerprint}
	}
	return out, nil
}

func (s *Service) scanKeys(ctx context.Context, ws, id uuid.UUID) (dbgen.Project, []gitops.HostKey, error) {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return row, nil, err
	}
	host, port, ok := SSHTarget(row.Url)
	if !ok {
		return row, nil, ErrNotSSH
	}
	keys, err := s.o.Git.ScanHostKey(ctx, host, port)
	if err != nil {
		return row, nil, &RemoteError{Msg: err.Error()}
	}
	return row, keys, nil
}

// ConfirmHostKey pins the scanned key(s) with the given fingerprint as the
// project's known_hosts. The server is scanned again, so only a key it
// currently presents can be pinned.
func (s *Service) ConfirmHostKey(ctx context.Context, ws, id uuid.UUID, fingerprint string) (Project, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if !strings.HasPrefix(fingerprint, "SHA256:") || len(fingerprint) < 10 {
		return Project{}, invalid("fingerprint must be in SHA256:... format")
	}
	_, keys, err := s.scanKeys(ctx, ws, id)
	if err != nil {
		return Project{}, err
	}
	var lines []string
	for _, k := range keys {
		if k.Fingerprint == fingerprint {
			lines = append(lines, k.KnownHosts)
		}
	}
	if len(lines) == 0 {
		return Project{}, ErrHostKeyMismatch
	}
	row, err := s.o.Store.SetProjectKnownHosts(ctx, dbgen.SetProjectKnownHostsParams{
		ID: id, WorkspaceID: ws, KnownHosts: strings.Join(lines, "\n") + "\n",
	})
	if err != nil {
		return Project{}, fmt.Errorf("pin host key: %w", mapErr(err))
	}
	return toProject(row), nil
}

// remote resolves the credential and builds the gitops remote. The returned
// function wipes the decrypted credential.
func (s *Service) remote(ctx context.Context, p dbgen.Project) (gitops.Remote, func(), error) {
	r := gitops.Remote{URL: p.Url, Auth: gitops.None()}
	_, _, ssh := SSHTarget(p.Url)
	if ssh && strings.TrimSpace(p.KnownHosts) == "" {
		return r, func() {}, ErrHostKeyNotPinned
	}
	if !p.CredentialSecretID.Valid {
		if ssh {
			return r, func() {}, invalid("ssh urls need a credential of type ssh_key")
		}
		return r, func() {}, nil
	}
	res, err := s.o.Secrets.Resolve(ctx, p.WorkspaceID, p.CredentialSecretID.Bytes)
	if err != nil {
		return r, func() {}, fmt.Errorf("resolve credential: %w", err)
	}
	wipe := func() { res.Value.Zero() }
	switch {
	case ssh && res.Type == secrets.TypeSSHKey:
		r.Auth = gitops.SSHKey(res.Value.Reveal(), []byte(p.KnownHosts))
	case !ssh && (res.Type == secrets.TypePassword || res.Type == secrets.TypeToken):
		user := p.HttpsUsername
		if user == "" {
			user = "git"
		}
		r.Auth = gitops.HTTPS(user, string(res.Value.Reveal()))
	default:
		wipe()
		return r, func() {}, invalid("credential type %q does not fit the project url", res.Type)
	}
	return r, wipe, nil
}

func remoteErr(err error) error {
	msg := err.Error()
	if len(msg) > 2000 {
		msg = msg[:2000] + "..."
	}
	return &RemoteError{Msg: msg}
}

// Test checks connectivity and credentials with ls-remote.
func (s *Service) Test(ctx context.Context, ws, id uuid.UUID) error {
	row, err := s.load(ctx, ws, id)
	if err != nil {
		return err
	}
	r, wipe, err := s.remote(ctx, row)
	if err != nil {
		return err
	}
	defer wipe()
	if err := s.o.Git.LsRemote(ctx, r); err != nil {
		if errors.Is(err, gitops.ErrInvalidInput) {
			return invalid("%v", err)
		}
		return remoteErr(err)
	}
	return nil
}

// Sync fetches the default ref into the cache clone, scans it and records a
// revision. Concurrent syncs of one project are rejected with ErrSyncInProgress.
func (s *Service) Sync(ctx context.Context, ws, id uuid.UUID) (Revision, error) {
	return s.syncAs(ctx, ws, id, TriggerManual)
}

func (s *Service) syncAs(ctx context.Context, ws, id uuid.UUID, trigger string) (Revision, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return Revision{}, err
	}
	l := s.lock(id)
	if !l.TryLock() {
		return Revision{}, ErrSyncInProgress
	}
	defer l.Unlock()
	row, err := s.load(ctx, ws, id) // re-read: the project may have changed while waiting
	if err != nil {
		return Revision{}, err
	}
	r, wipe, err := s.remote(ctx, row)
	if err != nil {
		return Revision{}, err
	}
	defer wipe()

	rctx, cancel := context.WithTimeout(ctx, s.o.SyncTimeout)
	defer cancel()
	dir := s.cacheDir(id)
	record := func(status, msg, sha string) {
		metrics.ProjectSyncs.WithLabelValues(status).Inc()
		sctx, c := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer c()
		_ = s.o.Store.SetProjectSyncResult(sctx, dbgen.SetProjectSyncResultParams{
			ID: id, LastSyncStatus: status, LastSyncError: msg, LastSyncSha: sha, LastSyncTrigger: trigger})
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return Revision{}, fmt.Errorf("create cache dir: %w", err)
	}
	sha, err := s.o.Git.Sync(rctx, dir, r, row.DefaultRef)
	if err != nil {
		re := remoteErr(err)
		record("error", re.Error(), row.LastSyncSha)
		if errors.Is(err, gitops.ErrInvalidInput) {
			return Revision{}, invalid("%v", err)
		}
		return Revision{}, re
	}
	scan, warnings := json.RawMessage(`{}`), []string{}
	if sc, serr := s.o.Scanner.Scan(rctx, dir); serr != nil {
		warnings = append(warnings, "scan failed: "+serr.Error())
	} else if len(sc) > 0 && json.Valid(sc) {
		scan = sc
	}
	wb, _ := json.Marshal(warnings)
	rev, err := s.o.Store.UpsertProjectRevision(ctx, dbgen.UpsertProjectRevisionParams{
		ProjectID: id, Sha: sha, Scan: scan, Warnings: wb})
	if err != nil {
		record("error", "record revision failed", row.LastSyncSha)
		return Revision{}, fmt.Errorf("record revision: %w", err)
	}
	record("ok", "", sha)
	return toRevision(rev), nil
}

// Revisions lists the latest revisions of a project, newest first.
func (s *Service) Revisions(ctx context.Context, ws, id uuid.UUID, limit int) ([]Revision, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.o.Store.ListProjectRevisions(ctx, dbgen.ListProjectRevisionsParams{ProjectID: id, Limit: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	out := make([]Revision, len(rows))
	for i, r := range rows {
		out[i] = toRevision(r)
	}
	return out, nil
}

// LatestRevision returns the most recent revision or ErrNotFound if never synced.
func (s *Service) LatestRevision(ctx context.Context, ws, id uuid.UUID) (Revision, error) {
	if _, err := s.load(ctx, ws, id); err != nil {
		return Revision{}, err
	}
	r, err := s.o.Store.GetLatestProjectRevision(ctx, id)
	if err != nil {
		return Revision{}, mapErr(err)
	}
	return toRevision(r), nil
}

func toRevision(r dbgen.ProjectRevision) Revision {
	return Revision{ID: r.ID, SHA: r.Sha, SyncedAt: r.SyncedAt.Time, Scan: r.Scan, Warnings: r.Warnings}
}

func toProject(r dbgen.Project) Project {
	_, _, ssh := SSHTarget(r.Url)
	p := Project{
		ID: r.ID, Name: r.Name, Slug: r.Slug, URL: r.Url, DefaultRef: r.DefaultRef,
		CredentialSecretID: fromPgUUID(r.CredentialSecretID), HTTPSUsername: r.HttpsUsername,
		SSH: ssh, HostKeyFingerprints: fingerprints(r.KnownHosts),
		SyncIntervalSeconds: fromPgInt(r.SyncIntervalSeconds), LastSyncStatus: r.LastSyncStatus,
		LastSyncError: r.LastSyncError, LastSyncSHA: r.LastSyncSha,
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	p.HostKeyPinned = len(p.HostKeyFingerprints) > 0
	if r.LastSyncAt.Valid {
		t := r.LastSyncAt.Time
		p.LastSyncAt = &t
	}
	return p
}

func fingerprints(knownHosts string) []string {
	out := []string{}
	for _, line := range strings.Split(knownHosts, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			continue
		}
		sum := sha256.Sum256(raw)
		out = append(out, "SHA256:"+base64.RawStdEncoding.EncodeToString(sum[:]))
	}
	return out
}

func toPgUUID(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}

func fromPgUUID(u pgtype.UUID) *uuid.UUID {
	if !u.Valid {
		return nil
	}
	id := uuid.UUID(u.Bytes)
	return &id
}

func toPgInt(i *int) pgtype.Int4 {
	if i == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*i), Valid: true} //nolint:gosec // range-checked in validate
}

func fromPgInt(i pgtype.Int4) *int {
	if !i.Valid {
		return nil
	}
	v := int(i.Int32)
	return &v
}
