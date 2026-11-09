package notify

import (
	"strings"
	"testing"
)

func TestMailDomain(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"acp@example.org", "example.org"},
		{"ACP <acp@Example.ORG>", "example.org"},
		{"broken", "localhost.localdomain"},
		{"trailing@", "localhost.localdomain"},
	} {
		if got := mailDomain(tc.in); got != tc.want {
			t.Errorf("mailDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildMailHasMessageID(t *testing.T) {
	m := string(buildMail(smtpConfig{From: "acp@example.org", To: []string{"ops@example.org"}}, Message{}))
	if !strings.Contains(m, "\r\nMessage-ID: <") || !strings.Contains(m, "@example.org>\r\n") {
		t.Fatalf("missing Message-ID header:\n%s", m)
	}
}
