// Package secrets implements envelope encryption (ADR-0005): every secret has
// its own random data key (AES-256-GCM); data keys are wrapped by a KEK derived
// via HKDF-SHA256 from a 256-bit master key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// EnvelopeVersion is the current envelope format version.
	EnvelopeVersion = 1
	keySize         = 32
	nonceSize       = 12
)

var (
	// ErrInvalidKey is returned for malformed or weak master keys.
	ErrInvalidKey = errors.New("invalid master key")
	// ErrDecrypt is returned when authentication fails (tamper, wrong key or AAD).
	ErrDecrypt = errors.New("decryption failed")
	// ErrUnknownKey is returned when the envelope's key ID is not known to the provider.
	ErrUnknownKey = errors.New("unknown key id")
	// ErrBadEnvelope is returned for structurally invalid envelopes.
	ErrBadEnvelope = errors.New("malformed envelope")
)

// AAD is the associated data bound into both GCM tags.
type AAD struct {
	SecretID    string
	WorkspaceID string
}

func (a AAD) bytes() []byte {
	return []byte("acp-secret/v1\x00" + a.SecretID + "\x00" + a.WorkspaceID)
}

// Envelope is the sealed form of a secret, suitable for storage (bytea columns).
type Envelope struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	WrapNonce  []byte `json:"wrap_nonce"`
	WrappedDEK []byte `json:"wrapped_dek"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

func (e *Envelope) validate() error {
	if e == nil || e.Version != EnvelopeVersion || len(e.WrapNonce) != nonceSize ||
		len(e.Nonce) != nonceSize || len(e.WrappedDEK) != keySize+16 || len(e.Ciphertext) < 16 {
		return ErrBadEnvelope
	}
	return nil
}

// Provider wraps and unwraps data keys. Implementations: local (master key),
// later OpenBao/Vault transit.
type Provider interface {
	// KeyID identifies the active key used for new wraps.
	KeyID() string
	// Wrap encrypts a data key bound to aad.
	Wrap(dek []byte, aad AAD) (keyID string, nonce, wrapped []byte, err error)
	// Unwrap decrypts a data key using the key named by keyID.
	Unwrap(keyID string, nonce, wrapped []byte, aad AAD) ([]byte, error)
	// Rotate re-wraps an envelope under the active key without touching the payload.
	Rotate(env *Envelope, aad AAD) (*Envelope, error)
}

// LocalProvider derives KEKs from in-memory master keys.
type LocalProvider struct {
	active string
	keys   map[string][]byte // key ID -> master key
}

var _ Provider = (*LocalProvider)(nil)

// KeyIDFor returns the stable ID of a master key (hex of first 8 bytes of SHA-256).
func KeyIDFor(master []byte) string {
	h := sha256.Sum256(append([]byte("acp-key-id\x00"), master...))
	return hex.EncodeToString(h[:8])
}

// NewLocalProvider creates a provider with an active master key and optional
// retired keys (kept for decrypting/rotating old envelopes).
func NewLocalProvider(active []byte, retired ...[]byte) (*LocalProvider, error) {
	p := &LocalProvider{keys: map[string][]byte{}}
	for i, k := range append([][]byte{active}, retired...) {
		if err := validateMaster(k); err != nil {
			return nil, fmt.Errorf("master key #%d: %w", i, err)
		}
		id := KeyIDFor(k)
		p.keys[id] = append([]byte(nil), k...)
		if i == 0 {
			p.active = id
		}
	}
	return p, nil
}

func validateMaster(k []byte) error {
	if len(k) != keySize {
		return fmt.Errorf("%w: want %d bytes, got %d", ErrInvalidKey, keySize, len(k))
	}
	distinct := map[byte]struct{}{}
	for _, b := range k {
		distinct[b] = struct{}{}
	}
	if len(distinct) < 8 {
		return fmt.Errorf("%w: key has too little entropy", ErrInvalidKey)
	}
	return nil
}

// ParseMasterKey decodes a master key as written by `make init` (base64) or
// as 64 hex characters. Surrounding whitespace is ignored.
func ParseMasterKey(data []byte) ([]byte, error) {
	s := strings.TrimSpace(string(data))
	var k []byte
	if len(s) == 2*keySize {
		if b, err := hex.DecodeString(s); err == nil {
			k = b
		}
	}
	if k == nil {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("%w: not valid base64 or hex", ErrInvalidKey)
		}
		k = b
	}
	if err := validateMaster(k); err != nil {
		zero(k)
		return nil, err
	}
	return k, nil
}

// LoadMasterKey reads a master key from path, or from the environment variable
// named envVar when path is empty.
func LoadMasterKey(path, envVar string) ([]byte, error) {
	var raw []byte
	switch {
	case path != "":
		b, err := os.ReadFile(path) // #nosec G304 -- operator-configured path
		if err != nil {
			return nil, fmt.Errorf("read master key file: %w", err)
		}
		raw = b
	case envVar != "" && os.Getenv(envVar) != "":
		raw = []byte(os.Getenv(envVar))
	default:
		return nil, fmt.Errorf("%w: neither key file nor environment variable set", ErrInvalidKey)
	}
	defer zero(raw)
	return ParseMasterKey(raw)
}

// NewLocalProviderFromFile loads the master key via LoadMasterKey and builds a provider.
func NewLocalProviderFromFile(path, envVar string) (*LocalProvider, error) {
	k, err := LoadMasterKey(path, envVar)
	if err != nil {
		return nil, err
	}
	defer zero(k)
	return NewLocalProvider(k)
}

// KeyID returns the active key ID.
func (p *LocalProvider) KeyID() string { return p.active }

func (p *LocalProvider) kek(keyID string) (cipher.AEAD, error) {
	master, ok := p.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownKey, keyID)
	}
	kek, err := hkdf.Key(sha256.New, master, nil, "acp-kek-v1", keySize)
	if err != nil {
		return nil, fmt.Errorf("derive kek: %w", err)
	}
	defer zero(kek)
	return newGCM(kek)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return g, nil
}

// Wrap implements Provider.
func (p *LocalProvider) Wrap(dek []byte, aad AAD) (string, []byte, []byte, error) {
	g, err := p.kek(p.active)
	if err != nil {
		return "", nil, nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	return p.active, nonce, g.Seal(nil, nonce, dek, aad.bytes()), nil
}

// Unwrap implements Provider.
func (p *LocalProvider) Unwrap(keyID string, nonce, wrapped []byte, aad AAD) ([]byte, error) {
	g, err := p.kek(keyID)
	if err != nil {
		return nil, err
	}
	if len(nonce) != nonceSize {
		return nil, ErrBadEnvelope
	}
	dek, err := g.Open(nil, nonce, wrapped, aad.bytes())
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", ErrDecrypt)
	}
	return dek, nil
}

// Rotate implements Provider.
func (p *LocalProvider) Rotate(env *Envelope, aad AAD) (*Envelope, error) {
	if err := env.validate(); err != nil {
		return nil, err
	}
	dek, err := p.Unwrap(env.KeyID, env.WrapNonce, env.WrappedDEK, aad)
	if err != nil {
		return nil, fmt.Errorf("rotate: %w", err)
	}
	defer zero(dek)
	id, nonce, wrapped, err := p.Wrap(dek, aad)
	if err != nil {
		return nil, fmt.Errorf("rotate: %w", err)
	}
	out := *env
	out.KeyID, out.WrapNonce, out.WrappedDEK = id, nonce, wrapped
	out.Ciphertext = append([]byte(nil), env.Ciphertext...)
	out.Nonce = append([]byte(nil), env.Nonce...)
	return &out, nil
}

// Seal encrypts plaintext under a fresh data key wrapped by p.
func Seal(p Provider, plaintext []byte, aad AAD) (*Envelope, error) {
	dek := make([]byte, keySize)
	defer zero(dek)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	g, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	id, wn, wrapped, err := p.Wrap(dek, aad)
	if err != nil {
		return nil, fmt.Errorf("wrap data key: %w", err)
	}
	return &Envelope{
		Version:    EnvelopeVersion,
		KeyID:      id,
		WrapNonce:  wn,
		WrappedDEK: wrapped,
		Nonce:      nonce,
		Ciphertext: g.Seal(nil, nonce, plaintext, aad.bytes()),
	}, nil
}

// Open decrypts an envelope. The returned Value should be zeroed after use.
func Open(p Provider, env *Envelope, aad AAD) (Value, error) {
	if err := env.validate(); err != nil {
		return Value{}, err
	}
	dek, err := p.Unwrap(env.KeyID, env.WrapNonce, env.WrappedDEK, aad)
	if err != nil {
		return Value{}, err
	}
	defer zero(dek)
	g, err := newGCM(dek)
	if err != nil {
		return Value{}, err
	}
	pt, err := g.Open(nil, env.Nonce, env.Ciphertext, aad.bytes())
	if err != nil {
		return Value{}, fmt.Errorf("open payload: %w", ErrDecrypt)
	}
	v := Value{b: pt}
	return v, nil
}
