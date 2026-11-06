package semaphore

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Credential is a decrypted access key. Sensitive parts are Secrets; only
// Type, Login and Fingerprint may be printed.
type Credential struct {
	Type        string
	Login       string
	Password    Secret // login_password
	PrivateKey  Secret // ssh, always unencrypted PEM
	Fingerprint string // SHA256 fingerprint of the ssh public key
	// Unprotected is true when a passphrase had to be removed from the key.
	Unprotected bool
}

// Decrypt decodes the secret column of a key. encKey is the base64 value of
// access_key_encryption from config.json; empty means unencrypted storage.
// Keys of type "none" yield (nil, nil).
func Decrypt(k Key, encKey Secret) (*Credential, error) {
	if k.Type == "none" || k.Type == "" {
		return nil, nil
	}
	plain, err := decodeSecretColumn(k.Secret, encKey)
	if err != nil {
		return nil, fmt.Errorf("key %q: %w", k.Name, err)
	}
	defer wipe(plain)
	switch k.Type {
	case "ssh":
		return parseSSH(k.Name, plain)
	case "login_password":
		var p struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(plain, &p); err != nil {
			return nil, fmt.Errorf("key %q: malformed login_password payload", k.Name)
		}
		return &Credential{Type: k.Type, Login: p.Login, Password: NewSecret([]byte(p.Password))}, nil
	default:
		return nil, fmt.Errorf("key %q: unsupported type %q", k.Name, k.Type)
	}
}

// decodeSecretColumn returns the JSON payload. Layouts tried, in order, with
// the first one that yields valid JSON winning:
//   - no key: raw JSON, or base64(JSON);
//   - key: base64(nonce || AES-256-GCM ciphertext), then base64(iv || AES-CFB)
//     (older Semaphore releases).
func decodeSecretColumn(col string, encKey Secret) ([]byte, error) {
	col = strings.TrimSpace(col)
	if col == "" {
		return nil, errors.New("empty secret")
	}
	if encKey.Empty() {
		if json.Valid([]byte(col)) {
			return []byte(col), nil
		}
		raw, err := base64.StdEncoding.DecodeString(col)
		if err != nil || !json.Valid(raw) {
			return nil, errors.New("secret is not plain JSON; is access_key_encryption missing?")
		}
		return raw, nil
	}
	raw, err := base64.StdEncoding.DecodeString(col)
	if err != nil {
		return nil, errors.New("secret column is not base64")
	}
	kb, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encKey.Reveal())))
	if err != nil {
		return nil, errors.New("access_key_encryption is not base64")
	}
	block, err := aes.NewCipher(kb)
	if err != nil {
		return nil, errors.New("access_key_encryption has an invalid length")
	}
	if gcm, err := cipher.NewGCM(block); err == nil && len(raw) > gcm.NonceSize() {
		if out, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil); err == nil && json.Valid(out) {
			return out, nil
		}
	}
	if len(raw) > aes.BlockSize {
		out := make([]byte, len(raw)-aes.BlockSize)
		cipher.NewCFBDecrypter(block, raw[:aes.BlockSize]).XORKeyStream(out, raw[aes.BlockSize:]) //nolint:staticcheck // legacy Semaphore layout
		if json.Valid(out) {
			return out, nil
		}
		wipe(out)
	}
	return nil, errors.New("decryption failed (wrong access_key_encryption?)")
}

func parseSSH(name string, plain []byte) (*Credential, error) {
	var p struct {
		Login      string `json:"login"`
		Passphrase string `json:"passphrase"`
		PrivateKey string `json:"private_key"`
	}
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, fmt.Errorf("key %q: malformed ssh payload", name)
	}
	if strings.TrimSpace(p.PrivateKey) == "" {
		return nil, fmt.Errorf("key %q: ssh key has no private key", name)
	}
	pemBytes := []byte(p.PrivateKey)
	var raw any
	var err error
	unprotect := false
	if p.Passphrase != "" {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase(pemBytes, []byte(p.Passphrase))
		if err != nil {
			// The passphrase may be stale while the key itself is not encrypted.
			var rawPlain any
			if rawPlain, err = ssh.ParseRawPrivateKey(pemBytes); err != nil {
				return nil, fmt.Errorf("key %q: cannot parse private key with stored passphrase", name)
			}
			raw = rawPlain
		} else {
			unprotect = true
		}
	} else {
		raw, err = ssh.ParseRawPrivateKey(pemBytes)
		if err != nil {
			var pm *ssh.PassphraseMissingError
			if errors.As(err, &pm) {
				return nil, fmt.Errorf("key %q: private key is passphrase-protected but no passphrase is stored", name)
			}
			return nil, fmt.Errorf("key %q: cannot parse private key", name)
		}
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, fmt.Errorf("key %q: unsupported private key type", name)
	}
	out := pemBytes
	if unprotect {
		blk, err := ssh.MarshalPrivateKey(raw, "")
		if err != nil {
			return nil, fmt.Errorf("key %q: cannot re-encode private key", name)
		}
		out = pem.EncodeToMemory(blk)
	}
	return &Credential{
		Type:        "ssh",
		Login:       p.Login,
		PrivateKey:  NewSecret(out),
		Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		Unprotected: unprotect,
	}, nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
