// Package walletfake is a test double of the Gateway's /v1/wallet/service/*
// edge and the Wallet Store behind it, for the tests of every package that
// talks to the kombify Wallet through kombifywallet.
//
// It checks on the wire what the real edge checks: the product's own service
// token for the Wallet audience speaking for the owner the body names, the
// service's right on the item (or the partition's create relation), and it
// opens a capture against the transport context the store builds itself and
// seals every delivery with the same JWE transport (ECDH-ES+A256KW, A256GCM,
// critical wallet_ctx header) the shared client opens. Tests therefore
// exercise the real client against sealed bytes, not against a mock of it.
//
// The transport primitives mirror kombify-go-common walletservice/transport.go
// and the Wallet Store's transport.mjs. They live here only because that
// package keeps them unexported. Production code never imports this package.
package walletfake

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
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/gocommon/walletservice"
	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

const (
	// ServiceSecret is Techstack's own Gateway secret in the fake.
	ServiceSecret = "synthetic-techstack-wallet-service-secret-0123456789"
	// SharedSecret stands for the platform's shared SERVICE_AUTH_SECRET, which
	// never admits Wallet routes.
	SharedSecret = "synthetic-shared-platform-secret-never-for-wallet-9876543210"
	// RecipientGeneration is the generation the fake pins for the service key.
	RecipientGeneration = "2"

	captureKeyID      = "wallet-capture-1"
	captureGeneration = "7"
	storeAudience     = "urn:kombify:wallet-store"
	pathPrefix        = "/v1/wallet/service"
	base32Alphabet    = "abcdefghijklmnopqrstuvwxyz234567"
)

// Item is one Wallet item as the fake holds it.
type Item struct {
	ID          string
	Owner       string
	Type        string
	DisplayName string
	Description string
	Name        string
	Scope       string
	// Versions are the stored values, oldest first.
	Versions [][]byte
	// Rights are what Techstack holds on the item: use, read, manage.
	Rights []string
}

// Value is the current value.
func (i Item) Value() []byte { return i.Versions[len(i.Versions)-1] }

// Request is one authenticated call the fake served.
type Request struct {
	Route string
	Body  []byte
}

// Fake is the Gateway and Wallet Store double.
type Fake struct {
	t       testing.TB
	server  *httptest.Server
	capture *ecdh.PrivateKey
	// recipientJWK is the private recipient JWK Techstack is configured with.
	recipientJWK    string
	recipientPublic walletservice.PublicJWK

	mu        sync.Mutex
	items     map[string]*Item
	order     []string
	canCreate map[string]bool
	outage    bool
	requests  []Request
}

// New starts the fake. It is stopped with the test.
func New(t testing.TB) *Fake {
	t.Helper()
	capture, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, public, err := walletservice.GenerateRecipientKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{t: t, capture: capture, recipientJWK: private, recipientPublic: public, items: map[string]*Item{}, canCreate: map[string]bool{}}
	f.server = httptest.NewServer(f)
	t.Cleanup(f.server.Close)
	return f
}

// URL is the fake Gateway origin (a loopback IP, which the client admits over http).
func (f *Fake) URL() string { return f.server.URL }

// Env is a complete TECHSTACK_WALLET_* environment pointing at the fake, for
// Boot.
func (f *Fake) Env() map[string]string {
	return map[string]string{
		kombifywallet.EnvSource:               string(kombifywallet.SourceKombify),
		kombifywallet.EnvServiceURL:           f.server.URL,
		kombifywallet.EnvServiceSecret:        ServiceSecret,
		kombifywallet.EnvRecipientKey:         f.recipientJWK,
		kombifywallet.EnvRecipientGeneration:  RecipientGeneration,
		kombifywallet.EnvCaptureKeyThumbprint: f.captureThumbprint(),
	}
}

// Client is a kombifywallet.Client booted from Env, the way production boots it.
func (f *Fake) Client() *kombifywallet.Client {
	f.t.Helper()
	env := f.Env()
	client, _, err := kombifywallet.Boot(config.ModeSaaS, func(name string) string { return env[name] })
	if err != nil {
		f.t.Fatalf("boot kombify wallet client: %v", err)
	}
	return client
}

// Grant stores an item the owner assigned to Techstack with the given rights.
func (f *Fake) Grant(owner, itemType, displayName, scope string, value []byte, rights ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(&Item{Owner: owner, Type: itemType, DisplayName: displayName, Scope: scope, Versions: [][]byte{slices.Clone(value)}, Rights: rights})
}

// GrantDescribed is Grant for an item that also carries a description, such as
// one a migration run created before it was interrupted.
func (f *Fake) GrantDescribed(owner, itemType, displayName, description, scope string, value []byte, rights ...string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(&Item{Owner: owner, Type: itemType, DisplayName: displayName, Description: description, Scope: scope, Versions: [][]byte{slices.Clone(value)}, Rights: rights})
}

// AllowCreate gives Techstack the owner's partition create relation.
func (f *Fake) AllowCreate(owner string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canCreate[owner] = true
}

// Outage makes every authenticated call answer 503.
func (f *Fake) Outage(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outage = down
}

// Items returns a snapshot of the owner's items.
func (f *Fake) Items(owner string) []Item {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Item
	for _, id := range f.order {
		if item := f.items[id]; item.Owner == owner {
			copied := *item
			copied.Versions = slices.Clone(item.Versions)
			out = append(out, copied)
		}
	}
	return out
}

// Item returns one item, and whether it exists.
func (f *Fake) Item(id string) (Item, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[id]
	if !ok {
		return Item{}, false
	}
	copied := *item
	copied.Versions = slices.Clone(item.Versions)
	return copied, true
}

// Requests returns every authenticated call served so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *Fake) addLocked(item *Item) string {
	random := make([]byte, 26)
	_, _ = rand.Read(random)
	var id strings.Builder
	id.WriteString("itm_")
	for _, b := range random {
		id.WriteByte(base32Alphabet[int(b)%32])
	}
	item.ID = id.String()
	f.items[item.ID] = item
	f.order = append(f.order, item.ID)
	return item.ID
}

func (f *Fake) captureThumbprint() string { return jwkOf(f.capture.PublicKey()).Thumbprint() }

func (f *Fake) reply(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *Fake) deny(w http.ResponseWriter, status int, reason string, extra map[string]any) {
	body := map[string]any{
		"error_code": "WALLET_OPERATION_DENIED", "reason_code": reason, "retryable": status >= 500,
		"user_guidance": "Synthetic guidance for " + reason + ".",
	}
	for name, value := range extra {
		body[name] = value
	}
	f.reply(w, status, body)
}

// ServeHTTP implements the edge.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	payload, _ := io.ReadAll(r.Body)
	route := strings.TrimPrefix(r.URL.Path, pathPrefix)
	claims, err := servicecall.VerifyToken(r.Header.Get(servicecall.HeaderServiceAuth), ServiceSecret, "")
	if r.Method != http.MethodPost || err != nil || claims.Svc != kombifywallet.ServiceID || claims.Aud != "kombify-wallet" || claims.Exp-claims.Iat > 60 {
		f.deny(w, http.StatusUnauthorized, "wallet_service_auth_required", nil)
		return
	}
	var body map[string]json.RawMessage
	var owner string
	if json.Unmarshal(payload, &body) != nil || json.Unmarshal(body["owner"], &owner) != nil || owner == "" {
		f.deny(w, http.StatusBadRequest, "request_invalid", nil)
		return
	}
	delete(body, "owner")
	if claims.OnBehalfOf == nil || claims.OnBehalfOf.Sub != owner {
		f.deny(w, http.StatusForbidden, "wallet_service_owner_mismatch", nil)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{Route: route, Body: payload})
	if f.outage {
		f.deny(w, http.StatusServiceUnavailable, "wallet_store_unavailable", nil)
		return
	}
	switch {
	case route == "/items/list":
		f.list(w, owner)
	case route == "/capture-key":
		f.captureKey(w, owner, body)
	case route == "/items":
		f.create(w, owner, body)
	case strings.HasPrefix(route, "/items/"):
		parts := strings.Split(strings.TrimPrefix(route, "/items/"), "/")
		if len(parts) != 2 {
			f.deny(w, http.StatusNotFound, "not_found", nil)
			return
		}
		f.onItem(w, owner, parts[0], parts[1], body)
	default:
		f.deny(w, http.StatusNotFound, "not_found", nil)
	}
}

func (f *Fake) list(w http.ResponseWriter, owner string) {
	items := []map[string]any{}
	for _, id := range f.order {
		item := f.items[id]
		if item.Owner != owner || len(item.Rights) == 0 {
			continue
		}
		entry := map[string]any{
			"itemId": item.ID, "itemType": item.Type, "displayName": item.DisplayName, "scope": item.Scope,
			"policy": map[string]any{"valueAccess": "read_right_holders"}, "state": "active",
			"currentVersion": strconv.Itoa(len(item.Versions)), "rights": item.Rights,
		}
		if item.Name != "" {
			entry["name"] = item.Name
		}
		if item.Description != "" {
			entry["description"] = item.Description
		}
		items = append(items, entry)
	}
	f.reply(w, http.StatusOK, map[string]any{"items": items})
}

func (f *Fake) captureKey(w http.ResponseWriter, owner string, body map[string]json.RawMessage) {
	var itemID string
	if raw, keyed := body["itemId"]; keyed {
		_ = json.Unmarshal(raw, &itemID)
		item := f.items[itemID]
		if item == nil || item.Owner != owner || !slices.Contains(item.Rights, "manage") {
			f.deny(w, http.StatusForbidden, "wallet_right_missing", map[string]any{"required_right": "manage", "item_id": itemID})
			return
		}
	} else if !f.canCreate[owner] {
		f.deny(w, http.StatusForbidden, "wallet_right_missing", map[string]any{"required_right": "create"})
		return
	}
	public := jwkOf(f.capture.PublicKey())
	f.reply(w, http.StatusOK, map[string]any{
		"format": "kombify.wallet-capture-key.v1", "kid": captureKeyID, "keyGeneration": captureGeneration, "publicKey": public,
		"thumbprint": public.Thumbprint(), "audience": storeAudience, "custodyNamespace": "personal", "vaultRef": "personal",
	})
}

func (f *Fake) openCapture(owner, operationID, version string, raw json.RawMessage) ([]byte, bool) {
	var capture struct {
		JWE       string `json:"jwe"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if json.Unmarshal(raw, &capture) != nil {
		return nil, false
	}
	ctx := contextOf(owner, version, "capture", operationID, storeAudience, f.captureThumbprint(), captureGeneration, capture.ExpiresAt)
	value, ok := openEnvelope(capture.JWE, f.capture, ctx)
	return value, ok
}

func (f *Fake) create(w http.ResponseWriter, owner string, body map[string]json.RawMessage) {
	if !f.canCreate[owner] {
		f.deny(w, http.StatusForbidden, "wallet_right_missing", map[string]any{"required_right": "create"})
		return
	}
	var itemType, operationID string
	var metadata struct {
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Name        string `json:"name"`
	}
	if json.Unmarshal(body["itemType"], &itemType) != nil || json.Unmarshal(body["operationId"], &operationID) != nil || json.Unmarshal(body["metadata"], &metadata) != nil {
		f.deny(w, http.StatusBadRequest, "request_invalid", nil)
		return
	}
	value, ok := f.openCapture(owner, operationID, "1", body["capture"])
	if !ok {
		f.deny(w, http.StatusConflict, "capture_invalid", nil)
		return
	}
	id := f.addLocked(&Item{
		Owner: owner, Type: itemType, DisplayName: metadata.DisplayName, Description: metadata.Description, Name: metadata.Name, Scope: metadata.Scope,
		Versions: [][]byte{value}, Rights: []string{"use", "read", "manage"},
	})
	f.reply(w, http.StatusCreated, map[string]any{"operationId": operationID, "itemId": id, "version": "1", "state": "active", "auditPending": false})
}

func (f *Fake) onItem(w http.ResponseWriter, owner, id, action string, body map[string]json.RawMessage) {
	need := map[string]string{"deliver": "use", "versions": "manage"}[action]
	item := f.items[id]
	switch {
	case need == "":
		f.deny(w, http.StatusNotFound, "not_found", nil)
	case item == nil || item.Owner != owner:
		f.deny(w, http.StatusNotFound, "item_unknown", nil)
	case !slices.Contains(item.Rights, need):
		f.deny(w, http.StatusForbidden, "wallet_right_missing", map[string]any{"required_right": need, "item_id": id})
	case action == "deliver":
		f.deliver(w, owner, item, body)
	default:
		f.addVersion(w, owner, item, body)
	}
}

func (f *Fake) addVersion(w http.ResponseWriter, owner string, item *Item, body map[string]json.RawMessage) {
	var operationID string
	_ = json.Unmarshal(body["operationId"], &operationID)
	next := strconv.Itoa(len(item.Versions) + 1)
	value, ok := f.openCapture(owner, operationID, next, body["capture"])
	if !ok {
		f.deny(w, http.StatusConflict, "capture_invalid", nil)
		return
	}
	item.Versions = append(item.Versions, value)
	f.reply(w, http.StatusOK, map[string]any{"operationId": operationID, "itemId": item.ID, "version": next, "state": "active", "auditPending": false})
}

func (f *Fake) deliver(w http.ResponseWriter, owner string, item *Item, body map[string]json.RawMessage) {
	var expiresAt int64
	_ = json.Unmarshal(body["expiresAt"], &expiresAt)
	version := strconv.Itoa(len(item.Versions))
	ctx := contextOf(owner, version, "process", uuid.NewString(), "urn:kombify:wallet:service:"+kombifywallet.ServiceID,
		f.recipientPublic.Thumbprint(), RecipientGeneration, expiresAt)
	envelope, err := sealEnvelope(item.Value(), f.recipientPublic, "service:"+kombifywallet.ServiceID, ctx)
	if err != nil {
		f.deny(w, http.StatusInternalServerError, "wallet_store_unavailable", nil)
		return
	}
	f.reply(w, http.StatusOK, map[string]any{
		"operationId": ctx.OperationID, "itemId": item.ID, "version": version, "state": "active", "auditPending": false,
		"jwe": envelope, "context": ctx,
	})
}

// transportContext is the authenticated context; field order is the canonical
// encoding order of the Wallet transport.
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
	ExpiresAt              int64  `json:"expiresAt"`
}

func contextOf(owner, version, operation, operationID, audience, thumbprint, generation string, expiresAt int64) transportContext {
	return transportContext{
		CustodyNamespace: "personal", OwnerSubject: owner, VaultRef: "personal", ItemVersion: version, KeyGeneration: generation,
		Operation: operation, OperationID: operationID, Audience: audience, RecipientKeyThumbprint: thumbprint, ExpiresAt: expiresAt,
	}
}

// canonical is JSON.stringify of the ordered context: no whitespace, only the
// quote and backslash escaped.
func (c transportContext) canonical() []byte {
	var out strings.Builder
	out.WriteByte('{')
	for _, field := range []struct{ name, value string }{
		{"custodyNamespace", c.CustodyNamespace}, {"ownerSubject", c.OwnerSubject}, {"vaultRef", c.VaultRef},
		{"itemVersion", c.ItemVersion}, {"keyGeneration", c.KeyGeneration}, {"operation", c.Operation},
		{"operationId", c.OperationID}, {"audience", c.Audience}, {"recipientKeyThumbprint", c.RecipientKeyThumbprint},
	} {
		out.WriteString(`"` + field.name + `":"`)
		for _, r := range field.value {
			if r == '"' || r == '\\' {
				out.WriteByte('\\')
			}
			out.WriteRune(r)
		}
		out.WriteString(`",`)
	}
	out.WriteString(`"expiresAt":` + strconv.FormatInt(c.ExpiresAt, 10) + `}`)
	return []byte(out.String())
}

func jwkOf(key *ecdh.PublicKey) walletservice.PublicJWK {
	raw := key.Bytes()
	return walletservice.PublicJWK{
		Kty: "EC", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString(raw[1:33]),
		Y: base64.RawURLEncoding.EncodeToString(raw[33:65]),
	}
}

func publicKeyOf(jwk walletservice.PublicJWK) (*ecdh.PublicKey, bool) {
	x, errX := base64.RawURLEncoding.DecodeString(jwk.X)
	y, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, false
	}
	key, err := ecdh.P256().NewPublicKey(append(append([]byte{0x04}, x...), y...))
	return key, err == nil
}

// kek is the ECDH-ES key agreement result run through the RFC 7518 section 4.6
// Concat KDF for a 256-bit A256KW key, with no apu and no apv.
func kek(shared []byte) []byte {
	prefixed := func(data []byte) []byte {
		out := make([]byte, 4+len(data))
		binary.BigEndian.PutUint32(out, uint32(len(data)))
		copy(out[4:], data)
		return out
	}
	hash := sha256.New()
	hash.Write([]byte{0, 0, 0, 1})
	hash.Write(shared)
	hash.Write(prefixed([]byte("ECDH-ES+A256KW")))
	hash.Write(prefixed(nil))
	hash.Write(prefixed(nil))
	hash.Write([]byte{0, 0, 1, 0})
	return hash.Sum(nil)
}

var wrapIV = [8]byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

func keyWrap(block cipher.Block, plain []byte) []byte {
	n := len(plain) / 8
	a := wrapIV
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

func keyUnwrap(block cipher.Block, wrapped []byte) ([]byte, bool) {
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
	return r, subtle.ConstantTimeCompare(a[:], wrapIV[:]) == 1
}

type jweHeader struct {
	Alg       string                  `json:"alg"`
	Enc       string                  `json:"enc"`
	Typ       string                  `json:"typ"`
	Kid       string                  `json:"kid"`
	Epk       walletservice.PublicJWK `json:"epk"`
	Crit      []string                `json:"crit"`
	WalletCtx string                  `json:"wallet_ctx"`
}

func sealEnvelope(value []byte, recipient walletservice.PublicJWK, keyID string, ctx transportContext) (string, error) {
	recipientKey, ok := publicKeyOf(recipient)
	if !ok {
		return "", io.ErrUnexpectedEOF
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	shared, err := ephemeral.ECDH(recipientKey)
	if err != nil {
		return "", err
	}
	kekBlock, err := aes.NewCipher(kek(shared))
	if err != nil {
		return "", err
	}
	cek := make([]byte, 32)
	if _, err = rand.Read(cek); err != nil {
		return "", err
	}
	header, err := json.Marshal(jweHeader{
		Alg: "ECDH-ES+A256KW", Enc: "A256GCM", Typ: "kombify.wallet-transport.v1", Kid: keyID, Epk: jwkOf(ephemeral.PublicKey()),
		Crit: []string{"wallet_ctx"}, WalletCtx: base64.RawURLEncoding.EncodeToString(ctx.canonical()),
	})
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	contentBlock, err := aes.NewCipher(cek)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(contentBlock)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, value, []byte(encodedHeader))
	split := len(sealed) - gcm.Overhead()
	enc := base64.RawURLEncoding.EncodeToString
	return strings.Join([]string{encodedHeader, enc(keyWrap(kekBlock, cek)), enc(iv), enc(sealed[:split]), enc(sealed[split:])}, "."), nil
}

// openEnvelope decrypts an envelope sealed to private, and only when its
// authenticated header names exactly the expected context.
func openEnvelope(envelope string, private *ecdh.PrivateKey, expected transportContext) ([]byte, bool) {
	parts := strings.Split(envelope, ".")
	if len(parts) != 5 {
		return nil, false
	}
	decoded := make([][]byte, 5)
	for index, part := range parts {
		raw, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return nil, false
		}
		decoded[index] = raw
	}
	var header jweHeader
	if json.Unmarshal(decoded[0], &header) != nil || header.WalletCtx != base64.RawURLEncoding.EncodeToString(expected.canonical()) {
		return nil, false
	}
	ephemeral, ok := publicKeyOf(header.Epk)
	if !ok {
		return nil, false
	}
	shared, err := private.ECDH(ephemeral)
	if err != nil {
		return nil, false
	}
	kekBlock, err := aes.NewCipher(kek(shared))
	if err != nil {
		return nil, false
	}
	cek, ok := keyUnwrap(kekBlock, decoded[1])
	if !ok {
		return nil, false
	}
	contentBlock, err := aes.NewCipher(cek)
	if err != nil {
		return nil, false
	}
	gcm, err := cipher.NewGCM(contentBlock)
	if err != nil {
		return nil, false
	}
	plain, err := gcm.Open(nil, decoded[2], append(bytes.Clone(decoded[3]), decoded[4]...), []byte(parts[0]))
	return plain, err == nil
}
