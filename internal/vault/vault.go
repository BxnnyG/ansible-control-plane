// Package vault implements the Ansible Vault file format (versions 1.1 and
// 1.2, cipher AES256) in pure Go. See ADR-0015.
//
// Passwords and plaintext are never logged and never included in errors.
package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	headerMagic = "$ANSIBLE_VAULT"
	cipherName  = "AES256"
	saltLen     = 32
	iterations  = 10000
	wrapWidth   = 80
)

var (
	// ErrFormat reports a malformed vault envelope.
	ErrFormat = errors.New("vault: invalid format")
	// ErrDecrypt reports a wrong password or tampered data. The two cases are
	// deliberately indistinguishable.
	ErrDecrypt = errors.New("vault: decryption failed (wrong password or corrupted data)")
	// ErrNoPassword reports that no password was supplied for the vault-id.
	ErrNoPassword = errors.New("vault: no password for vault-id")
)

// Header is the parsed first line of a vault envelope.
type Header struct {
	Version string // "1.1" or "1.2"
	Cipher  string // always "AES256"
	VaultID string // empty for version 1.1
}

// IsVaultFile reports whether data starts with an Ansible Vault header.
func IsVaultFile(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte(headerMagic+";"))
}

// ParseHeader parses the header line of an envelope.
func ParseHeader(envelope []byte) (Header, error) {
	h, _, err := splitEnvelope(envelope)
	return h, err
}

func splitEnvelope(envelope []byte) (Header, string, error) {
	s := strings.TrimLeft(string(envelope), " \t\r\n")
	line, body, _ := strings.Cut(s, "\n")
	parts := strings.Split(strings.TrimSpace(line), ";")
	if len(parts) < 3 || parts[0] != headerMagic {
		return Header{}, "", fmt.Errorf("%w: missing header", ErrFormat)
	}
	h := Header{Version: parts[1], Cipher: parts[2]}
	switch h.Version {
	case "1.1":
		if len(parts) != 3 {
			return Header{}, "", fmt.Errorf("%w: unexpected header fields", ErrFormat)
		}
	case "1.2":
		if len(parts) != 4 || parts[3] == "" {
			return Header{}, "", fmt.Errorf("%w: version 1.2 requires a vault-id", ErrFormat)
		}
		h.VaultID = parts[3]
	default:
		return Header{}, "", fmt.Errorf("%w: unsupported version %q", ErrFormat, h.Version)
	}
	if h.Cipher != cipherName {
		return Header{}, "", fmt.Errorf("%w: unsupported cipher %q", ErrFormat, h.Cipher)
	}
	return h, body, nil
}

func deriveKeys(password string, salt []byte) (aesKey, hmacKey, iv []byte, err error) {
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, 80)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vault: derive key: %w", err)
	}
	return dk[:32], dk[32:64], dk[64:80], nil
}

// Encrypt returns a vault envelope. With an empty vaultID the 1.1 format is
// produced, otherwise 1.2.
func Encrypt(plaintext []byte, password, vaultID string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("%w: empty password", ErrNoPassword)
	}
	if strings.ContainsAny(vaultID, ";\r\n") {
		return nil, fmt.Errorf("%w: invalid vault-id", ErrFormat)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("vault: read random salt: %w", err)
	}
	aesKey, hmacKey, iv, err := deriveKeys(password, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("vault: init cipher: %w", err)
	}
	padLen := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	ct := make([]byte, len(padded))
	cipher.NewCTR(block, iv).XORKeyStream(ct, padded)

	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(ct)

	inner := hex.EncodeToString(salt) + "\n" + hex.EncodeToString(mac.Sum(nil)) + "\n" + hex.EncodeToString(ct)
	body := hex.EncodeToString([]byte(inner))

	header := headerMagic + ";1.1;" + cipherName
	if vaultID != "" {
		header = headerMagic + ";1.2;" + cipherName + ";" + vaultID
	}
	var out strings.Builder
	out.WriteString(header + "\n")
	for len(body) > wrapWidth {
		out.WriteString(body[:wrapWidth] + "\n")
		body = body[wrapWidth:]
	}
	out.WriteString(body + "\n")
	return []byte(out.String()), nil
}

// Decrypt decrypts an envelope with a single password.
func Decrypt(envelope []byte, password string) ([]byte, error) {
	_, body, err := splitEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	return decryptBody(body, password)
}

// DecryptWithPasswords selects the password by the envelope's vault-id.
// Envelopes without a vault-id (1.1) and envelopes whose id is not in the map
// fall back to the entry with the empty key "".
func DecryptWithPasswords(envelope []byte, passwords map[string]string) ([]byte, error) {
	h, body, err := splitEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	pw := passwords[h.VaultID]
	if pw == "" {
		pw = passwords[""]
	}
	if pw == "" {
		return nil, ErrNoPassword
	}
	return decryptBody(body, pw)
}

func decryptBody(body, password string) ([]byte, error) {
	if password == "" {
		return nil, ErrNoPassword
	}
	innerRaw, err := hex.DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil {
		return nil, fmt.Errorf("%w: body is not hex", ErrFormat)
	}
	fields := strings.Split(string(innerRaw), "\n")
	if len(fields) != 3 {
		return nil, fmt.Errorf("%w: body must have salt, hmac and data", ErrFormat)
	}
	salt, err1 := hex.DecodeString(fields[0])
	wantMAC, err2 := hex.DecodeString(fields[1])
	ct, err3 := hex.DecodeString(fields[2])
	if err1 != nil || err2 != nil || err3 != nil || len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("%w: malformed body fields", ErrFormat)
	}
	aesKey, hmacKey, iv, err := deriveKeys(password, salt)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(ct)
	if subtle.ConstantTimeCompare(mac.Sum(nil), wantMAC) != 1 {
		return nil, ErrDecrypt
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, fmt.Errorf("vault: init cipher: %w", err)
	}
	pt := make([]byte, len(ct))
	cipher.NewCTR(block, iv).XORKeyStream(pt, ct)
	padLen := int(pt[len(pt)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(pt) {
		return nil, ErrDecrypt
	}
	return pt[:len(pt)-padLen], nil
}

// Rekey decrypts with oldPassword and re-encrypts with newPassword and
// newVaultID (empty for 1.1).
func Rekey(envelope []byte, oldPassword, newPassword, newVaultID string) ([]byte, error) {
	pt, err := Decrypt(envelope, oldPassword)
	if err != nil {
		return nil, err
	}
	return Encrypt(pt, newPassword, newVaultID)
}
