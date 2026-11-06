package semaphore

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Secret holds sensitive bytes. Every formatting path (String, %v, %#v, JSON,
// text, slog) yields a mask; the plaintext is only reachable via Reveal.
type Secret struct{ b []byte }

// NewSecret copies b into a Secret.
func NewSecret(b []byte) Secret { return Secret{b: append([]byte(nil), b...)} }

// Reveal returns the plaintext. Callers must hand it straight to secrets.Service.
func (s Secret) Reveal() []byte { return s.b }

// Empty reports whether the secret holds no bytes.
func (s Secret) Empty() bool { return len(s.b) == 0 }

func (Secret) String() string               { return redacted }
func (Secret) GoString() string             { return redacted }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (s Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }
