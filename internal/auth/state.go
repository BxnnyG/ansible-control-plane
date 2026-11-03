package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// flowState is the short-lived login state carried in a sealed cookie
// between /login and /callback. It never leaves the browser unencrypted.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

type sealer struct{ aead cipher.AEAD }

// newSealer derives an AES-256-GCM key from key material of any length >= 16.
func newSealer(key []byte) (*sealer, error) {
	if len(key) < 16 {
		return nil, errors.New("state key too short")
	}
	k := sha256.Sum256(append([]byte("acp-oidc-state-v1\x00"), key...))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, fmt.Errorf("state cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("state gcm: %w", err)
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(st flowState) (string, error) {
	plain, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("marshal state: %w", err)
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("state nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte("flow"))), nil
}

func (s *sealer) open(v string, now time.Time) (flowState, error) {
	var st flowState
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(raw) <= s.aead.NonceSize() {
		return st, errors.New("malformed state cookie")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte("flow"))
	if err != nil {
		return st, errors.New("invalid state cookie")
	}
	if err := json.Unmarshal(plain, &st); err != nil {
		return st, errors.New("invalid state payload")
	}
	if now.Unix() > st.Expires {
		return st, errors.New("state expired")
	}
	return st, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

// safeReturnTo accepts only same-site absolute paths; anything else becomes "/".
func safeReturnTo(p string) string {
	if len(p) == 0 || p[0] != '/' || (len(p) > 1 && (p[1] == '/' || p[1] == '\\')) {
		return "/"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f || c == '\\' {
			return "/"
		}
	}
	return p
}
