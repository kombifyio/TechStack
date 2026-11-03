// Package nodeproof verifies Gateway RIL authority at the worker execution seam.
package nodeproof

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	jcs "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

var stableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
var shaDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Identity struct{ TenantID, OwnerID, ServerID, StackID string }
type identityKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

type publicKey struct {
	Kid    string   `json:"kid"`
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv"`
	X      string   `json:"x"`
	Y      string   `json:"y,omitempty"`
	Alg    string   `json:"alg,omitempty"`
	Use    string   `json:"use,omitempty"`
	KeyOps []string `json:"key_ops,omitempty"`
	Ext    *bool    `json:"ext,omitempty"`
}
type keySet struct {
	Keys []publicKey `json:"keys"`
}
type keyFile struct {
	Generation      int64  `json:"generation"`
	IssuedAt        int64  `json:"issuedAt"`
	ValidUntil      int64  `json:"validUntil"`
	RILClaim        keySet `json:"rilClaim"`
	RILExecution    keySet `json:"rilExecution"`
	DecisionReceipt keySet `json:"decisionReceipt"`
}
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}
type principal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type lease struct {
	SchemaVersion     string    `json:"schemaVersion"`
	Iss               string    `json:"iss"`
	Aud               string    `json:"aud"`
	Purpose           string    `json:"purpose"`
	LeaseID           string    `json:"leaseId"`
	OperationID       string    `json:"operationId"`
	DecisionID        string    `json:"decisionId"`
	ReceiptDigest     string    `json:"receiptDigest"`
	EffectDigest      string    `json:"effectDigest"`
	TenantID          string    `json:"tenantId"`
	OwnerSubject      string    `json:"ownerSubject"`
	Requester         principal `json:"requester"`
	Executor          string    `json:"executor"`
	CardID            string    `json:"cardId"`
	CardRevision      int64     `json:"cardRevision"`
	ServerID          string    `json:"serverId"`
	StackID           string    `json:"stackId"`
	PrimitiveID       string    `json:"primitiveId"`
	Scope             string    `json:"scope"`
	LifecycleRevision int64     `json:"lifecycleRevision"`
	Iat               int64     `json:"iat"`
	Nbf               int64     `json:"nbf"`
	Exp               int64     `json:"exp"`
}
type claim struct {
	SchemaVersion         string `json:"schemaVersion"`
	Iss                   string `json:"iss"`
	Aud                   string `json:"aud"`
	Purpose               string `json:"purpose"`
	DecisionID            string `json:"decisionId"`
	LeaseID               string `json:"leaseId"`
	ExecutionID           string `json:"executionId"`
	Fence                 string `json:"fence"`
	EffectDigest          string `json:"effectDigest"`
	ReceiptDigest         string `json:"receiptDigest"`
	LeaseDigest           string `json:"leaseDigest"`
	TenantID              string `json:"tenantId"`
	OwnerSubject          string `json:"ownerSubject"`
	ServerID              string `json:"serverId"`
	StackID               string `json:"stackId"`
	LifecycleRevision     int64  `json:"lifecycleRevision"`
	RuntimeLeaseExpiresAt int64  `json:"runtimeLeaseExpiresAt"`
	Iat                   int64  `json:"iat"`
	Exp                   int64  `json:"exp"`
}
type effect struct {
	SchemaVersion string `json:"schemaVersion"`
	OperationID   string `json:"operationId"`
	Audience      string `json:"audience"`
	TenantID      string `json:"tenantId"`
	OwnerSubject  string `json:"ownerSubject"`
	CardID        string `json:"cardId"`
	CardRevision  int64  `json:"cardRevision"`
	ServerID      string `json:"serverId"`
	StackID       string `json:"stackId"`
	Primitive     struct {
		ID             string `json:"id"`
		ContractHash   string `json:"contractHash"`
		OperationClass string `json:"operationClass"`
	} `json:"primitive"`
	ResolvedPlanHash string `json:"resolvedPlanHash"`
	Target           struct {
		Scope               string `json:"scope"`
		SiteRef             string `json:"siteRef,omitempty"`
		NodeRef             string `json:"nodeRef,omitempty"`
		ModuleInstanceRef   string `json:"moduleInstanceRef,omitempty"`
		RuntimeInstanceRef  string `json:"runtimeInstanceRef,omitempty"`
		ExecutionChannelRef string `json:"executionChannelRef,omitempty"`
	} `json:"target"`
	Inputs []json.RawMessage `json:"inputs"`
}
type receipt struct {
	ReceiptID    string    `json:"receiptId"`
	DecisionID   string    `json:"decisionId"`
	OperationID  string    `json:"operationId"`
	IssuedAt     string    `json:"issuedAt"`
	Outcome      string    `json:"outcome"`
	OptionKind   string    `json:"optionKind"`
	AnsweredBy   principal `json:"answeredBy"`
	Surface      string    `json:"surface,omitempty"`
	Assurance    string    `json:"assurance"`
	IntentDigest string    `json:"intentDigest"`
	Lifetime     struct {
		Scope string `json:"scope"`
	} `json:"lifetime"`
	Signature struct {
		Alg   string `json:"alg"`
		Kid   string `json:"kid"`
		Value string `json:"value"`
	} `json:"signature"`
}

func strict(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func canonical(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 65536 {
		return nil, fmt.Errorf("proof size invalid")
	}
	return jcs.Transform(raw)
}
func hash(raw []byte) string { h := sha256.Sum256(raw); return "sha256:" + hex.EncodeToString(h[:]) }
func rawURL(s string) ([]byte, error) {
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || base64.RawURLEncoding.EncodeToString(b) != s {
		return nil, fmt.Errorf("invalid base64url")
	}
	return b, nil
}
func key(keys keySet, kid, alg string) (publicKey, error) {
	var out publicKey
	n := 0
	for _, k := range keys.Keys {
		if k.Kid == kid {
			out = k
			n++
		}
	}
	if n != 1 || kid == "" || (out.Alg != "" && out.Alg != alg) || (out.Use != "" && out.Use != "sig") || (len(out.KeyOps) != 0 && (len(out.KeyOps) != 1 || out.KeyOps[0] != "verify")) {
		return publicKey{}, fmt.Errorf("purpose key unavailable")
	}
	return out, nil
}
func validPublicKey(k publicKey, alg string) bool {
	x, err := rawURL(k.X)
	if err != nil || len(x) != 32 {
		return false
	}
	if alg == "EdDSA" {
		return k.Kty == "OKP" && k.Crv == "Ed25519" && k.Y == ""
	}
	y, err := rawURL(k.Y)
	return err == nil && len(y) == 32 && k.Kty == "EC" && k.Crv == "P-256" && elliptic.P256().IsOnCurve(new(big.Int).SetBytes(x), new(big.Int).SetBytes(y))
}
func verifySig(k publicKey, alg string, signed, signature []byte) bool {
	x, err := rawURL(k.X)
	if err != nil {
		return false
	}
	switch alg {
	case "ES256":
		y, e := rawURL(k.Y)
		if e != nil || k.Kty != "EC" || k.Crv != "P-256" || len(x) != 32 || len(y) != 32 || len(signature) != 64 {
			return false
		}
		pub := ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return false
		}
		h := sha256.Sum256(signed)
		return ecdsa.Verify(&pub, h[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:]))
	case "EdDSA":
		return k.Kty == "OKP" && k.Crv == "Ed25519" && k.Y == "" && len(x) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(x), signed, signature)
	}
	return false
}
func signedJWS(token, typ string, keys keySet, dst any) error {
	if len(token) > 16384 {
		return fmt.Errorf("JWS too large")
	}
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return fmt.Errorf("invalid JWS")
	}
	hraw, e := rawURL(p[0])
	if e != nil {
		return e
	}
	var h header
	if strict(hraw, &h) != nil || h.Typ != typ || h.Alg != "ES256" {
		return fmt.Errorf("invalid protected header")
	}
	hc, e := canonical(hraw)
	if e != nil || !bytes.Equal(hc, hraw) {
		return fmt.Errorf("noncanonical header")
	}
	k, e := key(keys, h.Kid, h.Alg)
	if e != nil {
		return e
	}
	sig, e := rawURL(p[2])
	if e != nil || !verifySig(k, h.Alg, []byte(p[0]+"."+p[1]), sig) {
		return fmt.Errorf("invalid signature")
	}
	raw, e := rawURL(p[1])
	if e != nil {
		return e
	}
	c, e := canonical(raw)
	if e != nil || !bytes.Equal(c, raw) {
		return fmt.Errorf("noncanonical payload")
	}
	return strict(raw, dst)
}
func readKeys(path string) (keyFile, error) {
	var keys keyFile
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 65536 {
		return keys, fmt.Errorf("purpose key file unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return keys, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || info.Size() != opened.Size() {
		return keys, fmt.Errorf("purpose key file changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || int64(len(raw)) != info.Size() {
		return keys, fmt.Errorf("purpose key file changed")
	}
	return parseKeyFile(raw)
}

func parseKeyFile(raw []byte) (keyFile, error) {
	var keys keyFile
	if strict(raw, &keys) != nil || keys.Generation < 1 || len(keys.RILClaim.Keys) != 1 || len(keys.RILExecution.Keys) == 0 || len(keys.DecisionReceipt.Keys) == 0 || len(keys.RILExecution.Keys) > 100 || len(keys.DecisionReceipt.Keys) > 100 {
		return keys, fmt.Errorf("purpose key file invalid")
	}
	seen := map[string]bool{}
	for index, set := range []keySet{keys.RILExecution, keys.DecisionReceipt} {
		ids := map[string]bool{}
		for _, item := range set.Keys {
			alg := "ES256"
			if index == 1 && item.Kty == "OKP" && item.Crv == "Ed25519" {
				alg = "EdDSA"
			}
			if _, err := key(set, item.Kid, alg); err != nil || !validPublicKey(item, alg) {
				return keys, fmt.Errorf("invalid purpose key material")
			}
			if item.Kid == "" || ids[item.Kid] {
				return keys, fmt.Errorf("ambiguous purpose key id")
			}
			ids[item.Kid] = true
			material := item.Kty + ":" + item.Crv + ":" + item.X + ":" + item.Y
			if seen[material] {
				return keys, fmt.Errorf("key material is reused across authority purposes")
			}
			seen[material] = true
		}
	}
	claimKey := keys.RILClaim.Keys[0]
	matched := false
	for _, k := range keys.RILExecution.Keys {
		if k.Kid == claimKey.Kid && k.Kty == claimKey.Kty && k.Crv == claimKey.Crv && k.X == claimKey.X && k.Y == claimKey.Y && k.Alg == claimKey.Alg {
			matched = true
		}
	}
	if !matched {
		return keys, fmt.Errorf("claim key is not in the RIL-purpose set")
	}
	return keys, nil
}
func verifyReceipt(raw []byte, keys keySet) (receipt, []byte, error) {
	var r receipt
	c, e := canonical(raw)
	if e != nil {
		return r, nil, e
	}
	if strict(c, &r) != nil {
		return r, nil, fmt.Errorf("invalid receipt")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(c, &fields) != nil {
		return r, nil, fmt.Errorf("invalid receipt")
	}
	delete(fields, "signature")
	unsigned, e := json.Marshal(fields)
	if e != nil {
		return r, nil, e
	}
	unsigned, e = canonical(unsigned)
	if e != nil {
		return r, nil, e
	}
	hraw, _ := json.Marshal(header{Alg: r.Signature.Alg, Kid: r.Signature.Kid, Typ: "kombify.decision-receipt.v1"})
	hraw, e = canonical(hraw)
	if e != nil {
		return r, nil, e
	}
	k, e := key(keys, r.Signature.Kid, r.Signature.Alg)
	if e != nil {
		return r, nil, e
	}
	sig, e := rawURL(r.Signature.Value)
	if e != nil || !verifySig(k, r.Signature.Alg, []byte(base64.RawURLEncoding.EncodeToString(hraw)+"."+base64.RawURLEncoding.EncodeToString(unsigned)), sig) {
		return r, nil, fmt.Errorf("invalid receipt signature")
	}
	return r, c, nil
}

// Verify checks signatures, exact effect and locally enrolled identity. The
// caller must enforce the returned absolute deadline through process exit.
func Verify(ctx context.Context, command *agentpb.StackKitCommand, keyPath string, now time.Time) (time.Time, error) {
	if command == nil || command.RilExecutionProof == nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY {
		return time.Time{}, fmt.Errorf("RIL proof required")
	}
	id, ok := ctx.Value(identityKey{}).(Identity)
	if !ok || id.TenantID == "" || id.ServerID == "" {
		return time.Time{}, fmt.Errorf("local RIL identity unavailable")
	}
	keys, e := readKeys(keyPath)
	if e != nil {
		return time.Time{}, e
	}
	if keys.IssuedAt > now.Unix() || keys.ValidUntil <= now.Unix() || keys.ValidUntil <= keys.IssuedAt || keys.ValidUntil-keys.IssuedAt > 45 {
		return time.Time{}, fmt.Errorf("RIL public key snapshot expired")
	}
	proof := command.RilExecutionProof
	var l lease
	if e = signedJWS(proof.LeaseJws, "kombify.ril-execution-lease.v1", keys.RILExecution, &l); e != nil {
		return time.Time{}, e
	}
	var c claim
	if e = signedJWS(proof.ClaimJws, "kombify.ril-execution-claim.v1", keys.RILClaim, &c); e != nil {
		return time.Time{}, e
	}
	r, rc, e := verifyReceipt(proof.ReceiptJson, keys.DecisionReceipt)
	if e != nil {
		return time.Time{}, e
	}
	ec, e := canonical(proof.EffectJson)
	if e != nil {
		return time.Time{}, e
	}
	var f effect
	if strict(ec, &f) != nil {
		return time.Time{}, fmt.Errorf("invalid effect")
	}
	eh := hash(append(append([]byte("kombify.ril-effect.v1\n"), ec...), '\n'))
	rh := hash(rc)
	lh := hash([]byte(proof.LeaseJws))
	n := now.Unix()
	if l.SchemaVersion != "kombify.ril-execution-lease.v1" || l.Iss != "kombify-gateway" || l.Aud != "stackkits" || l.Purpose != "ril-execution" || l.Executor != "techstack" || l.PrimitiveID != "verify-stackkit-state" || l.Scope != "stackkit-verify" || l.Iat <= 0 || l.Nbf < l.Iat || l.Nbf > n || l.Exp <= n || l.Exp-l.Iat > 900 || l.CardRevision < 1 || l.LifecycleRevision < 1 || !stableID.MatchString(l.LeaseID) || !stableID.MatchString(l.OperationID) || !stableID.MatchString(l.DecisionID) || !stableID.MatchString(l.CardID) || !shaDigest.MatchString(l.EffectDigest) || !shaDigest.MatchString(l.ReceiptDigest) || l.Requester.ID == "" || (l.Requester.Kind != "service" && l.Requester.Kind != "agent_instance") {
		return time.Time{}, fmt.Errorf("lease authority invalid")
	}
	if err := validateClaim(c, l, command.CommandId, c.Fence, lh, n); err != nil {
		return time.Time{}, err
	}
	if c.EffectDigest != eh || c.ReceiptDigest != rh {
		return time.Time{}, fmt.Errorf("claim evidence mismatch")
	}

	issuedAt, issuedErr := time.Parse(time.RFC3339Nano, r.IssuedAt)
	if issuedErr != nil || issuedAt.After(now) || !stableID.MatchString(r.ReceiptID) || l.EffectDigest != eh || l.ReceiptDigest != rh || r.DecisionID != l.DecisionID || r.OperationID != l.OperationID || r.Outcome != "allowed" || r.OptionKind != "allow_once" || r.Lifetime.Scope != "once" || r.AnsweredBy.Kind != "person" || r.AnsweredBy.ID != l.OwnerSubject || r.Assurance != "L2" || !shaDigest.MatchString(r.IntentDigest) {
		return time.Time{}, fmt.Errorf("decision binding invalid")
	}
	if f.SchemaVersion != "kombify.ril-effect.v1" || f.Audience != "stackkits" || f.OperationID != l.OperationID || f.CardID != l.CardID || f.CardRevision != l.CardRevision || f.Primitive.ID != "verify-stackkit-state" || f.Primitive.OperationClass != "product-verify" || f.Target.Scope != "stack" || f.Target.SiteRef != "" || f.Target.NodeRef != "" || f.Target.ModuleInstanceRef != "" || f.Target.RuntimeInstanceRef != "" || f.Target.ExecutionChannelRef != "" || f.Inputs == nil || len(f.Inputs) != 0 || !shaDigest.MatchString(f.Primitive.ContractHash) || !shaDigest.MatchString(f.ResolvedPlanHash) || f.ResolvedPlanHash != command.ExpectedPlanHash {
		return time.Time{}, fmt.Errorf("effect binding invalid")
	}
	if l.TenantID != id.TenantID || l.ServerID != id.ServerID || l.OwnerSubject == "" || l.StackID == "" || (id.OwnerID != "" && l.OwnerSubject != id.OwnerID) || (id.StackID != "" && l.StackID != id.StackID) || c.TenantID != l.TenantID || c.OwnerSubject != l.OwnerSubject || c.ServerID != l.ServerID || c.StackID != l.StackID || f.TenantID != l.TenantID || f.OwnerSubject != l.OwnerSubject || f.ServerID != l.ServerID || f.StackID != l.StackID {
		return time.Time{}, fmt.Errorf("local identity mismatch")
	}
	return time.Unix(c.Exp, 0).UTC(), nil
}

func validateClaim(c claim, l lease, executionID, fence, leaseDigest string, now int64) error {
	if c.SchemaVersion != "kombify.ril-execution-claim.v1" || c.Iss != "kombify-gateway" || c.Aud != "stackkits" || c.Purpose != "ril-execution-claim" || c.Iat <= 0 || c.Iat < l.Nbf || c.Iat > now || c.Exp <= now || c.Exp-c.Iat > 30 || c.Exp > l.Exp || c.Exp > c.RuntimeLeaseExpiresAt || !stableID.MatchString(c.Fence) || c.Fence != fence || c.ExecutionID != executionID || c.DecisionID != l.DecisionID || c.LeaseID != l.LeaseID || c.LifecycleRevision != l.LifecycleRevision || c.EffectDigest != l.EffectDigest || c.ReceiptDigest != l.ReceiptDigest || c.LeaseDigest != leaseDigest || c.TenantID != l.TenantID || c.OwnerSubject != l.OwnerSubject || c.ServerID != l.ServerID || c.StackID != l.StackID {
		return fmt.Errorf("claim authority invalid")
	}
	return nil
}

// TransportFacts decodes untrusted Gateway response for comparison to locked
// server facts; only Verify authenticates it at the node.
type TransportFacts struct {
	DecisionID, LeaseID, ExecutionID, Fence, EffectDigest, ReceiptDigest, TenantID, OwnerSubject, ServerID, StackID string
	LifecycleRevision, RuntimeLeaseExpiresAt, Exp                                                                   int64
}

func InspectTransport(proof *agentpb.RILExecutionProof) (TransportFacts, error) {
	var out TransportFacts
	if proof == nil || len(proof.ClaimJws) > 16384 {
		return out, fmt.Errorf("claim missing")
	}
	parts := strings.Split(proof.ClaimJws, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("claim malformed")
	}
	raw, err := rawURL(parts[1])
	if err != nil {
		return out, err
	}
	var c claim
	if strict(raw, &c) != nil {
		return out, fmt.Errorf("claim malformed")
	}
	effect, err := canonical(proof.EffectJson)
	if err != nil {
		return out, err
	}
	if hash(append(append([]byte("kombify.ril-effect.v1\n"), effect...), '\n')) != c.EffectDigest {
		return out, fmt.Errorf("claim effect mismatch")
	}
	return TransportFacts{DecisionID: c.DecisionID, LeaseID: c.LeaseID, ExecutionID: c.ExecutionID, Fence: c.Fence, EffectDigest: c.EffectDigest, ReceiptDigest: c.ReceiptDigest, TenantID: c.TenantID, OwnerSubject: c.OwnerSubject, ServerID: c.ServerID, StackID: c.StackID, LifecycleRevision: c.LifecycleRevision, RuntimeLeaseExpiresAt: c.RuntimeLeaseExpiresAt, Exp: c.Exp}, nil
}
