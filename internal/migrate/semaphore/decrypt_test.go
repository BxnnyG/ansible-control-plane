package semaphore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKeyB64(t *testing.T) (Secret, cipher.Block) {
	t.Helper()
	kb := make([]byte, 32)
	if _, err := rand.Read(kb); err != nil {
		t.Fatal(err)
	}
	blk, err := aes.NewCipher(kb)
	if err != nil {
		t.Fatal(err)
	}
	return NewSecret([]byte(base64.StdEncoding.EncodeToString(kb))), blk
}

func encGCM(t *testing.T, blk cipher.Block, plain []byte) string {
	t.Helper()
	g, _ := cipher.NewGCM(blk)
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, plain, nil))
}

func encCFB(blk cipher.Block, plain []byte) string {
	iv := make([]byte, aes.BlockSize)
	_, _ = rand.Read(iv)
	out := make([]byte, len(plain))
	cipher.NewCFBEncrypter(blk, iv).XORKeyStream(out, plain) //nolint:staticcheck // legacy layout under test
	return base64.StdEncoding.EncodeToString(append(iv, out...))
}

func genKey(t *testing.T, pass string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var blk *pem.Block
	if pass == "" {
		blk, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		blk, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(pass))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(blk))
}

func payload(login, pass, key string) []byte {
	b, _ := json.Marshal(map[string]string{"login": login, "passphrase": pass, "private_key": key})
	return b
}

func TestDecryptLayouts(t *testing.T) {
	key, blk := testKeyB64(t)
	pk := genKey(t, "")
	pl := payload("deploy", "", pk)
	tests := []struct {
		name string
		col  string
		key  Secret
	}{
		{"gcm", encGCM(t, blk, pl), key},
		{"cfb", encCFB(blk, pl), key},
		{"plain base64", base64.StdEncoding.EncodeToString(pl), Secret{}},
		{"plain json", string(pl), Secret{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Decrypt(Key{Name: "k", Type: "ssh", Secret: tt.col}, tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if c.Login != "deploy" || !strings.HasPrefix(c.Fingerprint, "SHA256:") || c.Unprotected {
				t.Fatalf("unexpected credential: %s %s %v", c.Login, c.Fingerprint, c.Unprotected)
			}
			if string(c.PrivateKey.Reveal()) != pk {
				t.Fatal("private key altered")
			}
		})
	}
}

func TestDecryptPassphraseRemoved(t *testing.T) {
	key, blk := testKeyB64(t)
	enc := genKey(t, "s3cret-pass")
	c, err := Decrypt(Key{Name: "k", Type: "ssh", Secret: encGCM(t, blk, payload("u", "s3cret-pass", enc))}, key)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Unprotected {
		t.Fatal("expected passphrase removal")
	}
	if _, err := ssh.ParseRawPrivateKey(c.PrivateKey.Reveal()); err != nil {
		t.Fatalf("result is not an unencrypted key: %v", err)
	}
}

func TestDecryptMissingPassphraseAndWrongKey(t *testing.T) {
	key, blk := testKeyB64(t)
	enc := genKey(t, "pp")
	col := encGCM(t, blk, payload("u", "", enc))
	if _, err := Decrypt(Key{Name: "k", Type: "ssh", Secret: col}, key); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("want passphrase error, got %v", err)
	}
	other, _ := testKeyB64(t)
	if _, err := Decrypt(Key{Name: "k", Type: "ssh", Secret: col}, other); err == nil {
		t.Fatal("wrong key must fail")
	}
}

func TestDecryptLoginPasswordAndNone(t *testing.T) {
	key, blk := testKeyB64(t)
	pl, _ := json.Marshal(map[string]string{"login": "bob", "password": "hunter2-xyz"})
	c, err := Decrypt(Key{Name: "p", Type: "login_password", Secret: encGCM(t, blk, pl)}, key)
	if err != nil || c.Login != "bob" || string(c.Password.Reveal()) != "hunter2-xyz" {
		t.Fatalf("login_password: %v", err)
	}
	if c, err := Decrypt(Key{Name: "n", Type: "none"}, key); c != nil || err != nil {
		t.Fatalf("none: %v %v", c, err)
	}
}

func TestSecretNeverFormats(t *testing.T) {
	s := NewSecret([]byte("topsecret-value"))
	j, _ := json.Marshal(struct{ S Secret }{s})
	outs := []string{
		s.String(), fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s), string(j),
		fmt.Sprint(Credential{Password: s, PrivateKey: s}), fmt.Sprintf("%+v", Credential{Password: s}),
		slog.AnyValue(s).String(),
	}
	for _, o := range outs {
		if strings.Contains(o, "topsecret") || strings.Contains(o, hex.EncodeToString([]byte("topsecret"))) {
			t.Fatalf("leak: %s", o)
		}
	}
}

func writeBackup(t *testing.T, files map[string]string, sums bool) string {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(c))
		fmt.Fprintf(&sb, "%s  ./%s\n", hex.EncodeToString(h[:]), n)
	}
	if sums {
		_ = os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sb.String()), 0o600)
	}
	return dir
}

func TestOpenBackup(t *testing.T) {
	good := map[string]string{"config.json": `{"access_key_encryption":"abc="}`, "semaphore-db.sql.gz": "x"}
	dir := writeBackup(t, good, true)
	b, err := OpenBackup(dir)
	if err != nil || b.Files != 2 || string(b.EncryptionKey.Reveal()) != "abc=" {
		t.Fatalf("good backup: %v", err)
	}
	if _, err := OpenBackup(writeBackup(t, good, false)); err == nil {
		t.Fatal("missing SHA256SUMS must fail")
	}
	if _, err := OpenBackup(writeBackup(t, map[string]string{"config.json": "{}"}, true)); err == nil {
		t.Fatal("missing dump must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "semaphore-db.sql.gz"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBackup(dir); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("tamper not detected: %v", err)
	}
	if _, err := OpenBackup(""); err == nil {
		t.Fatal("empty dir must fail")
	}
}

func TestOpenBackupEffectiveKeyPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		effective string
		want      string
	}{
		{"env overrides config.json", `{"access_key_encryption":"env="}`, "env="},
		{"empty env falls back", `{}`, "cfg="},
		{"no effective file", "", "cfg="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"config.json": `{"access_key_encryption":"cfg="}`, "semaphore-db.sql.gz": "x"}
			if tt.effective != "" {
				files[effectiveConfigFile] = tt.effective
			}
			b, err := OpenBackup(writeBackup(t, files, true))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(b.EncryptionKey.Reveal()); got != tt.want {
				t.Fatalf("key = %q, want %q", got, tt.want)
			}
		})
	}
}
