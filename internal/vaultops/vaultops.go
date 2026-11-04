// Package vaultops implements the vault operations on project files
// (ADR-0015): listing, decrypting for view, encrypting and rekeying. Changes
// are committed through the Git write-back path of the projects package;
// plaintext and passwords are never logged, stored or returned in errors.
package vaultops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/BxnnyG/ansible-control-plane/internal/projects"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
	"github.com/BxnnyG/ansible-control-plane/internal/vault"
	"github.com/BxnnyG/ansible-control-plane/internal/vaultids"
)

const (
	// MaxPlaintext is the largest plaintext accepted for encryption.
	MaxPlaintext = 1 << 20
	// maxFileRead bounds files read for decrypt and rekey.
	maxFileRead = 4 << 20
	// DefaultBranch is the write branch used when the request names none.
	DefaultBranch = "acp/vault"
)

var (
	// ErrInvalid wraps validation failures; the message is safe to show to clients.
	ErrInvalid = errors.New("invalid vault request")
	// ErrFileNotFound is returned when the path does not exist in the checkout.
	ErrFileNotFound = errors.New("file not found in the project")
	// ErrKeyNotFound is returned when a YAML key path does not exist.
	ErrKeyNotFound = errors.New("yaml key path not found")
	// ErrNotVault is returned when the target holds no vault-encrypted data.
	ErrNotVault = errors.New("the target is not vault encrypted")
	// ErrNoIdentity is returned when no vault identity matches.
	ErrNoIdentity = errors.New("no matching vault identity")
	// ErrDecrypt is returned for a wrong password or corrupted data.
	ErrDecrypt = errors.New("decryption failed (wrong password or corrupted data)")
	// ErrTooLarge is returned when the plaintext or file exceeds the limit.
	ErrTooLarge = errors.New("content too large")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.HasSuffix(b, ".")
}

// Projects is the part of the projects service used here.
type Projects interface {
	WithCheckout(ctx context.Context, ws, id uuid.UUID, fn func(root *os.Root, sha string) error) error
	WriteBack(ctx context.Context, ws, id uuid.UUID, req projects.WriteRequest) (projects.WriteResult, error)
	LatestRevision(ctx context.Context, ws, id uuid.UUID) (projects.Revision, error)
}

// Passwords resolves vault passwords; *vaultids.Service implements it.
type Passwords interface {
	Password(ctx context.Context, ws uuid.UUID, label string) (vaultids.Password, error)
}

// Service runs vault operations on projects.
type Service struct {
	prj Projects
	pw  Passwords
}

// New builds a Service.
func New(prj Projects, pw Passwords) *Service { return &Service{prj: prj, pw: pw} }

// Listing is the vault inventory of a project's latest revision.
type Listing struct {
	SHA         string             `json:"sha"`
	VaultFiles  []scan.VaultFile   `json:"vault_files"`
	InlineVault []scan.InlineVault `json:"inline_vault"`
}

// List returns the vault files and inline vault locations found by the scan
// of the latest synced revision.
func (s *Service) List(ctx context.Context, ws, project uuid.UUID) (Listing, error) {
	rev, err := s.prj.LatestRevision(ctx, ws, project)
	if err != nil {
		return Listing{}, fmt.Errorf("latest revision: %w", err)
	}
	var res scan.Result
	if err := json.Unmarshal(rev.Scan, &res); err != nil {
		return Listing{}, fmt.Errorf("decode scan: %w", err)
	}
	l := Listing{SHA: rev.SHA, VaultFiles: res.VaultFiles, InlineVault: res.InlineVault}
	if l.VaultFiles == nil {
		l.VaultFiles = []scan.VaultFile{}
	}
	if l.InlineVault == nil {
		l.InlineVault = []scan.InlineVault{}
	}
	return l, nil
}

// pwCache resolves and caches passwords per label for one operation.
type pwCache struct {
	s   *Service
	ws  uuid.UUID
	pws map[string]*vaultids.Password
}

func (c *pwCache) zero() {
	for _, p := range c.pws {
		p.Zero()
	}
}

// exact resolves the identity with exactly this label ("" means default).
func (c *pwCache) exact(ctx context.Context, label string) (string, error) {
	if label == "" {
		label = vaultids.DefaultLabel
	}
	if p, ok := c.pws[label]; ok {
		return p.Reveal(), nil
	}
	p, err := c.s.pw.Password(ctx, c.ws, label)
	switch {
	case errors.Is(err, vaultids.ErrNotFound), errors.Is(err, vaultids.ErrNoPassword):
		return "", fmt.Errorf("%w: %q", ErrNoIdentity, label)
	case err != nil:
		return "", err
	}
	if c.pws == nil {
		c.pws = map[string]*vaultids.Password{}
	}
	c.pws[label] = &p
	return p.Reveal(), nil
}

// forEnvelope resolves the password for an envelope: the identity named by
// the vault-id in its header, else the default identity.
func (c *pwCache) forEnvelope(ctx context.Context, envelope []byte) (pw, label string, err error) {
	h, err := vault.ParseHeader(envelope)
	if err != nil {
		return "", "", ErrNotVault
	}
	if h.VaultID != "" {
		pw, err := c.exact(ctx, h.VaultID)
		if err == nil || !errors.Is(err, ErrNoIdentity) {
			return pw, h.VaultID, err
		}
	}
	pw, err = c.exact(ctx, vaultids.DefaultLabel)
	return pw, vaultids.DefaultLabel, err
}

func cleanFile(p string) (string, error) {
	if p == "" || p == "." || !fs.ValidPath(p) || strings.ContainsAny(p, "\x00\r\n\\") {
		return "", invalid("path %q is not acceptable", p)
	}
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".git") {
			return "", invalid("path %q is not acceptable", p)
		}
	}
	return p, nil
}

func readFile(root *os.Root, p string) ([]byte, error) {
	f, err := root.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrFileNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, invalid("%s is not a regular file", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileRead+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(data) > maxFileRead {
		return nil, ErrTooLarge
	}
	return data, nil
}

func mapVaultErr(err error) error {
	switch {
	case errors.Is(err, vault.ErrDecrypt):
		return ErrDecrypt
	case errors.Is(err, vault.ErrFormat):
		return ErrNotVault
	}
	return err
}

// DecryptInput selects what to decrypt. KeyPath empty means the whole file.
type DecryptInput struct {
	Path    string
	KeyPath string
}

// Decrypted is plaintext for display. The caller must not log or store it.
type Decrypted struct {
	Plaintext []byte
	Identity  string // label of the identity that decrypted
}

// Decrypt decrypts a vault file or one inline `!vault` value of the latest
// synced revision. The identity is chosen by the vault-id in the envelope
// header, falling back to the "default" identity.
func (s *Service) Decrypt(ctx context.Context, ws, project uuid.UUID, in DecryptInput) (Decrypted, error) {
	p, err := cleanFile(in.Path)
	if err != nil {
		return Decrypted{}, err
	}
	var segs []pathSeg
	if in.KeyPath != "" {
		if segs, err = parseKeyPath(in.KeyPath); err != nil {
			return Decrypted{}, err
		}
	}
	var envelope []byte
	err = s.prj.WithCheckout(ctx, ws, project, func(root *os.Root, _ string) error {
		data, err := readFile(root, p)
		if err != nil {
			return err
		}
		envelope, err = extractEnvelope(data, segs)
		return err
	})
	if err != nil {
		return Decrypted{}, err
	}
	c := pwCache{s: s, ws: ws}
	defer c.zero()
	pw, label, err := c.forEnvelope(ctx, envelope)
	if err != nil {
		return Decrypted{}, err
	}
	pt, err := vault.Decrypt(envelope, pw)
	if err != nil {
		return Decrypted{}, mapVaultErr(err)
	}
	return Decrypted{Plaintext: pt, Identity: label}, nil
}

func extractEnvelope(data []byte, segs []pathSeg) ([]byte, error) {
	if segs == nil {
		if !vault.IsVaultFile(data) {
			return nil, ErrNotVault
		}
		return data, nil
	}
	doc, err := parseDoc(data, false)
	if err != nil {
		return nil, err
	}
	n, err := lookup(doc, segs, false)
	if err != nil {
		return nil, err
	}
	if n.Tag != "!vault" {
		return nil, ErrNotVault
	}
	return []byte(n.Value), nil
}

// WriteOptions are the Git settings shared by encrypt and rekey.
type WriteOptions struct {
	Branch  string // default DefaultBranch
	Message string
	Author  projects.Author
}

func (o WriteOptions) request(base, defMsg string, plan projects.PlanFunc) (projects.WriteRequest, error) {
	if o.Branch == "" {
		o.Branch = DefaultBranch
	}
	if !validBranch(o.Branch) {
		return projects.WriteRequest{}, invalid("write branch is not a valid branch name")
	}
	if strings.TrimSpace(o.Message) == "" {
		o.Message = defMsg
	}
	return projects.WriteRequest{Branch: o.Branch, BaseSHA: base, Message: o.Message, Author: o.Author, Plan: plan}, nil
}

// EncryptInput describes an encryption. Without KeyPath the whole file is
// encrypted; Plaintext nil then means "the current content of the file".
// With KeyPath the value at that YAML path becomes a `!vault` scalar.
type EncryptInput struct {
	Path      string
	KeyPath   string
	Plaintext *string
	Identity  string // vault-id label; empty means "default"
	WriteOptions
}

func vaultIDFor(label string) string {
	if label == "" || label == vaultids.DefaultLabel {
		return ""
	}
	return label
}

// Encrypt encrypts a file or a single value and commits the result to the
// write branch. A moved branch yields *projects.ConflictError.
func (s *Service) Encrypt(ctx context.Context, ws, project uuid.UUID, in EncryptInput) (projects.WriteResult, error) {
	p, err := cleanFile(in.Path)
	if err != nil {
		return projects.WriteResult{}, err
	}
	if in.Plaintext != nil && len(*in.Plaintext) > MaxPlaintext {
		return projects.WriteResult{}, ErrTooLarge
	}
	var segs []pathSeg
	if in.KeyPath != "" {
		if segs, err = parseKeyPath(in.KeyPath); err != nil {
			return projects.WriteResult{}, err
		}
		if in.Plaintext == nil {
			return projects.WriteResult{}, invalid("plaintext is required for an inline value")
		}
	}
	rev, err := s.prj.LatestRevision(ctx, ws, project)
	if err != nil {
		return projects.WriteResult{}, fmt.Errorf("latest revision: %w", err)
	}
	c := pwCache{s: s, ws: ws}
	defer c.zero()
	pw, err := c.exact(ctx, in.Identity)
	if err != nil {
		return projects.WriteResult{}, err
	}
	id := vaultIDFor(in.Identity)
	target := p
	if segs != nil {
		target += " (" + in.KeyPath + ")"
	}
	req, err := in.request(rev.SHA, "Encrypt "+target+" with ansible-vault", func(base *os.Root) (projects.Plan, error) {
		data, rerr := readFile(base, p)
		if rerr != nil && (!errors.Is(rerr, ErrFileNotFound) || segs != nil || in.Plaintext == nil) {
			return projects.Plan{}, rerr
		}
		var out []byte
		if segs == nil {
			plain := data
			if in.Plaintext != nil {
				plain = []byte(*in.Plaintext)
			}
			if vault.IsVaultFile(data) {
				return projects.Plan{}, invalid("%s is already vault encrypted; use rekey", p)
			}
			if len(plain) == 0 || len(plain) > MaxPlaintext {
				return projects.Plan{}, invalid("content must be 1 byte to %d MiB", MaxPlaintext>>20)
			}
			out, err = vault.Encrypt(plain, pw, id)
		} else {
			out, err = encryptInline(data, segs, *in.Plaintext, pw, id)
		}
		if err != nil {
			return projects.Plan{}, err
		}
		return projects.Plan{Changes: []projects.FileChange{{Path: p, Content: out}}}, nil
	})
	if err != nil {
		return projects.WriteResult{}, err
	}
	return s.prj.WriteBack(ctx, ws, project, req)
}

func encryptInline(data []byte, segs []pathSeg, value, pw, id string) ([]byte, error) {
	if value == "" {
		return nil, invalid("value must not be empty")
	}
	doc, err := parseDoc(data, true)
	if err != nil {
		return nil, err
	}
	n, err := lookup(doc, segs, true)
	if err != nil {
		return nil, err
	}
	env, err := vault.Encrypt([]byte(value), pw, id)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	if err := setVault(n, env); err != nil {
		return nil, err
	}
	return encodeDoc(doc)
}

// RekeyInput re-encrypts files from one identity to another. For a plain YAML
// file every inline `!vault` value in it is rekeyed. From empty means: choose
// per envelope by its header vault-id, falling back to "default".
type RekeyInput struct {
	Paths []string
	From  string
	To    string
	WriteOptions
}

// MaxRekeyPaths bounds the files of one rekey request.
const MaxRekeyPaths = 100

// Rekey re-encrypts vault files (and inline values) with another identity and
// commits the result to the write branch.
func (s *Service) Rekey(ctx context.Context, ws, project uuid.UUID, in RekeyInput) (projects.WriteResult, error) {
	if len(in.Paths) == 0 || len(in.Paths) > MaxRekeyPaths {
		return projects.WriteResult{}, invalid("1 to %d paths are required", MaxRekeyPaths)
	}
	if in.To == "" {
		return projects.WriteResult{}, invalid("target identity is required")
	}
	seen := map[string]bool{}
	paths := make([]string, 0, len(in.Paths))
	for _, raw := range in.Paths {
		p, err := cleanFile(raw)
		if err != nil {
			return projects.WriteResult{}, err
		}
		if seen[p] {
			return projects.WriteResult{}, invalid("path %q is listed twice", p)
		}
		seen[p] = true
		paths = append(paths, p)
	}
	rev, err := s.prj.LatestRevision(ctx, ws, project)
	if err != nil {
		return projects.WriteResult{}, fmt.Errorf("latest revision: %w", err)
	}
	c := pwCache{s: s, ws: ws}
	defer c.zero()
	toPW, err := c.exact(ctx, in.To)
	if err != nil {
		return projects.WriteResult{}, err
	}
	toID := vaultIDFor(in.To)
	fromPW := func(envelope []byte) (string, error) {
		if in.From != "" {
			return c.exact(ctx, in.From)
		}
		pw, _, err := c.forEnvelope(ctx, envelope)
		return pw, err
	}
	rekey := func(envelope []byte) ([]byte, error) {
		old, err := fromPW(envelope)
		if err != nil {
			return nil, err
		}
		out, err := vault.Rekey(envelope, old, toPW, toID)
		return out, mapVaultErr(err)
	}
	msg := "Rekey " + strings.Join(paths, ", ") + " to vault identity " + in.To
	if len(msg) > 200 {
		msg = fmt.Sprintf("Rekey %d vault files to vault identity %s", len(paths), in.To)
	}
	req, err := in.request(rev.SHA, msg, func(base *os.Root) (projects.Plan, error) {
		var plan projects.Plan
		for _, p := range paths {
			data, err := readFile(base, p)
			if err != nil {
				return projects.Plan{}, fmt.Errorf("%s: %w", p, err)
			}
			var out []byte
			if vault.IsVaultFile(data) {
				out, err = rekey(data)
			} else {
				out, err = rekeyInline(data, rekey)
			}
			if err != nil {
				return projects.Plan{}, fmt.Errorf("%s: %w", p, err)
			}
			plan.Changes = append(plan.Changes, projects.FileChange{Path: p, Content: out})
		}
		return plan, nil
	})
	if err != nil {
		return projects.WriteResult{}, err
	}
	return s.prj.WriteBack(ctx, ws, project, req)
}

func rekeyInline(data []byte, rekey func([]byte) ([]byte, error)) ([]byte, error) {
	doc, err := parseDoc(data, true)
	if err != nil {
		return nil, err
	}
	count := 0
	err = walkVault(doc, "", func(_ string, n *yaml.Node) error {
		env, err := rekey([]byte(n.Value))
		if err != nil {
			return err
		}
		count++
		return setVault(n, env)
	})
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrNotVault
	}
	return encodeDoc(doc)
}
