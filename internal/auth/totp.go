package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 default; interoperable with all authenticator apps
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// TOTP parameters (RFC 6238 defaults, understood by every authenticator app).
const (
	totpPeriod     = 30 // seconds
	totpDigits     = 6
	totpSecretLen  = 20 // 160 bit, the RFC 4226 recommendation
	totpSkewSteps  = 1  // accept the previous and next step for clock drift
	totpIssuerName = "ACP"
)

// nilWorkspaceID is the workspace part of the AAD for global (non-workspace)
// secrets such as user TOTP secrets.
const nilWorkspaceID = "00000000-0000-0000-0000-000000000000"

// TOTPAAD binds a sealed TOTP secret to its user: SecretID is the user ID,
// WorkspaceID the nil UUID. A sealed TOTP secret therefore cannot be swapped
// between users or replayed as a workspace secret (workspace IDs are never nil).
func TOTPAAD(userID string) secrets.AAD {
	return secrets.AAD{SecretID: userID, WorkspaceID: nilWorkspaceID}
}

// GenerateTOTPSecret returns a fresh random secret.
func GenerateTOTPSecret() ([]byte, error) {
	b := make([]byte, totpSecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}
	return b, nil
}

// EncodeTOTPSecret renders a secret as unpadded base32 for enrollment.
func EncodeTOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}

// TOTPURI builds the otpauth:// enrollment URI.
func TOTPURI(account string, secret []byte) string {
	v := url.Values{
		"secret": {EncodeTOTPSecret(secret)}, "issuer": {totpIssuerName},
		"algorithm": {"SHA1"}, "digits": {fmt.Sprint(totpDigits)}, "period": {fmt.Sprint(totpPeriod)},
	}
	return "otpauth://totp/" + url.PathEscape(totpIssuerName+":"+account) + "?" + v.Encode()
}

// TOTPStep returns the RFC 6238 time step for t.
func TOTPStep(t time.Time) int64 { return t.Unix() / totpPeriod }

// TOTPCode computes the code for a step (RFC 4226 dynamic truncation).
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step)) //nolint:gosec // step is positive
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, bin%1_000_000)
}

// VerifyTOTP checks code against the steps around now and returns the matching
// step. All candidate steps are compared in constant time. The caller must
// reject steps <= the last accepted step to prevent replay.
func VerifyTOTP(secret []byte, code string, now time.Time) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	for d := int64(-totpSkewSteps); d <= totpSkewSteps; d++ {
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, cur+d)), []byte(code)) == 1 {
			step, ok = cur+d, true
		}
	}
	return step, ok
}

// SealTOTPSecret encrypts a secret for storage in users.totp_envelope.
func SealTOTPSecret(p secrets.Provider, userID string, secret []byte) ([]byte, error) {
	env, err := secrets.Seal(p, secret, TOTPAAD(userID))
	if err != nil {
		return nil, fmt.Errorf("seal totp secret: %w", err)
	}
	return json.Marshal(env)
}

// OpenTOTPSecret decrypts users.totp_envelope. The caller must zero the result.
func OpenTOTPSecret(p secrets.Provider, userID string, envelope []byte) (secrets.Value, error) {
	var env secrets.Envelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		return secrets.Value{}, fmt.Errorf("decode totp envelope: %w", err)
	}
	v, err := secrets.Open(p, &env, TOTPAAD(userID))
	if err != nil {
		return secrets.Value{}, fmt.Errorf("open totp secret: %w", err)
	}
	return v, nil
}
