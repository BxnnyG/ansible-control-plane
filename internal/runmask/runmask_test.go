package runmask

import (
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	begin, end := "-----BEGIN OPENSSH "+"PRIVATE KEY-----", "-----END OPENSSH "+"PRIVATE KEY-----"
	key := begin + "\nAAAAbbbbCCCC\nddddEEEE\n" + end + "\n"
	m := New([]string{"hunter22", key, "ab", "pa\"ss word", "hunter22-long"})
	tests := []struct{ name, in, want string }{
		{"plain", "password is hunter22!", "password is ***!"},
		{"longest first", "x hunter22-long y", "x *** y"},
		{"key line", "debug: AAAAbbbbCCCC end", "debug: *** end"},
		{"armor kept", begin, begin},
		{"short ignored", "ab ab", "ab ab"},
		{"json escaped", `{"p": "pa\"ss word"}`, `{"p": "***"}`},
		{"raw", `pa"ss word`, `***`},
		{"nothing", "hello", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Mask(tt.in); got != tt.want {
				t.Errorf("Mask(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if strings.Contains(New(nil).Mask("x"), "***") {
		t.Error("empty masker changed input")
	}
	var nilM *Masker
	if nilM.Mask("a") != "a" {
		t.Error("nil masker")
	}
}
