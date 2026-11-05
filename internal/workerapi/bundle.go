package workerapi

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/inventory"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
	"github.com/BxnnyG/ansible-control-plane/internal/runs"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
)

// TreeSource writes the project tree at a revision; *projects.Service implements it.
type TreeSource interface {
	WriteTree(ctx context.Context, ws, id uuid.UUID, rev, prefix string, w io.Writer) error
}

// InventorySource loads the stored inventory; *inventory.Service implements it.
type InventorySource interface {
	LoadDoc(ctx context.Context, ws, id uuid.UUID) (*inventory.Doc, error)
}

// SecretSource resolves stored secrets; *secrets.Service implements it.
type SecretSource interface {
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
}

// VaultSource resolves vault identities; *vaultids.Service implements it.
type VaultSource interface {
	Get(ctx context.Context, ws, id uuid.UUID) (vaultids.Identity, error)
	Password(ctx context.Context, ws uuid.UUID, label string) (vaultids.Password, error)
}

// Builder assembles run bundles. It is the only place where credentials of a
// run are decrypted.
type Builder struct {
	Trees       TreeSource
	Inventories InventorySource
	Secrets     SecretSource
	Vault       VaultSource
	Provider    secrets.Provider
}

// credentials is everything sensitive of a run, decrypted just in time.
type credentials struct {
	sshKey     []byte
	becomePass []byte
	vault      map[string]string // label -> password
	extraVars  map[string]any
	mask       []string
}

// ErrBundle marks failures to assemble a bundle that are caused by the run's
// configuration (missing or wrong credential, unsupported provider).
var ErrBundle = errors.New("cannot build bundle")

func (b *Builder) credentials(ctx context.Context, run dbgen.Run, p runs.Params) (*credentials, error) {
	c := &credentials{vault: map[string]string{}}
	var err error
	if c.extraVars, err = p.OpenSecretVars(b.Provider, run.WorkspaceID); err != nil {
		return nil, err
	}
	for _, k := range p.SecretVars {
		switch v := c.extraVars[k].(type) {
		case nil:
		case string:
			c.mask = append(c.mask, v)
		default:
			if j, err := json.Marshal(v); err == nil {
				c.mask = append(c.mask, string(j))
			}
		}
	}
	if p.SSHSecretID != nil {
		r, err := b.Secrets.Resolve(ctx, run.WorkspaceID, *p.SSHSecretID)
		switch {
		case errors.Is(err, secrets.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("resolve ssh secret: %w", err)
		case r.Type != secrets.TypeSSHKey:
			r.Value.Zero()
			return nil, fmt.Errorf("%w: the SSH credential is not an ssh_key secret", ErrBundle)
		default:
			c.sshKey = append([]byte(nil), r.Value.Reveal()...)
			r.Value.Zero()
			c.mask = append(c.mask, string(c.sshKey))
		}
	}
	if p.BecomeSecretID != nil {
		r, err := b.Secrets.Resolve(ctx, run.WorkspaceID, *p.BecomeSecretID)
		switch {
		case errors.Is(err, secrets.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("resolve become secret: %w", err)
		default:
			c.becomePass = append([]byte(nil), r.Value.Reveal()...)
			r.Value.Zero()
			c.mask = append(c.mask, strings.TrimRight(string(c.becomePass), "\r\n"))
		}
	}
	for _, id := range p.VaultIdentityIDs {
		ident, err := b.Vault.Get(ctx, run.WorkspaceID, id)
		if errors.Is(err, vaultids.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load vault identity: %w", err)
		}
		pw, err := b.Vault.Password(ctx, run.WorkspaceID, ident.Label)
		if err != nil {
			return nil, fmt.Errorf("%w: vault identity %q: %w", ErrBundle, ident.Label, err)
		}
		c.vault[ident.Label] = pw.Reveal()
		c.mask = append(c.mask, pw.Reveal())
		pw.Zero()
	}
	return c, nil
}

// MaskValues returns the secret values of the run that must never reach the
// log.
func (b *Builder) MaskValues(ctx context.Context, run dbgen.Run) ([]string, error) {
	p, err := runs.DecodeParams(run.Params)
	if err != nil {
		return nil, err
	}
	c, err := b.credentials(ctx, run, p)
	if err != nil {
		return nil, err
	}
	return c.mask, nil
}

// spec builds the command specification.
func spec(run dbgen.Run, p runs.Params, c *credentials) (runproto.Spec, error) {
	env := make(map[string]string, len(p.EnvVars))
	for k, v := range p.EnvVars {
		switch x := v.(type) {
		case string:
			env[k] = x
		case bool:
			env[k] = strconv.FormatBool(x)
		case float64:
			env[k] = strconv.FormatFloat(x, 'f', -1, 64)
		default:
			return runproto.Spec{}, fmt.Errorf("%w: environment variable %s has an unsupported type", ErrBundle, k)
		}
	}
	s := runproto.Spec{
		RunID: run.ID.String(), Revision: p.ProjectRevision, Playbook: p.Playbook, Limit: p.Limit, Tags: p.Tags,
		SkipTags: p.SkipTags, Check: p.Check, Diff: p.Diff, Verbosity: p.Verbosity, Env: env,
		SSHKey: c.sshKey != nil, Become: c.becomePass != nil, Mask: c.mask,
	}
	s.VaultIDs = slices.Sorted(maps.Keys(c.vault))
	return s, nil
}

// Build writes the bundle of the run as tar.gz to w: spec, unmasked inventory,
// extra variables, credentials and the project tree at the pinned revision.
func (b *Builder) Build(ctx context.Context, run dbgen.Run, w io.Writer) (maskValues []string, err error) {
	p, err := runs.DecodeParams(run.Params)
	if err != nil {
		return nil, err
	}
	c, err := b.credentials(ctx, run, p)
	if err != nil {
		return nil, err
	}
	sp, err := spec(run, p, c)
	if err != nil {
		return nil, err
	}
	doc, err := b.Inventories.LoadDoc(ctx, run.WorkspaceID, p.InventoryID)
	if err != nil {
		return nil, fmt.Errorf("%w: load inventory: %w", ErrBundle, err)
	}
	// Vaulted inventory values stay encrypted; the worker has the vault passwords.
	invJSON, err := json.Marshal(inventory.Render(doc, inventory.RenderOptions{}))
	if err != nil {
		return nil, fmt.Errorf("encode inventory: %w", err)
	}
	if c.extraVars == nil {
		c.extraVars = map[string]any{}
	}
	evJSON, err := json.Marshal(c.extraVars)
	if err != nil {
		return nil, fmt.Errorf("encode extra vars: %w", err)
	}
	specJSON, err := json.Marshal(sp)
	if err != nil {
		return nil, fmt.Errorf("encode spec: %w", err)
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()
	put := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: now, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	type file struct {
		name string
		data []byte
	}
	files := []file{{runproto.FileSpec, specJSON}, {runproto.FileInventory, invJSON}, {runproto.FileExtraVars, evJSON}}
	if c.sshKey != nil {
		files = append(files, file{runproto.FileSSHKey, c.sshKey})
	}
	if c.becomePass != nil {
		files = append(files, file{runproto.FileBecomePass, append(append([]byte(nil), strings.TrimRight(string(c.becomePass), "\r\n")...), '\n')})
	}
	for _, l := range sp.VaultIDs {
		files = append(files, file{runproto.DirVault + l, []byte(c.vault[l] + "\n")})
	}
	for _, f := range files {
		if err := put(f.name, f.data); err != nil {
			return nil, fmt.Errorf("write bundle: %w", err)
		}
	}
	if err := b.copyTree(ctx, run, p, tw); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("write bundle: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("write bundle: %w", err)
	}
	return c.mask, nil
}

// copyTree copies the entries of the project archive into tw.
func (b *Builder) copyTree(ctx context.Context, run dbgen.Run, p runs.Params, tw *tar.Writer) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := b.Trees.WriteTree(ctx, run.WorkspaceID, p.ProjectID, p.ProjectRevision, runproto.DirProject+"/", pw)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	tr := tar.NewReader(pr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = pr.CloseWithError(err)
			_ = wait(done)
			return fmt.Errorf("%w: read project tree: %w", ErrBundle, err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if clean := path.Clean(h.Name); strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			_ = pr.CloseWithError(err)
			_ = wait(done)
			return fmt.Errorf("write bundle: %w", err)
		}
		if _, err := io.CopyN(tw, tr, h.Size); err != nil {
			_ = pr.CloseWithError(err)
			_ = wait(done)
			return fmt.Errorf("write bundle: %w", err)
		}
	}
	if err := <-done; err != nil {
		return fmt.Errorf("%w: project tree: %w", ErrBundle, err)
	}
	return nil
}

func wait(c <-chan error) error { return <-c }
