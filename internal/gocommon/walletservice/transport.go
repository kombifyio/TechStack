package walletservice

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// The Wallet transport (`kombify.wallet-transport.v1`) is the Go side of
// kombify-Gateway `services/wallet-store/src/transport.mjs`: a compact JWE
// with ECDH-ES+A256KW and A256GCM whose protected header marks the member
// `wallet_ctx` critical and carries the authenticated transport context. A
// value sealed for one owner, item version, operation, audience, key or
// expiry never opens for another.
//
// Neither go-jose (it rejects every `crit` header on decryption) nor a general
// JWE library is used. The cryptography is the standard library's (ECDH on
// P-256, AES, GCM, SHA-256); the two small primitives it lacks, RFC 7518
// section 4.6 Concat KDF and RFC 3394 AES key wrap, are implemented below and
// pinned by their RFC vectors and by values sealed by transport.mjs itself.
// Everything else here is header and context discipline.

const (
	transportType     = "kombify.wallet-transport.v1"
	keyAlgorithm      = "ECDH-ES+A256KW"
	contentEncryption = "A256GCM"
	criticalContext   = "wallet_ctx"

	namespacePersonal = "personal"
	vaultPersonal     = "personal"
	storeAudience     = "urn:kombify:wallet-store"

	opCapture = "capture"
	opProcess = "process"
	opMint    = "mint"

	curveP256 = "P-256"

	// JSON member names shared by the canonical context, requests and answers.
	memberOperationID = "operationId"
	memberExpiresAt   = "expiresAt"
	memberPrincipals  = "principals"
	memberHeaders     = "headers"

	maxValueBytes    = 65_536
	maxEnvelopeBytes = 100_000
	maxResultLife    = 300 * time.Second
	maxFieldUnits    = 512
)

var (
	decimalPattern    = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	thumbprintPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	envelopePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	keyIDPattern      = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

	// The operations transport.mjs admits. A service seals `capture` and opens
	// `process` and `mint`; the others exist so the context grammar is exactly
	// the store's.
	operations = map[string]bool{"capture": true, "reveal": true, "export": true, "process": true, "mint": true}

	b64 = base64.RawURLEncoding.Strict()
)

// transportContext is the fixed authenticated context. The struct's field
// order is the canonical encoding order, and its JSON tags are the names the
// Gateway returns in the `context` member of a sealed answer.
type transportContext struct {
	CustodyNamespace       string `json:"custodyNamespace"`
	OwnerSubject           string `json:"ownerSubject"`
	VaultRef               string `json:"vaultRef"`
	ItemVersion            string `json:"itemVersion"`
	KeyGeneration          string `json:"keyGeneration"`
	Operation              string `json:"operation"`
	OperationID            string `json:"operationId"`
	Audience               string `json:"audience"`
	RecipientKeyThumbprint string `json:"recipientKeyThumbprint"`
	// ExpiresAt is epoch milliseconds.
	ExpiresAt int64 `json:"expiresAt"`
}

// canonical validates the context and encodes it exactly as `JSON.stringify`
// does for the ordered object in transport.mjs: members in the fixed order, no
// whitespace, only `"` and `\` escaped (control characters are refused before
// encoding), and an integer expiry.
func (c transportContext) canonical() ([]byte, error) {
	if !operations[c.Operation] || !decimalPattern.MatchString(c.ItemVersion) || !decimalPattern.MatchString(c.KeyGeneration) ||
		!thumbprintPattern.MatchString(c.RecipientKeyThumbprint) || c.ExpiresAt <= 0 || c.ExpiresAt > 1<<53-1 {
		return nil, errTransport()
	}
	var out strings.Builder
	out.WriteByte('{')
	for _, field := range []struct{ name, value string }{
		{"custodyNamespace", c.CustodyNamespace}, {"ownerSubject", c.OwnerSubject}, {"vaultRef", c.VaultRef},
		{"itemVersion", c.ItemVersion}, {"keyGeneration", c.KeyGeneration}, {"operation", c.Operation},
		{memberOperationID, c.OperationID}, {"audience", c.Audience}, {"recipientKeyThumbprint", c.RecipientKeyThumbprint},
	} {
		if !validField(field.value) {
			return nil, errTransport()
		}
		out.WriteString(`"` + field.name + `":"`)
		for _, r := range field.value {
			if r == '"' || r == '\\' {
				out.WriteByte('\\')
			}
			out.WriteRune(r)
		}
		out.WriteString(`",`)
	}
	out.WriteString(`"` + memberExpiresAt + `":` + strconv.FormatInt(c.ExpiresAt, 10) + `}`)
	return []byte(out.String()), nil
}

// validField mirrors the store: non-empty, valid text, at most 512 UTF-16 code
// units (JavaScript string length), no control characters.
func validField(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(utf16.Encode([]rune(value))) > maxFieldUnits {
		return false
	}
	for _, r := range value {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}

// checkLifetime mirrors transport.mjs: the result expires after now and within
// five minutes of it.
func (c transportContext) checkLifetime(now time.Time) error {
	at := time.UnixMilli(c.ExpiresAt)
	if !at.After(now) || at.After(now.Add(maxResultLife)) {
		return errTransport()
	}
	return nil
}

// PublicJWK is a P-256 public key as the Wallet publishes and enrolls it.
type PublicJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func publicJWKOf(key *ecdh.PublicKey) PublicJWK {
	raw := key.Bytes() // 0x04 || X || Y
	return PublicJWK{
		Kty: "EC", Crv: curveP256,
		X: base64.RawURLEncoding.EncodeToString(raw[1:33]),
		Y: base64.RawURLEncoding.EncodeToString(raw[33:65]),
	}
}

// Thumbprint is the RFC 7638 SHA-256 thumbprint (required members in lexical
// order, no whitespace), base64url without padding.
func (k PublicJWK) Thumbprint() string {
	sum := sha256.Sum256([]byte(`{"crv":"` + k.Crv + `","kty":"` + k.Kty + `","x":"` + k.X + `","y":"` + k.Y + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// key imports the JWK; it refuses anything but a canonical, valid P-256 point.
func (k PublicJWK) key() (*ecdh.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != curveP256 {
		return nil, errTransport()
	}
	x, errX := b64.DecodeString(k.X)
	y, errY := b64.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, errTransport()
	}
	key, err := ecdh.P256().NewPublicKey(append(append([]byte{0x04}, x...), y...))
	if err != nil {
		return nil, errTransport()
	}
	return key, nil
}

// keyPair is a P-256 ECDH key pair with the key id its envelopes carry and the
// generation the Wallet pins for it. A service's pair is its recipient key; the
// store's capture pair exists here only for the test double of the store.
type keyPair struct {
	private    *ecdh.PrivateKey
	keyID      string
	generation string
	thumbprint string
}

func newKeyPair(private *ecdh.PrivateKey, keyID, generation string) *keyPair {
	return &keyPair{private: private, keyID: keyID, generation: generation, thumbprint: publicJWKOf(private.PublicKey()).Thumbprint()}
}

// deriveKEK is the ECDH-ES key agreement with Concat KDF (RFC 7518 section
// 4.6, NIST SP 800-56A single-step with SHA-256) for key wrapping: AlgorithmID
// is the `alg` value, no apu and no apv, and a 256-bit key. 256 bits is one
// SHA-256 block, so a single counter round yields the key.
func deriveKEK(shared []byte) []byte {
	prefixed := func(data []byte) []byte {
		out := make([]byte, 4+len(data))
		binary.BigEndian.PutUint32(out, uint32(len(data)))
		copy(out[4:], data)
		return out
	}
	hash := sha256.New()
	hash.Write([]byte{0, 0, 0, 1}) // round counter
	hash.Write(shared)
	hash.Write(prefixed([]byte(keyAlgorithm))) // AlgorithmID
	hash.Write(prefixed(nil))                  // PartyUInfo (apu)
	hash.Write(prefixed(nil))                  // PartyVInfo (apv)
	hash.Write([]byte{0, 0, 1, 0})             // SuppPubInfo: key length in bits
	return hash.Sum(nil)
}

var keyWrapIV = [8]byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

// aesKeyWrap is RFC 3394 AES key wrap of plain (a multiple of 8 bytes, at
// least 16) under kek.
func aesKeyWrap(block cipher.Block, plain []byte) []byte {
	n := len(plain) / 8
	a := keyWrapIV
	r := bytes.Clone(plain)
	var b [16]byte
	for j := 0; j < 6; j++ {
		for i := 0; i < n; i++ {
			copy(b[:8], a[:])
			copy(b[8:], r[i*8:i*8+8])
			block.Encrypt(b[:], b[:])
			binary.BigEndian.PutUint64(a[:], binary.BigEndian.Uint64(b[:8])^uint64(n*j+i+1))
			copy(r[i*8:], b[8:])
		}
	}
	return append(a[:], r...)
}

// aesKeyUnwrap reverses aesKeyWrap and fails unless the integrity register
// comes back as the RFC 3394 default IV.
func aesKeyUnwrap(block cipher.Block, wrapped []byte) ([]byte, bool) {
	if len(wrapped) < 24 || len(wrapped)%8 != 0 {
		return nil, false
	}
	n := len(wrapped)/8 - 1
	var a [8]byte
	copy(a[:], wrapped[:8])
	r := bytes.Clone(wrapped[8:])
	var b [16]byte
	for j := 5; j >= 0; j-- {
		for i := n - 1; i >= 0; i-- {
			binary.BigEndian.PutUint64(b[:8], binary.BigEndian.Uint64(a[:])^uint64(n*j+i+1))
			copy(b[8:], r[i*8:i*8+8])
			block.Decrypt(b[:], b[:])
			copy(a[:], b[:8])
			copy(r[i*8:], b[8:])
		}
	}
	if subtle.ConstantTimeCompare(a[:], keyWrapIV[:]) != 1 {
		clear(r)
		return nil, false
	}
	return r, true
}

type protectedHeader struct {
	Alg       string    `json:"alg"`
	Enc       string    `json:"enc"`
	Typ       string    `json:"typ"`
	Kid       string    `json:"kid"`
	Epk       PublicJWK `json:"epk"`
	Crit      []string  `json:"crit"`
	WalletCtx string    `json:"wallet_ctx"`
}

// seal encrypts value to a public key, binding ctx into the protected header.
// It is the capture direction: a service to the store's capture key. public
// must be the key whose thumbprint ctx names.
func seal(value []byte, public PublicJWK, keyID string, ctx transportContext, now time.Time) (string, error) {
	canonical, err := ctx.canonical()
	if err != nil || ctx.checkLifetime(now) != nil {
		return "", errTransport()
	}
	if public.Thumbprint() != ctx.RecipientKeyThumbprint || !keyIDPattern.MatchString(keyID) {
		return "", errTransport()
	}
	return sealEnvelope(value, public, protectedHeader{
		Alg: keyAlgorithm, Enc: contentEncryption, Typ: transportType, Kid: keyID,
		Crit: []string{criticalContext}, WalletCtx: base64.RawURLEncoding.EncodeToString(canonical),
	})
}

// sealEnvelope is the ECDH-ES+A256KW / A256GCM encryption of value to public
// under header (its ephemeral key is filled in here). seal builds the one
// header the transport defines; tests build others to prove open refuses them.
func sealEnvelope(value []byte, public PublicJWK, header protectedHeader) (string, error) {
	recipient, err := public.key()
	if err != nil || len(value) == 0 || len(value) > maxValueBytes {
		return "", errTransport()
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", errTransport()
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return "", errTransport()
	}
	kek := deriveKEK(shared)
	defer clear(shared)
	defer clear(kek)
	cek := make([]byte, 32)
	if _, err = rand.Read(cek); err != nil {
		return "", errTransport()
	}
	defer clear(cek)
	kekBlock, err := aes.NewCipher(kek)
	if err != nil {
		return "", errTransport()
	}
	wrapped := aesKeyWrap(kekBlock, cek)
	header.Epk = publicJWKOf(ephemeral.PublicKey())
	encoded, err := json.Marshal(header)
	if err != nil {
		return "", errTransport()
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(encoded)
	contentBlock, err := aes.NewCipher(cek)
	if err != nil {
		return "", errTransport()
	}
	gcm, err := cipher.NewGCM(contentBlock)
	if err != nil {
		return "", errTransport()
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(iv); err != nil {
		return "", errTransport()
	}
	sealed := gcm.Seal(nil, iv, value, []byte(encodedHeader))
	split := len(sealed) - gcm.Overhead()
	enc := base64.RawURLEncoding.EncodeToString
	return strings.Join([]string{encodedHeader, enc(wrapped), enc(iv), enc(sealed[:split]), enc(sealed[split:])}, "."), nil
}

// open decrypts an envelope sealed to this key pair. The protected header is
// checked first, and nothing is decrypted unless it carries exactly the
// expected context, the pinned algorithms, this key's id and only the members
// the transport defines. A valid JWE is not sender authentication: open mints
// no authority, and callers pass only an envelope the Wallet returned on its
// authenticated call. The caller wipes the returned plaintext.
func (k *keyPair) open(envelope string, expected transportContext, now func() time.Time) ([]byte, error) {
	canonical, err := expected.canonical()
	if err != nil || !k.names(expected) || expected.checkLifetime(now()) != nil {
		return nil, errTransport()
	}
	parts, ok := splitEnvelope(envelope)
	if !ok {
		return nil, errTransport()
	}
	header, err := checkHeader(parts.header, k.keyID, canonical)
	if err != nil {
		return nil, err
	}
	plaintext, err := k.decrypt(parts, header)
	if err != nil {
		return nil, err
	}
	// Unwrapping and decryption take real time: the clock is read again.
	if expected.checkLifetime(now()) != nil {
		clear(plaintext)
		return nil, errTransport()
	}
	return plaintext, nil
}

// names reports whether the context is addressed to this key pair: its
// recipient thumbprint and generation are this key's.
func (k *keyPair) names(expected transportContext) bool {
	return k != nil && k.private != nil && expected.RecipientKeyThumbprint == k.thumbprint && expected.KeyGeneration == k.generation
}

// envelopeParts are the five compact-JWE segments, the header kept encoded
// because it is the GCM additional authenticated data.
type envelopeParts struct {
	header                 string
	wrapped, iv, body, tag []byte
}

func splitEnvelope(envelope string) (*envelopeParts, bool) {
	if len(envelope) == 0 || len(envelope) > maxEnvelopeBytes || !envelopePattern.MatchString(envelope) {
		return nil, false
	}
	segments := strings.Split(envelope, ".")
	if len(segments) != 5 {
		return nil, false
	}
	wrapped, errK := b64.DecodeString(segments[1])
	iv, errI := b64.DecodeString(segments[2])
	body, errC := b64.DecodeString(segments[3])
	tag, errT := b64.DecodeString(segments[4])
	if errK != nil || errI != nil || errC != nil || errT != nil ||
		len(wrapped) != 40 || len(iv) != 12 || len(tag) != 16 || len(body) == 0 || len(body) > maxValueBytes {
		return nil, false
	}
	return &envelopeParts{header: segments[0], wrapped: wrapped, iv: iv, body: body, tag: tag}, true
}

// decrypt performs the key agreement, the key unwrap and the content
// decryption of an envelope whose header has already been checked.
func (k *keyPair) decrypt(parts *envelopeParts, header *protectedHeader) ([]byte, error) {
	ephemeral, err := header.Epk.key()
	if err != nil {
		return nil, err
	}
	shared, err := k.private.ECDH(ephemeral)
	if err != nil {
		return nil, errTransport()
	}
	kek := deriveKEK(shared)
	defer clear(shared)
	defer clear(kek)
	kekBlock, err := aes.NewCipher(kek)
	if err != nil {
		return nil, errTransport()
	}
	cek, ok := aesKeyUnwrap(kekBlock, parts.wrapped)
	if !ok || len(cek) != 32 {
		return nil, errTransport()
	}
	defer clear(cek)
	contentBlock, err := aes.NewCipher(cek)
	if err != nil {
		return nil, errTransport()
	}
	gcm, err := cipher.NewGCM(contentBlock)
	if err != nil {
		return nil, errTransport()
	}
	plaintext, err := gcm.Open(nil, parts.iv, append(parts.body, parts.tag...), []byte(parts.header))
	if err != nil || len(plaintext) == 0 || len(plaintext) > maxValueBytes {
		clear(plaintext)
		return nil, errTransport()
	}
	return plaintext, nil
}

// checkHeader enforces the transport's header rules before any decryption:
// exactly the members alg, enc, typ, kid, epk, crit and wallet_ctx, each once;
// the pinned algorithms and type; this key id; crit naming only wallet_ctx;
// and wallet_ctx equal to the expected context. It returns the parsed header.
func checkHeader(segment, keyID string, expectedContext []byte) (*protectedHeader, error) {
	raw, err := b64.DecodeString(segment)
	if err != nil {
		return nil, errTransport()
	}
	members, ok := headerMembers(raw)
	if !ok {
		return nil, errTransport()
	}
	var header protectedHeader
	text := func(name string, into *string) bool {
		return strictJSON(members[name], into)
	}
	if !text("alg", &header.Alg) || !text("enc", &header.Enc) || !text("typ", &header.Typ) || !text("kid", &header.Kid) ||
		!text(criticalContext, &header.WalletCtx) || !strictJSON(members["crit"], &header.Crit) || !strictJSON(members["epk"], &header.Epk) {
		return nil, errTransport()
	}
	if !header.pinned(keyID) || header.WalletCtx != base64.RawURLEncoding.EncodeToString(expectedContext) {
		return nil, errTransport()
	}
	return &header, nil
}

// pinned reports whether the header carries the transport's algorithms and
// type, this key id and a critical list naming only wallet_ctx.
func (h protectedHeader) pinned(keyID string) bool {
	return h.Alg == keyAlgorithm && h.Enc == contentEncryption && h.Typ == transportType && h.Kid == keyID &&
		len(h.Crit) == 1 && h.Crit[0] == criticalContext
}

// headerMembers splits a JSON object into its members, refusing a name the
// transport does not define, a repeated name, a missing member and trailing
// data. Names are matched exactly (case-sensitively).
func headerMembers(raw []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	members := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, isString := token.(string)
		if err != nil || !isString {
			return nil, false
		}
		switch name {
		case "alg", "enc", "typ", "kid", "epk", "crit", criticalContext:
		default:
			return nil, false
		}
		if _, repeated := members[name]; repeated {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		members[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return members, len(members) == 7
}

// strictJSON decodes one JSON value into target, refusing unknown members and
// trailing data.
func strictJSON(raw json.RawMessage, target any) bool {
	if len(raw) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
