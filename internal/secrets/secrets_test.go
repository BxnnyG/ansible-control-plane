package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(seed byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = seed + byte(i*7)
	}
	return k
}

func newProv(t *testing.T, seed byte, retired ...[]byte) *LocalProvider {
	t.Helper()
	p, err := NewLocalProvider(testKey(seed), retired...)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var aad = AAD{SecretID: "s1", WorkspaceID: "w1"}

func TestRoundTrip(t *testing.T) {
	p := newProv(t, 1)
	env, err := Seal(p, []byte("hunter2"), aad)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(p, env, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(v.Reveal()) != "hunter2" {
		t.Fatal("mismatch")
	}
	v.Zero()
	if v.Len() != 0 {
		t.Fatal("not zeroed")
	}
}

func TestTamper(t *testing.T) {
	p := newProv(t, 1)
	other := newProv(t, 2)
	tests := []struct {
		name   string
		mutate func(e *Envelope)
		prov   Provider
		aad    AAD
		want   error
	}{
		{"wrong secret id", func(*Envelope) {}, p, AAD{"s2", "w1"}, ErrDecrypt},
		{"wrong workspace", func(*Envelope) {}, p, AAD{"s1", "w2"}, ErrDecrypt},
		{"ciphertext", func(e *Envelope) { e.Ciphertext[0] ^= 1 }, p, aad, ErrDecrypt},
		{"payload nonce", func(e *Envelope) { e.Nonce[0] ^= 1 }, p, aad, ErrDecrypt},
		{"wrapped dek", func(e *Envelope) { e.WrappedDEK[0] ^= 1 }, p, aad, ErrDecrypt},
		{"wrap nonce", func(e *Envelope) { e.WrapNonce[0] ^= 1 }, p, aad, ErrDecrypt},
		{"wrong master key", func(*Envelope) {}, other, aad, ErrUnknownKey},
		{"bad version", func(e *Envelope) { e.Version = 9 }, p, aad, ErrBadEnvelope},
		{"short nonce", func(e *Envelope) { e.Nonce = e.Nonce[:3] }, p, aad, ErrBadEnvelope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Seal(p, []byte("secret"), aad)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(env)
			_, err = Open(tt.prov, env, tt.aad)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestWrongMasterSameID(t *testing.T) {
	p := newProv(t, 1)
	env, _ := Seal(p, []byte("x"), aad)
	env.KeyID = KeyIDFor(testKey(3))
	q := newProv(t, 3)
	if _, err := Open(q, env, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("got %v", err)
	}
}

func TestRotate(t *testing.T) {
	oldK, newK := testKey(1), testKey(5)
	oldP := newProv(t, 1)
	env, err := Seal(oldP, []byte("payload"), aad)
	if err != nil {
		t.Fatal(err)
	}
	np, err := NewLocalProvider(newK, oldK)
	if err != nil {
		t.Fatal(err)
	}
	re, err := np.Rotate(env, aad)
	if err != nil {
		t.Fatal(err)
	}
	if re.KeyID != KeyIDFor(newK) || re.KeyID == env.KeyID {
		t.Fatal("key id not updated")
	}
	if !bytes.Equal(re.Ciphertext, env.Ciphertext) {
		t.Fatal("payload should be untouched")
	}
	// new provider without retired key can read the rotated envelope
	onlyNew := newProv(t, 5)
	v, err := Open(onlyNew, re, aad)
	if err != nil || string(v.Reveal()) != "payload" {
		t.Fatalf("open rotated: %v", err)
	}
	// old envelope still readable with retired key
	if _, err := Open(np, env, aad); err != nil {
		t.Fatal(err)
	}
	if _, err := np.Rotate(env, AAD{"x", "y"}); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("rotate wrong aad: %v", err)
	}
}

func TestNonceUniqueness(t *testing.T) {
	p := newProv(t, 1)
	a, _ := Seal(p, []byte("same"), aad)
	b, _ := Seal(p, []byte("same"), aad)
	if bytes.Equal(a.Ciphertext, b.Ciphertext) || bytes.Equal(a.WrappedDEK, b.WrappedDEK) {
		t.Fatal("expected distinct output")
	}
}

func TestParseMasterKey(t *testing.T) {
	good := testKey(1)
	hexs := fmt.Sprintf("%x", good)
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"base64 newline", base64.StdEncoding.EncodeToString(good) + "\n", true},
		{"hex", hexs, true},
		{"short", base64.StdEncoding.EncodeToString(good[:16]), false},
		{"empty", "", false},
		{"garbage", "!!!", false},
		{"all zero", base64.StdEncoding.EncodeToString(make([]byte, 32)), false},
		{"repeating", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("ab"), 16)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := ParseMasterKey([]byte(tt.in))
			if tt.ok != (err == nil) {
				t.Fatalf("err=%v", err)
			}
			if tt.ok && !bytes.Equal(k, good) {
				t.Fatal("decoded mismatch")
			}
			if !tt.ok && !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("want ErrInvalidKey, got %v", err)
			}
		})
	}
}

func TestLoadMasterKey(t *testing.T) {
	enc := base64.StdEncoding.EncodeToString(testKey(1))
	path := filepath.Join(t.TempDir(), "m.key")
	if err := os.WriteFile(path, []byte(enc+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalProviderFromFile(path, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_TEST_MK", enc)
	if _, err := LoadMasterKey("", "ACP_TEST_MK"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMasterKey("", "ACP_TEST_UNSET"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("got %v", err)
	}
	if _, err := LoadMasterKey(filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestValueMasking(t *testing.T) {
	v := NewValue([]byte("topsecret"))
	j, _ := json.Marshal(struct{ V Value }{v})
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "v", v)
	outs := []string{
		v.String(), v.GoString(), fmt.Sprintf("%v %+v %#v %s", v, v, v, v),
		string(j), buf.String(),
	}
	for _, o := range outs {
		if strings.Contains(o, "topsecret") {
			t.Fatalf("leak: %s", o)
		}
	}
	if !strings.Contains(buf.String(), "***") {
		t.Fatal("slog not masked")
	}
}
