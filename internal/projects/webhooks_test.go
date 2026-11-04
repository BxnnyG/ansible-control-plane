package projects

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func hexMAC(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func hdr(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestVerifySignature(t *testing.T) {
	const secret, body = "s3cret-test", `{"ref":"refs/heads/main"}`
	good := hexMAC(secret, body)
	bad := hexMAC("other", body)
	tests := []struct {
		name     string
		provider string
		secret   string
		headers  map[string]string
		body     string
		want     bool
	}{
		{"gitea ok", ProviderGitea, secret, map[string]string{"X-Gitea-Signature": good}, body, true},
		{"gitea wrong secret", ProviderGitea, secret, map[string]string{"X-Gitea-Signature": bad}, body, false},
		{"gitea prefixed rejected", ProviderGitea, secret, map[string]string{"X-Gitea-Signature": "sha256=" + good}, body, false},
		{"gitea tampered body", ProviderGitea, secret, map[string]string{"X-Gitea-Signature": good}, body + " ", false},
		{"gitea missing", ProviderGitea, secret, nil, body, false},
		{"github ok", ProviderGitHub, secret, map[string]string{"X-Hub-Signature-256": "sha256=" + good}, body, true},
		{"github no prefix", ProviderGitHub, secret, map[string]string{"X-Hub-Signature-256": good}, body, false},
		{"github sha1 prefix", ProviderGitHub, secret, map[string]string{"X-Hub-Signature-256": "sha1=" + good}, body, false},
		{"github wrong", ProviderGitHub, secret, map[string]string{"X-Hub-Signature-256": "sha256=" + bad}, body, false},
		{"github not hex", ProviderGitHub, secret, map[string]string{"X-Hub-Signature-256": "sha256=zz"}, body, false},
		{"gitlab ok", ProviderGitLab, secret, map[string]string{"X-Gitlab-Token": secret}, body, true},
		{"gitlab wrong", ProviderGitLab, secret, map[string]string{"X-Gitlab-Token": secret + "x"}, body, false},
		{"gitlab missing", ProviderGitLab, secret, nil, body, false},
		{"generic ok", ProviderGeneric, secret, map[string]string{"X-ACP-Signature": "sha256=" + good}, body, true},
		{"generic wrong", ProviderGeneric, secret, map[string]string{"X-ACP-Signature": "sha256=" + bad}, body, false},
		{"generic no prefix", ProviderGeneric, secret, map[string]string{"X-ACP-Signature": good}, body, false},
		{"provider mismatch", ProviderGitHub, secret, map[string]string{"X-Gitea-Signature": good}, body, false},
		{"empty secret", ProviderGitLab, "", map[string]string{"X-Gitlab-Token": ""}, body, false},
		{"unknown provider", "bitbucket", secret, map[string]string{"X-Gitlab-Token": secret}, body, false},
	}
	for _, tc := range tests {
		if got := VerifySignature(tc.provider, []byte(tc.secret), hdr(tc.headers), []byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestRefMatches(t *testing.T) {
	tests := []struct {
		pushed, configured string
		want               bool
	}{
		{"refs/heads/main", "main", true},
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/heads/dev", "main", false},
		{"refs/heads/main2", "main", false},
		{"refs/tags/v1", "v1", true},
		{"main", "main", true},
		{"", "main", false},
		{"refs/heads/main", "", false},
	}
	for _, tc := range tests {
		if got := RefMatches(tc.pushed, tc.configured); got != tc.want {
			t.Errorf("RefMatches(%q,%q)=%v want %v", tc.pushed, tc.configured, got, tc.want)
		}
	}
}

func TestPushedRef(t *testing.T) {
	if got := pushedRef([]byte(`{"ref":"refs/heads/x","after":"abc"}`)); got != "refs/heads/x" {
		t.Errorf("got %q", got)
	}
	for _, in := range []string{``, `not json`, `{"ref":5}`, `{}`} {
		if got := pushedRef([]byte(in)); got != "" {
			t.Errorf("pushedRef(%q)=%q", in, got)
		}
	}
}
