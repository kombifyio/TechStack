package walletservice

import (
	"fmt"
	"io"
	"log/slog"
)

// secretMarker is what a Secret prints everywhere.
// #nosec G101 -- a fixed placeholder text, not a credential.
const secretMarker = "[wallet credential]"

// Secret holds a credential the Wallet released to this service. It lives in
// memory for the attempt that asked for it: Wipe overwrites the buffer, and
// the type renders as a fixed marker in fmt, slog and JSON, so it cannot reach
// a log, an error or a response by accident.
//
// Bytes returns the live buffer, valid until Wipe. Reveal copies the value
// into an immutable string for APIs that take only strings; that copy cannot
// be wiped, so hand it to one call and do not keep it.
type Secret struct {
	value []byte
}

// NewSecret wraps value and takes ownership of it.
func NewSecret(value []byte) Secret { return Secret{value: value} }

// Bytes returns the value without copying it. It is empty after Wipe.
func (s Secret) Bytes() []byte { return s.value }

// Reveal returns a copy of the value as a string.
func (s Secret) Reveal() string { return string(s.value) }

// Len is the value's length in bytes.
func (s Secret) Len() int { return len(s.value) }

// Wipe overwrites the value. Every copy of the Secret shares the buffer, so
// all of them are emptied.
func (s *Secret) Wipe() {
	if s != nil {
		clear(s.value)
		s.value = nil
	}
}

// The rendering methods take the value receiver, so formatting the value, a
// pointer to it, or a struct that embeds it is equally safe.
func (Secret) String() string   { return secretMarker }
func (Secret) GoString() string { return secretMarker }

// Format prints the marker for every verb and flag.
func (Secret) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, secretMarker) }

// LogValue keeps the value out of slog output.
func (Secret) LogValue() slog.Value { return slog.StringValue(secretMarker) }

// MarshalJSON and MarshalText keep the value out of encoded output.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + secretMarker + `"`), nil }
func (Secret) MarshalText() ([]byte, error) { return []byte(secretMarker), nil }
