package walletservice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Item types the Wallet admits at create.
const (
	ItemTypeGenericSecret     = "generic_secret"
	ItemTypeLogin             = "login"
	ItemTypeVariable          = "variable"
	ItemTypeConfiguration     = "configuration"
	ItemTypeNodeAccessProfile = "node_access_profile"
	ItemTypeParentCredential  = "parent_credential" // #nosec G101 -- an item type name, not a credential.
	ItemTypeSSHCA             = "ssh_ca"
	ItemTypeAICredential      = "ai_credential" // #nosec G101 -- an item type name, not a credential.
)

const (
	captureKeyFormat = "kombify.wallet-capture-key.v1"
	generateSSHCA    = "ssh-ed25519"
)

var (
	itemTypes = map[string]bool{
		ItemTypeGenericSecret: true, ItemTypeLogin: true, ItemTypeVariable: true, ItemTypeConfiguration: true,
		ItemTypeNodeAccessProfile: true, ItemTypeParentCredential: true, ItemTypeSSHCA: true, ItemTypeAICredential: true,
	}
	namePatterns = map[string]*regexp.Regexp{
		ItemTypeVariable:      regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`),
		ItemTypeConfiguration: regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`),
	}
	rightsPattern     = regexp.MustCompile(`^[a-z_]{1,32}$`)
	headerNamePattern = regexp.MustCompile("^[a-z0-9!#$%&'*+.^_`|~-]{1,128}$")
	headerValue       = regexp.MustCompile(`^[\t\x20-\x7e]*$`)
	serialPattern     = regexp.MustCompile(`^[0-9]{1,20}$`)
	brokerMethods     = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
)

// ItemPolicy is an item's value policy. `use_only` means nobody receives the
// value, the owner included; it is only used through a brokered call.
type ItemPolicy struct {
	ValueAccess string `json:"valueAccess"`
	OwnerProof  string `json:"ownerProof,omitempty"`
}

// UseTarget is one place a brokered call with the item may go. The Wallet owns
// the grammar and refuses a target it does not admit.
type UseTarget struct {
	Host       string    `json:"host"`
	Methods    []string  `json:"methods"`
	PathPrefix string    `json:"pathPrefix"`
	Inject     Injection `json:"inject"`
}

// Injection says how the Wallet puts the credential into a brokered call:
// kind `bearer`, `basic` (logins) or `header` (with Name).
type Injection struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// Item is one Wallet item the owner assigned to this service. It carries
// display metadata, the item's policy and this service's rights, never a value.
type Item struct {
	ItemID      string `json:"itemId"`
	ItemType    string `json:"itemType"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Name        string `json:"name"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	// CurrentVersion is the latest stored version (a decimal), empty while there is none.
	CurrentVersion string `json:"currentVersion"`
	// Rights are the Core right names (`use`, `read`, `manage`) this service holds.
	Rights     []string        `json:"rights"`
	Policy     ItemPolicy      `json:"policy"`
	UseTargets []UseTarget     `json:"useTargets"`
	MintPolicy json.RawMessage `json:"mintPolicy"`
	SignPolicy json.RawMessage `json:"signPolicy"`
	// CAPublicKey is an SSH CA's public `ssh-ed25519 <base64>` line, which
	// StackKits installs as a trusted user CA. It is not secret.
	CAPublicKey string `json:"caPublicKey"`
}

// List returns the owner's items that this service holds a right on. The
// Wallet never lists a value.
func (c *Client) List(ctx context.Context, owner string) ([]Item, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	raw, status, err := c.post(ctx, owner, "/items/list", nil, false)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Items []Item `json:"items"`
	}
	if status != 200 || json.Unmarshal(raw, &answer) != nil || answer.Items == nil {
		return nil, errResponse(status, "list")
	}
	for _, item := range answer.Items {
		if !itemIDPattern.MatchString(item.ItemID) || (item.CurrentVersion != "" && !decimalPattern.MatchString(item.CurrentVersion)) {
			return nil, errResponse(status, "list")
		}
		for _, right := range item.Rights {
			if !rightsPattern.MatchString(right) {
				return nil, errResponse(status, "list")
			}
		}
	}
	return answer.Items, nil
}

// CaptureKey is the Wallet Store's public key for sealing a value to it.
type CaptureKey struct {
	KeyID      string
	Generation string
	PublicKey  PublicJWK
	Thumbprint string
}

// CaptureKey fetches the store's capture key for the owner's partition (the
// service needs the partition's create relation), or with an itemID for that
// item (the service needs the `manage` right on it). The statement is checked
// against the key itself and the expected audience and namespace, the
// thumbprint is recomputed from the key, and a configured pin is enforced.
func (c *Client) CaptureKey(ctx context.Context, owner, itemID string) (*CaptureKey, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	fields := map[string]any(nil)
	if itemID != "" {
		if !itemIDPattern.MatchString(itemID) {
			return nil, errInvalid("item_id")
		}
		fields = map[string]any{"itemId": itemID}
	}
	raw, status, err := c.post(ctx, owner, "/capture-key", fields, false)
	if err != nil {
		return nil, err
	}
	key, ok := parseCaptureKey(raw)
	if status != 200 || !ok {
		return nil, errResponse(status, "capture_key")
	}
	if c.pin != "" && c.pin != key.Thumbprint {
		return nil, newError(CodeTransportInvalid, 0, "capture_key_pin", false)
	}
	return key, nil
}

// parseCaptureKey checks the store's key statement against the key itself and
// the expected format, audience and namespace. The thumbprint is recomputed
// from the key, never taken on trust.
func parseCaptureKey(raw []byte) (*CaptureKey, bool) {
	var statement struct {
		Format           string    `json:"format"`
		Kid              string    `json:"kid"`
		KeyGeneration    string    `json:"keyGeneration"`
		PublicKey        PublicJWK `json:"publicKey"`
		Thumbprint       string    `json:"thumbprint"`
		Audience         string    `json:"audience"`
		CustodyNamespace string    `json:"custodyNamespace"`
		VaultRef         string    `json:"vaultRef"`
	}
	if json.Unmarshal(raw, &statement) != nil || statement.Format != captureKeyFormat || statement.Audience != storeAudience ||
		statement.CustodyNamespace != namespacePersonal || statement.VaultRef != vaultPersonal ||
		!keyIDPattern.MatchString(statement.Kid) || !decimalPattern.MatchString(statement.KeyGeneration) {
		return nil, false
	}
	if _, err := statement.PublicKey.key(); err != nil || statement.PublicKey.Thumbprint() != statement.Thumbprint {
		return nil, false
	}
	return &CaptureKey{KeyID: statement.Kid, Generation: statement.KeyGeneration, PublicKey: statement.PublicKey, Thumbprint: statement.Thumbprint}, true
}

// Receipt is the Wallet's receipt for a new item or a new version. It carries
// no value.
type Receipt struct {
	OperationID  string
	ItemID       string
	Version      string
	State        string
	AuditPending bool
}

// CreateInput describes one item to create for an owner. Supply Value to
// capture a value, or for an ssh_ca item Generate to have the Wallet make the
// key inside custody (the private key then never exists outside it).
type CreateInput struct {
	Owner       string
	ItemType    string // one of the ItemType constants
	DisplayName string
	Description string
	// Name is required for variable and configuration items and refused otherwise.
	Name  string
	Scope string // account, project:<p> or environment:<p>/<e>
	// Value is sealed to the store's capture key. The caller owns the slice and
	// wipes it.
	Value []byte
	// Generate is "ssh-ed25519" to have the Wallet generate an ssh_ca key; Value
	// must then be empty. The CA's public key is in List as CAPublicKey.
	Generate string
	// Optional settings, checked by the Wallet against the item's type.
	Policy     *ItemPolicy
	UseTargets []UseTarget
	MintPolicy json.RawMessage // parent_credential only: the owner's bound on minted children
	SignPolicy json.RawMessage // ssh_ca only: the owner's bound on signed certificates
}

// Create stores a new item for the owner. The edge projects this service's `use`
// right on the new item before the Wallet creates it, so an item a service
// created is never unreachable to it. OperationID is the lease jti and is bound
// into the capture, so every call is a new operation.
func (c *Client) Create(ctx context.Context, in CreateInput) (*Receipt, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if reason := validateCreate(in); reason != "" {
		return nil, errInvalid(reason)
	}
	operationID := uuid.NewString()
	body := map[string]any{"itemType": in.ItemType, memberOperationID: operationID, "metadata": createMetadata(in)}
	if in.Policy != nil {
		body["policy"] = in.Policy
	}
	if len(in.UseTargets) > 0 {
		body["useTargets"] = in.UseTargets
	}
	if len(in.MintPolicy) > 0 {
		body["mintPolicy"] = in.MintPolicy
	}
	if len(in.SignPolicy) > 0 {
		body["signPolicy"] = in.SignPolicy
	}
	if in.Generate != "" {
		body["generate"] = map[string]any{"algorithm": in.Generate}
	} else {
		sealed, expiresAt, err := c.sealCapture(ctx, in.Owner, "", "1", operationID, in.Value)
		if err != nil {
			return nil, err
		}
		body["capture"] = map[string]any{"jwe": sealed, memberExpiresAt: expiresAt}
	}
	raw, status, err := c.post(ctx, in.Owner, "/items", body, false)
	if err != nil {
		return nil, err
	}
	receipt, ok := parseReceipt(raw)
	if (status != 200 && status != 201) || !ok || receipt.OperationID != operationID || receipt.Version != "1" {
		return nil, errResponse(status, "create")
	}
	return receipt, nil
}

func validateCreate(in CreateInput) string {
	if reason := validateMetadata(in); reason != "" {
		return reason
	}
	if in.Generate != "" {
		if in.ItemType != ItemTypeSSHCA || in.Generate != generateSSHCA || len(in.Value) != 0 {
			return "generate"
		}
	} else if len(in.Value) == 0 || len(in.Value) > maxValueBytes {
		return "value"
	}
	if in.Policy != nil && in.Policy.ValueAccess != "read_right_holders" && in.Policy.ValueAccess != "use_only" {
		return "policy"
	}
	if len(in.UseTargets) > 16 || !jsonObject(in.MintPolicy, 16_384) || !jsonObject(in.SignPolicy, 16_384) {
		return "settings"
	}
	return ""
}

// validateMetadata mirrors the Wallet's display grammar for a new item.
func validateMetadata(in CreateInput) string {
	switch {
	case !eligibleOwner(in.Owner):
		return "owner"
	case !itemTypes[in.ItemType]:
		return "item_type"
	case !scopePattern.MatchString(in.Scope):
		return "scope"
	case !displayText(in.DisplayName, 120, false):
		return "display_name"
	case in.Description != "" && !displayText(in.Description, 500, true):
		return "description"
	}
	if pattern, named := namePatterns[in.ItemType]; named {
		if !pattern.MatchString(in.Name) {
			return "name"
		}
	} else if in.Name != "" {
		return "name"
	}
	return ""
}

// jsonObject accepts an absent value or a JSON object of at most limit bytes.
func jsonObject(raw json.RawMessage, limit int) bool {
	if len(raw) == 0 {
		return true
	}
	trimmed := bytes.TrimSpace(raw)
	return len(raw) <= limit && json.Valid(raw) && len(trimmed) > 0 && trimmed[0] == '{'
}

func createMetadata(in CreateInput) map[string]any {
	metadata := map[string]any{"displayName": in.DisplayName, "scope": in.Scope}
	if in.Description != "" {
		metadata["description"] = in.Description
	}
	if in.Name != "" {
		metadata["name"] = in.Name
	}
	return metadata
}

// sealCapture fetches the capture key and seals value to it, bound to this
// owner, the item version the store will build, and the operation id.
func (c *Client) sealCapture(ctx context.Context, owner, itemID, version, operationID string, value []byte) (string, int64, error) {
	key, err := c.CaptureKey(ctx, owner, itemID)
	if err != nil {
		return "", 0, err
	}
	expiresAt := c.now().Add(captureLifetime).UnixMilli()
	sealed, err := seal(value, key.PublicKey, key.KeyID, transportContext{
		CustodyNamespace: namespacePersonal, OwnerSubject: owner, VaultRef: vaultPersonal, ItemVersion: version,
		KeyGeneration: key.Generation, Operation: opCapture, OperationID: operationID, Audience: storeAudience,
		RecipientKeyThumbprint: key.Thumbprint, ExpiresAt: expiresAt,
	}, c.now())
	if err != nil {
		return "", 0, newError(CodeTransportInvalid, 0, "capture_seal", false)
	}
	return sealed, expiresAt, nil
}

func parseReceipt(raw []byte) (*Receipt, bool) {
	var answer struct {
		OperationID  string `json:"operationId"`
		ItemID       string `json:"itemId"`
		Version      string `json:"version"`
		State        string `json:"state"`
		AuditPending bool   `json:"auditPending"`
	}
	if json.Unmarshal(raw, &answer) != nil || !operationIDPattern.MatchString(answer.OperationID) ||
		!itemIDPattern.MatchString(answer.ItemID) || !decimalPattern.MatchString(answer.Version) {
		return nil, false
	}
	return &Receipt{OperationID: answer.OperationID, ItemID: answer.ItemID, Version: answer.Version, State: answer.State, AuditPending: answer.AuditPending}, true
}

// VersionInput is a new value for an existing item, for example a rotated
// refresh token the product obtained.
type VersionInput struct {
	Owner  string
	ItemID string
	// BasedOn is the version the product read (Delivered.Version, or
	// Item.CurrentVersion): the capture is bound to the next version, so a value
	// stored after another writer rotated the item is refused by the Wallet.
	BasedOn string
	// Value is the new value. The caller wipes it.
	Value []byte
}

// AddVersion stores a new version of an item the service holds the `manage`
// right on. The value is sealed to the store's capture key like an owner
// capture.
func (c *Client) AddVersion(ctx context.Context, in VersionInput) (*Receipt, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	based, parseErr := strconv.ParseUint(in.BasedOn, 10, 64)
	switch {
	case !eligibleOwner(in.Owner):
		return nil, errInvalid("owner")
	case !itemIDPattern.MatchString(in.ItemID):
		return nil, errInvalid("item_id")
	case !decimalPattern.MatchString(in.BasedOn) || parseErr != nil:
		return nil, errInvalid("based_on")
	case len(in.Value) == 0 || len(in.Value) > maxValueBytes:
		return nil, errInvalid("value")
	}
	next := strconv.FormatUint(based+1, 10)
	operationID := uuid.NewString()
	sealed, expiresAt, err := c.sealCapture(ctx, in.Owner, in.ItemID, next, operationID, in.Value)
	if err != nil {
		return nil, err
	}
	raw, status, err := c.post(ctx, in.Owner, "/items/"+in.ItemID+"/versions", map[string]any{
		"capture": map[string]any{"jwe": sealed, memberExpiresAt: expiresAt}, "operationId": operationID,
	}, false)
	if err != nil {
		return nil, err
	}
	receipt, ok := parseReceipt(raw)
	if status != 200 || !ok || receipt.OperationID != operationID || receipt.ItemID != in.ItemID || receipt.Version != next {
		return nil, errResponse(status, "version")
	}
	return receipt, nil
}

// Delivered is one value the Wallet opened for this service. It renders as a
// fixed marker everywhere; the caller wipes it after use.
type Delivered struct {
	ItemID       string
	Version      string
	AuditPending bool
	Secret
}

// sealedAnswer is the shape of a result the Wallet sealed to this service.
type sealedAnswer struct {
	OperationID  string          `json:"operationId"`
	ItemID       string          `json:"itemId"`
	Version      string          `json:"version"`
	AuditPending bool            `json:"auditPending"`
	JWE          string          `json:"jwe"`
	Context      json.RawMessage `json:"context"`
}

// openSealed opens a result sealed to this service. It does so only when the
// whole transport context the Wallet returned equals the one this call
// expects: this owner, the version the answer names, the operation, this
// service's audience, key thumbprint and generation, the operation id the
// answer names and the exact expiry this request asked for. The envelope's own
// authenticated header must say the same (checked before decryption).
func (c *Client) openSealed(raw []byte, status int, owner, itemID, operation string, expiresAt int64) (*sealedAnswer, []byte, error) {
	var answer sealedAnswer
	if status != 200 || json.Unmarshal(raw, &answer) != nil || answer.ItemID != itemID ||
		!operationIDPattern.MatchString(answer.OperationID) || !decimalPattern.MatchString(answer.Version) || answer.JWE == "" {
		return nil, nil, errResponse(status, operation)
	}
	expected := transportContext{
		CustodyNamespace: namespacePersonal, OwnerSubject: owner, VaultRef: vaultPersonal, ItemVersion: answer.Version,
		KeyGeneration: c.recipient.generation, Operation: operation, OperationID: answer.OperationID, Audience: c.audience,
		RecipientKeyThumbprint: c.recipient.thumbprint, ExpiresAt: expiresAt,
	}
	// The result is bound to this owner, this recipient key and generation, this
	// service's audience and this request's expiry. Anything else is a result
	// meant for someone else (or replayed), whatever it decrypts to.
	var returned transportContext
	if !strictJSON(answer.Context, &returned) || returned != expected {
		return nil, nil, errTransport()
	}
	value, err := c.recipient.open(answer.JWE, expected, c.now)
	if err != nil {
		return nil, nil, err
	}
	return &answer, value, nil
}

// Deliver asks the Wallet for the current value of one item, sealed to this
// service's recipient key for one use and recorded as an audited disclosure.
// The Wallet refuses a use-only value, a parent credential, an SSH CA and a
// `secret://` reference; each refusal is an *Error carrying the Gateway's
// reason code. The caller wipes the result.
func (c *Client) Deliver(ctx context.Context, owner, itemID string) (*Delivered, error) {
	if err := c.gateRecipient(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !itemIDPattern.MatchString(itemID) {
		return nil, errInvalid("item_id")
	}
	expiresAt := c.now().Add(deliverLifetime).UnixMilli()
	raw, status, err := c.post(ctx, owner, "/items/"+itemID+"/deliver", map[string]any{memberExpiresAt: expiresAt}, false)
	if err != nil {
		return nil, err
	}
	answer, value, err := c.openSealed(raw, status, owner, itemID, opProcess, expiresAt)
	if err != nil {
		return nil, err
	}
	return &Delivered{ItemID: itemID, Version: answer.Version, AuditPending: answer.AuditPending, Secret: NewSecret(value)}, nil
}

// Minted is a child credential the Wallet minted from a parent credential and
// sealed to this service's key. The parent never leaves custody.
type Minted struct {
	ItemID       string
	Version      string
	AuditPending bool
	// ExpiresAt is when the child credential expires, as sealed by the Wallet.
	ExpiresAt time.Time
	// Details is the provider's description of the child (permissions,
	// repositories, token id); it is not secret.
	Details json.RawMessage
	// Secret is the child credential. The caller wipes it.
	Secret
}

// Mint asks the Wallet to mint a child credential of a `parent_credential`
// item within its mint policy, for example `{"provider":"github_app",
// "repositories":[...],"permissions":{...}}` or `{"provider":"cloudflare",
// "templates":[...],"ttlSeconds":N}`. A service names no key: the child comes
// back sealed to this service's pinned recipient key and opens only under the
// same context checks as Deliver, with operation `mint`.
func (c *Client) Mint(ctx context.Context, owner, itemID string, child map[string]any) (*Minted, error) {
	if err := c.gateRecipient(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !itemIDPattern.MatchString(itemID) {
		return nil, errInvalid("item_id")
	}
	encoded, err := json.Marshal(child)
	if err != nil || len(child) == 0 || len(encoded) > 8_192 {
		return nil, errInvalid("child")
	}
	expiresAt := c.now().Add(deliverLifetime).UnixMilli()
	raw, status, err := c.post(ctx, owner, "/items/"+itemID+"/mint", map[string]any{"child": json.RawMessage(encoded), memberExpiresAt: expiresAt}, false)
	if err != nil {
		return nil, err
	}
	answer, plaintext, err := c.openSealed(raw, status, owner, itemID, opMint, expiresAt)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	var sealed struct {
		Credential json.RawMessage `json:"credential"`
		ExpiresAt  int64           `json:"expiresAt"`
		Details    json.RawMessage `json:"details"`
	}
	if !strictJSON(plaintext, &sealed) || sealed.ExpiresAt <= 0 || !jsonObject(sealed.Details, maxValueBytes) || len(sealed.Details) == 0 {
		clear(sealed.Credential)
		return nil, errTransport()
	}
	credential, ok := jsonStringBytes(sealed.Credential)
	clear(sealed.Credential)
	if !ok || len(credential) == 0 {
		return nil, errTransport()
	}
	return &Minted{
		ItemID: itemID, Version: answer.Version, AuditPending: answer.AuditPending,
		ExpiresAt: time.UnixMilli(sealed.ExpiresAt), Details: sealed.Details, Secret: NewSecret(credential),
	}, nil
}

// jsonStringBytes returns the content of a JSON string as a fresh buffer. A
// string without escapes is copied byte for byte, so no immutable copy of a
// credential is made; an escaped one is decoded the ordinary way.
func jsonStringBytes(raw json.RawMessage) ([]byte, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, false
	}
	inner := raw[1 : len(raw)-1]
	if !bytes.ContainsRune(inner, '\\') {
		return bytes.Clone(inner), true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil, false
	}
	return []byte(text), true
}

// BrokerRequest is one HTTPS call the Wallet makes with an item's credential
// injected inside custody; the service never sees the value.
type BrokerRequest struct {
	Method  string
	URL     string // canonical https URL
	Headers map[string]string
	Body    []byte
}

// BrokerResponse is the upstream answer after the Wallet redacted the value.
type BrokerResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// Broker runs a brokered call. The item's use targets and this service's `use`
// right decide whether the Wallet performs it.
func (c *Client) Broker(ctx context.Context, owner, itemID string, in BrokerRequest) (*BrokerResponse, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !itemIDPattern.MatchString(itemID) {
		return nil, errInvalid("item_id")
	}
	if reason := validateBroker(in); reason != "" {
		return nil, errInvalid(reason)
	}
	body := map[string]any{"method": in.Method, "url": in.URL}
	if len(in.Headers) > 0 {
		body[memberHeaders] = in.Headers
	}
	if len(in.Body) > 0 {
		body["body"] = base64.StdEncoding.EncodeToString(in.Body)
	}
	raw, status, err := c.post(ctx, owner, "/items/"+itemID+"/broker", body, true)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if status != 200 || json.Unmarshal(raw, &answer) != nil || answer.Status < 100 || answer.Status > 599 {
		return nil, errResponse(status, "broker")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(answer.Body)
	if err != nil || len(decoded) > 1<<20 {
		return nil, errResponse(status, "broker_body")
	}
	return &BrokerResponse{Status: answer.Status, Headers: answer.Headers, Body: decoded}, nil
}

func validateBroker(in BrokerRequest) string {
	if !brokerMethods[in.Method] {
		return "method"
	}
	u, err := url.Parse(in.URL)
	if err != nil || len(in.URL) > 8_192 || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Contains(in.URL, "#") {
		return "url"
	}
	if !brokerHeaders(in.Headers) {
		return "headers"
	}
	if len(in.Body) > 1<<20 || (len(in.Body) > 0 && (in.Method == "GET" || in.Method == "HEAD")) {
		return "body"
	}
	return ""
}

// brokerHeaders mirrors the edge's header rules: at most 32 lower-case
// header names and printable values, 8 KiB in all.
func brokerHeaders(headers map[string]string) bool {
	size := 0
	for name, value := range headers {
		if !headerNamePattern.MatchString(name) || !headerValue.MatchString(value) {
			return false
		}
		size += len(name) + len(value)
	}
	return len(headers) <= 32 && size <= 8_192
}

// SignRequest is an OpenSSH certificate request. The Wallet resolves it inside
// the CA's sign policy before it touches the key.
type SignRequest struct {
	Kind          string // "user" or "host"; a service may sign a host certificate where the policy allows it
	PublicKey     string // the subject's OpenSSH public key line
	Principals    []string
	TTLSeconds    int64
	SourceAddress string
	Extensions    []string
}

// Certificate is a signed OpenSSH certificate. It is public, not sealed.
type Certificate struct {
	OperationID  string
	ItemID       string
	Version      string
	Kind         string
	Certificate  string // `<type> <base64> <key id>`
	Serial       string // unsigned 64-bit decimal
	ValidAfter   time.Time
	ValidBefore  time.Time
	Principals   []string
	AuditPending bool
}

// Sign asks the Wallet to sign an OpenSSH certificate with an `ssh_ca` item
// (the CA's key never leaves custody). The returned certificate is checked to
// be for this item and operation (its key id is
// `wallet:<itemId>:service:sign:<operationId>`) and of the kind asked for.
func (c *Client) Sign(ctx context.Context, owner, itemID string, in SignRequest) (*Certificate, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !itemIDPattern.MatchString(itemID) {
		return nil, errInvalid("item_id")
	}
	if reason := validateSign(in); reason != "" {
		return nil, errInvalid(reason)
	}
	body := map[string]any{"kind": in.Kind, "publicKey": in.PublicKey, memberPrincipals: in.Principals, "ttlSeconds": in.TTLSeconds}
	if in.SourceAddress != "" {
		body["sourceAddress"] = in.SourceAddress
	}
	if len(in.Extensions) > 0 {
		body["extensions"] = in.Extensions
	}
	raw, status, err := c.post(ctx, owner, "/items/"+itemID+"/sign", body, false)
	if err != nil {
		return nil, err
	}
	certificate, ok := parseCertificate(raw, itemID, in.Kind)
	if status != 200 || !ok {
		return nil, errResponse(status, "sign")
	}
	return certificate, nil
}

// parseCertificate checks that a signing answer is for this item and kind, and
// that its certificate names this item and the answer's operation as its key id.
func parseCertificate(raw []byte, itemID, kind string) (*Certificate, bool) {
	var answer struct {
		OperationID  string   `json:"operationId"`
		ItemID       string   `json:"itemId"`
		Version      string   `json:"version"`
		Kind         string   `json:"kind"`
		Certificate  string   `json:"certificate"`
		Serial       string   `json:"serial"`
		ValidAfter   int64    `json:"validAfter"`
		ValidBefore  int64    `json:"validBefore"`
		Principals   []string `json:"principals"`
		AuditPending bool     `json:"auditPending"`
	}
	if json.Unmarshal(raw, &answer) != nil || answer.ItemID != itemID || answer.Kind != kind ||
		!operationIDPattern.MatchString(answer.OperationID) || !decimalPattern.MatchString(answer.Version) ||
		!serialPattern.MatchString(answer.Serial) || answer.ValidAfter <= 0 || answer.ValidBefore <= answer.ValidAfter ||
		len(answer.Principals) == 0 || !certificateFor(answer.Certificate, "wallet:"+itemID+":service:sign:"+answer.OperationID) {
		return nil, false
	}
	return &Certificate{
		OperationID: answer.OperationID, ItemID: itemID, Version: answer.Version, Kind: answer.Kind, Certificate: answer.Certificate,
		Serial: answer.Serial, ValidAfter: time.UnixMilli(answer.ValidAfter), ValidBefore: time.UnixMilli(answer.ValidBefore),
		Principals: answer.Principals, AuditPending: answer.AuditPending,
	}, true
}

func validateSign(in SignRequest) string {
	switch {
	case in.Kind != "user" && in.Kind != "host":
		return "kind"
	case in.PublicKey == "" || len(in.PublicKey) > 8_192:
		return "public_key"
	case !boundedList(in.Principals, 1, 32, 253):
		return "principals"
	case in.TTLSeconds <= 0 || in.TTLSeconds > 1<<53-1:
		return "ttl"
	case len(in.SourceAddress) > 1_024:
		return "source_address"
	case !boundedList(in.Extensions, 0, 5, 64):
		return "extensions"
	}
	return ""
}

// boundedList reports whether list has between least and most members, each
// non-empty and at most width bytes.
func boundedList(list []string, least, most, width int) bool {
	if len(list) < least || len(list) > most {
		return false
	}
	for _, member := range list {
		if member == "" || len(member) > width {
			return false
		}
	}
	return true
}

// certificateFor reports whether text is `<type>-cert-v01@openssh.com <base64>
// <keyID>` with exactly the expected key id.
func certificateFor(text, keyID string) bool {
	fields := strings.Split(text, " ")
	if len(fields) != 3 || !strings.HasSuffix(fields[0], "-cert-v01@openssh.com") || fields[2] != keyID {
		return false
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	return err == nil && len(blob) > 0
}
