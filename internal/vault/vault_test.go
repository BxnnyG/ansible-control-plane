package vault

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake credentials used only for the committed ansible-vault fixtures.
const (
	fixturePassword = "test-password"
	fixturePlain    = "fake-secret-value\n"
)

func TestRoundtrip(t *testing.T) {
	tests := []struct {
		name, plain, id, wantVer string
	}{
		{"empty", "", "", "1.1"},
		{"short", "a", "", "1.1"},
		{"block aligned", strings.Repeat("b", 16), "", "1.1"},
		{"with id", "hello: world\n", "prod", "1.2"},
		{"long with id", strings.Repeat("x", 1000), "dev", "1.2"},
		{"binary-ish", "\x00\xff\n\r\x01", "", "1.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Encrypt([]byte(tt.plain), "pw-1", tt.id)
			if err != nil {
				t.Fatal(err)
			}
			if !IsVaultFile(env) {
				t.Fatal("IsVaultFile false")
			}
			h, err := ParseHeader(env)
			if err != nil || h.Version != tt.wantVer || h.VaultID != tt.id || h.Cipher != "AES256" {
				t.Fatalf("header = %+v, err %v", h, err)
			}
			for _, l := range strings.Split(strings.TrimSpace(string(env)), "\n")[1:] {
				if len(l) > 80 {
					t.Fatalf("line too long: %d", len(l))
				}
			}
			got, err := Decrypt(env, "pw-1")
			if err != nil || string(got) != tt.plain {
				t.Fatalf("decrypt mismatch, err %v", err)
			}
		})
	}
}

func TestWrongPasswordAndTamper(t *testing.T) {
	env, err := Encrypt([]byte("secret"), "right", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(env, "wrong"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong password err = %v", err)
	}
	if _, err := Decrypt(env, ""); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("empty password err = %v", err)
	}
	// Tamper: decode the outer hex layer, flip the last hex digit of the
	// ciphertext field (still valid hex), and re-encode, so only the HMAC fails.
	_, body, _ := splitEnvelope(env)
	raw, err := hex.DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil {
		t.Fatal(err)
	}
	if raw[len(raw)-1] == '0' {
		raw[len(raw)-1] = '1'
	} else {
		raw[len(raw)-1] = '0'
	}
	b := []byte(hex.EncodeToString(raw))
	tampered := []byte("$ANSIBLE_VAULT;1.1;AES256\n" + string(b) + "\n")
	if _, err := Decrypt(tampered, "right"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered err = %v", err)
	}
}

func TestFormatErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no header":   "hello",
		"bad version": "$ANSIBLE_VAULT;2.0;AES256\nabcd\n",
		"bad cipher":  "$ANSIBLE_VAULT;1.1;DES\nabcd\n",
		"1.2 no id":   "$ANSIBLE_VAULT;1.2;AES256\nabcd\n",
		"bad hex":     "$ANSIBLE_VAULT;1.1;AES256\nzzzz\n",
		"bad body":    "$ANSIBLE_VAULT;1.1;AES256\n6162\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decrypt([]byte(in), "pw"); !errors.Is(err, ErrFormat) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if IsVaultFile([]byte("plain text")) {
		t.Fatal("false positive")
	}
}

func TestRekey(t *testing.T) {
	env, _ := Encrypt([]byte("data"), "old", "")
	re, err := Rekey(env, "old", "new", "ops")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := ParseHeader(re)
	if h.VaultID != "ops" {
		t.Fatalf("vault id = %q", h.VaultID)
	}
	if _, err := Decrypt(re, "old"); err == nil {
		t.Fatal("old password still works")
	}
	if got, err := Decrypt(re, "new"); err != nil || string(got) != "data" {
		t.Fatalf("rekey roundtrip failed: %v", err)
	}
	if _, err := Rekey(env, "bad", "new", ""); err == nil {
		t.Fatal("rekey with wrong password succeeded")
	}
}

func TestDecryptWithPasswords(t *testing.T) {
	e1, _ := Encrypt([]byte("one"), "pw-a", "a")
	e2, _ := Encrypt([]byte("two"), "pw-def", "")
	pws := map[string]string{"a": "pw-a", "": "pw-def"}
	if got, err := DecryptWithPasswords(e1, pws); err != nil || string(got) != "one" {
		t.Fatalf("e1: %v", err)
	}
	if got, err := DecryptWithPasswords(e2, pws); err != nil || string(got) != "two" {
		t.Fatalf("e2: %v", err)
	}
	if _, err := DecryptWithPasswords(e1, map[string]string{"b": "x"}); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v", err)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // fixed test fixture names
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Fixtures were produced by ansible-vault from ansible-core 2.21.5.
func TestInteropAnsibleFixtures(t *testing.T) {
	tests := []struct{ file, id string }{{"v11.vault", ""}, {"v12.vault", "testid"}}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			env := readFixture(t, tt.file)
			h, err := ParseHeader(env)
			if err != nil || h.VaultID != tt.id {
				t.Fatalf("header %+v, %v", h, err)
			}
			got, err := Decrypt(env, fixturePassword)
			if err != nil || string(got) != fixturePlain {
				t.Fatalf("decrypt fixture: %v", err)
			}
		})
	}
}

func TestInlineFixture(t *testing.T) {
	found, err := FindInlineVault(readFixture(t, "inline.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path != "inline_secret" {
		t.Fatalf("found = %+v", found)
	}
	got, err := Decrypt(found[0].Envelope, fixturePassword)
	if err != nil || string(got) != "fake-inline-value" {
		t.Fatalf("decrypt inline: %v", err)
	}
}

func TestFindInlineVaultPaths(t *testing.T) {
	doc := "a:\n  b: !vault |\n    $ANSIBLE_VAULT;1.1;AES256\n    00\nc:\n  - x\n  - !vault |\n    $ANSIBLE_VAULT;1.1;AES256\n    00\nd: plain\n---\ne: !vault |\n    $ANSIBLE_VAULT;1.1;AES256\n    00\n"
	found, err := FindInlineVault([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range found {
		paths = append(paths, f.Path)
	}
	if got := strings.Join(paths, ","); got != "a.b,c[1],e" {
		t.Fatalf("paths = %s", got)
	}
	if _, err := FindInlineVault([]byte("a: [unclosed")); err == nil {
		t.Fatal("expected yaml error")
	}
}
