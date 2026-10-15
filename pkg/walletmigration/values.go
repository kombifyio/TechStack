package walletmigration

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
)

// ValueEncoding says how the Techstack wallet stored one value.
type ValueEncoding string

const (
	// ValueAbsent means the row carries no value in this field.
	ValueAbsent ValueEncoding = "absent"
	// ValueEncryptedV1 is an enc:v1: AES-256-GCM envelope under
	// TECHSTACK_ENCRYPTION_KEY.
	ValueEncryptedV1 ValueEncoding = "enc_v1"
	// ValuePlaintext is stored as written: legacy plaintext for a secret or
	// TOTP field, the normal form for usernames, public keys and notes.
	ValuePlaintext ValueEncoding = "plaintext"
)

// The AES-GCM envelope is a 12-byte nonce followed by ciphertext and a 16-byte
// tag, so anything shorter cannot be a value written by pkg/auth.
const minEnvelopeBytes = 12 + 16

var (
	// ErrNoDecryptor is returned when an enc:v1 value is opened without the
	// legacy key's decryptor.
	ErrNoDecryptor = errors.New("walletmigration: an enc:v1 value needs the legacy decryptor")
	// errMalformedValue marks a value that is neither absent, a well-formed
	// enc:v1 envelope nor a plaintext string.
	errMalformedValue = errors.New("walletmigration: malformed stored value")
)

// rawValue sits behind a pointer so that even a reflective print of a struct
// holding a SourceValue in an unexported field shows an address, not content.
type rawValue struct{ stored string }

// SourceValue is one value exactly as the Techstack wallet stored it. It never
// formats, marshals or logs its content: every printing path renders a
// redaction that names only the encoding. Open is the only way to read it.
type SourceValue struct {
	encoding ValueEncoding
	raw      *rawValue
}

// inspectStoredValue classifies one metadata field without any key. A value
// that claims an envelope but cannot be one, or that is not a string, is
// malformed: migrating it as plaintext would capture ciphertext as a secret.
func inspectStoredValue(value any) (SourceValue, error) {
	if value == nil {
		return SourceValue{encoding: ValueAbsent}, nil
	}
	stored, ok := value.(string)
	if !ok {
		return SourceValue{encoding: ValueAbsent}, errMalformedValue
	}
	text := strings.TrimSpace(stored)
	switch {
	case text == "":
		return SourceValue{encoding: ValueAbsent}, nil
	case auth.IsEncrypted(text):
		decoded, err := base64.StdEncoding.DecodeString(text[len(auth.EncryptedPrefix):])
		if err != nil || len(decoded) < minEnvelopeBytes {
			return SourceValue{encoding: ValueAbsent}, errMalformedValue
		}
		return SourceValue{encoding: ValueEncryptedV1, raw: &rawValue{stored: text}}, nil
	case strings.HasPrefix(text, "enc:"):
		// An unknown envelope version is not legacy plaintext.
		return SourceValue{encoding: ValueAbsent}, errMalformedValue
	default:
		// A legacy plaintext secret keeps its exact bytes: the legacy readers
		// return it as stored, so trimming here would change the secret.
		return SourceValue{encoding: ValuePlaintext, raw: &rawValue{stored: stored}}, nil
	}
}

// plainSourceValue carries a non-secret plaintext field (username, public key,
// notes) through the same redacting type as secrets.
func plainSourceValue(text string) SourceValue {
	text = strings.TrimSpace(text)
	if text == "" {
		return SourceValue{encoding: ValueAbsent}
	}
	return SourceValue{encoding: ValuePlaintext, raw: &rawValue{stored: text}}
}

// Encoding reports how the value is stored.
func (v SourceValue) Encoding() ValueEncoding {
	if v.encoding == "" {
		return ValueAbsent
	}
	return v.encoding
}

// Present reports whether the field carries a value.
func (v SourceValue) Present() bool {
	return v.Encoding() != ValueAbsent && v.raw != nil
}

// Open returns the value in clear. An enc:v1 value is opened with decrypt; a
// plaintext value is returned as stored. Only the execute path, after the
// Wallet service API exists, may call it. The dry-run planner never does.
func (v SourceValue) Open(decrypt func(string) (string, error)) (string, error) {
	if !v.Present() {
		return "", nil
	}
	if v.encoding == ValuePlaintext {
		return v.raw.stored, nil
	}
	if decrypt == nil {
		return "", ErrNoDecryptor
	}
	return decrypt(v.raw.stored)
}

// String renders a redaction.
func (v SourceValue) String() string {
	return "[redacted:" + string(v.Encoding()) + "]"
}

// GoString renders the same redaction for %#v.
func (v SourceValue) GoString() string { return v.String() }

// Format renders the redaction for every verb and flag.
func (v SourceValue) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(v.String()))
}

// LogValue keeps structured logs redacted.
func (v SourceValue) LogValue() slog.Value { return slog.StringValue(v.String()) }

// MarshalJSON emits only the encoding.
func (v SourceValue) MarshalJSON() ([]byte, error) {
	return []byte(`{"encoding":"` + string(v.Encoding()) + `"}`), nil
}

// MarshalText emits only the redaction, for encoders that prefer text.
func (v SourceValue) MarshalText() ([]byte, error) { return []byte(v.String()), nil }
