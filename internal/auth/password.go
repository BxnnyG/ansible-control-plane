package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for the break-glass password (ADR-0006). 64 MiB x 3
// passes x 2 lanes costs roughly 100-200 ms and fits the small (4 GiB) host
// even with a few concurrent logins; see maxConcurrentHashes.
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonSaltLen   = 16
	argonKeyLen    = 32

	// Upper bounds accepted when parsing a stored hash, so a tampered row
	// cannot make the server allocate unbounded memory.
	maxArgonMemoryKiB = 256 * 1024
	maxArgonTime      = 10
	maxArgonThreads   = 16

	// MinPasswordLen is enforced when a break-glass password is set.
	MinPasswordLen = 12
	maxPasswordLen = 1024
)

// hashSlots bounds concurrent Argon2 computations so a login flood cannot
// exhaust memory (each takes argonMemoryKiB).
const maxConcurrentHashes = 2

var (
	// ErrWeakPassword is returned for passwords shorter than MinPasswordLen.
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	errBadHash      = errors.New("malformed password hash")
)

var hashSlots = make(chan struct{}, maxConcurrentHashes)

func acquireHashSlot(ctx context.Context) (release func(), err error) {
	select {
	case hashSlots <- struct{}{}:
		return func() { <-hashSlots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for hash slot: %w", ctx.Err())
	}
}

// HashPassword returns a PHC-encoded argon2id hash with a random salt.
func HashPassword(ctx context.Context, password string) (string, error) {
	if len(password) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	if len(password) > maxPasswordLen {
		return "", errors.New("password too long")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword compares password against a PHC-encoded argon2id hash in
// constant time. It returns false for any malformed hash.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	if len(password) > maxPasswordLen {
		password = password[:maxPasswordLen]
	}
	var mem, tm uint32
	var thr uint8
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return false, errBadHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &tm, &thr); err != nil {
		return false, errBadHash
	}
	if mem == 0 || mem > maxArgonMemoryKiB || tm == 0 || tm > maxArgonTime || thr == 0 || thr > maxArgonThreads {
		return false, errBadHash
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(want) == 0 || len(want) > 64 {
		return false, errBadHash
	}
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	got := argon2.IDKey([]byte(password), salt, tm, mem, thr, uint32(len(want))) //nolint:gosec // len bounded above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789" //gitleaks:allow (character set, not a secret)

// GeneratePassword returns a random password of n characters (n >= 16 advised)
// drawn uniformly from an unambiguous alphabet.
func GeneratePassword(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	limit := byte(256 - 256%len(passwordAlphabet)) // rejection sampling avoids modulo bias
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, passwordAlphabet[int(b)%len(passwordAlphabet)])
			}
		}
	}
	return string(out), nil
}
