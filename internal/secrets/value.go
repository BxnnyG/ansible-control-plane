package secrets

import (
	"log/slog"
)

const mask = "***"

// Value holds a plaintext secret and masks itself in every common output path.
type Value struct {
	b []byte
}

// NewValue copies b into a new Value.
func NewValue(b []byte) Value {
	c := make([]byte, len(b))
	copy(c, b)
	return Value{b: c}
}

// Reveal returns the underlying bytes. Callers must not log or retain them.
func (v Value) Reveal() []byte { return v.b }

// Len returns the plaintext length.
func (v Value) Len() int { return len(v.b) }

// Zero overwrites the buffer.
func (v *Value) Zero() { zero(v.b); v.b = nil }

func (v Value) String() string   { return mask }
func (v Value) GoString() string { return mask }

// MarshalJSON always emits a masked string.
func (v Value) MarshalJSON() ([]byte, error) { return []byte(`"` + mask + `"`), nil }

// MarshalText masks the value for text encoders.
func (v Value) MarshalText() ([]byte, error) { return []byte(mask), nil }

// LogValue implements slog.LogValuer.
func (v Value) LogValue() slog.Value { return slog.StringValue(mask) }

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
