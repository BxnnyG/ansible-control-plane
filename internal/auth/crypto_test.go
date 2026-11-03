package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPasswordHashVerify(t *testing.T) {
	ctx := context.Background()
	h, err := HashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pw   string
		want bool
	}{{"correct horse battery", true}, {"correct horse batterz", false}, {"", false}} {
		got, err := VerifyPassword(ctx, tc.pw, h)
		if err != nil || got != tc.want {
			t.Errorf("verify(%q) = %v, %v; want %v", tc.pw, got, err, tc.want)
		}
	}
	if _, err := HashPassword(ctx, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password: %v", err)
	}
	h2, _ := HashPassword(ctx, "correct horse battery")
	if h == h2 {
		t.Error("salt must differ")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=65536,t=3,p=2$AAAA",
		"$argon2id$v=19$m=99999999,t=3,p=2$AAAAAAAAAAAA$AAAAAAAAAAAA",
		"$argon2id$v=19$m=1024,t=0,p=2$AAAAAAAAAAAA$AAAAAAAAAAAA",
		"$bcrypt$v=19$m=1024,t=1,p=1$AAAAAAAAAAAA$AAAAAAAAAAAA"} {
		if ok, err := VerifyPassword(context.Background(), "x", h); ok || err == nil {
			t.Errorf("hash %q accepted (%v, %v)", h, ok, err)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	a, err := GeneratePassword(24)
	b, _ := GeneratePassword(24)
	if err != nil || len(a) != 24 || a == b {
		t.Fatalf("generate: %q %q %v", a, b, err)
	}
}

// RFC 6238 appendix B vector (SHA1, secret "12345678901234567890"): T=59 ->
// 94287082, whose last six digits are the 6-digit code.
func TestTOTPRFCVector(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := TOTPCode(secret, 59/30); got != "287082" {
		t.Errorf("code at t=59: %s", got)
	}
	if got := TOTPCode(secret, 1111111109/30); got != "081804" {
		t.Errorf("code at t=1111111109: %s", got)
	}
}

func TestVerifyTOTPSkew(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111109, 0)
	cur := TOTPStep(now)
	for _, tc := range []struct {
		name string
		code string
		step int64
		ok   bool
	}{
		{"current", TOTPCode(secret, cur), cur, true},
		{"previous", TOTPCode(secret, cur-1), cur - 1, true},
		{"next", TOTPCode(secret, cur+1), cur + 1, true},
		{"too old", TOTPCode(secret, cur-2), 0, false},
		{"too new", TOTPCode(secret, cur+2), 0, false},
		{"with space", TOTPCode(secret, cur)[:3] + " " + TOTPCode(secret, cur)[3:], cur, true},
		{"short", "123", 0, false},
	} {
		step, ok := VerifyTOTP(secret, tc.code, now)
		if ok != tc.ok || (ok && step != tc.step) {
			t.Errorf("%s: got %d,%v want %d,%v", tc.name, step, ok, tc.step, tc.ok)
		}
	}
}

func TestTOTPURI(t *testing.T) {
	u := TOTPURI("admin", []byte("12345678901234567890"))
	want := "otpauth://totp/ACP:admin?"
	if len(u) < len(want) || u[:len(want)] != want {
		t.Errorf("uri %q", u)
	}
}
