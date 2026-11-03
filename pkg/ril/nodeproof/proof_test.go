package nodeproof

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func canonValue(t *testing.T, v any) []byte {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	c, e := canonical(raw)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func signed(t *testing.T, k *ecdsa.PrivateKey, typ string, v any) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	h := canonValue(t, header{Alg: "ES256", Kid: "test-key", Typ: typ})
	p := canonValue(t, v)
	input := enc(h) + "." + enc(p)
	sum := sha256.Sum256([]byte(input))
	r, s, e := ecdsa.Sign(rand.Reader, k, sum[:])
	if e != nil {
		t.Fatal(e)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return input + "." + enc(signature)
}
func pub(k *ecdsa.PrivateKey) publicKey {
	to64 := func(v *big.Int) string {
		b := make([]byte, 32)
		v.FillBytes(b)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return publicKey{Kid: "test-key", Kty: "EC", Crv: "P-256", X: to64(k.X), Y: to64(k.Y), Alg: "ES256"}
}
func proofFixture(t *testing.T) (context.Context, *agentpb.StackKitCommand, string, time.Time) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	receiptKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	keys := keyFile{Generation: 1, IssuedAt: 1800000000, ValidUntil: 1800000045, RILClaim: keySet{Keys: []publicKey{pub(key)}}, RILExecution: keySet{Keys: []publicKey{pub(key)}}, DecisionReceipt: keySet{Keys: []publicKey{pub(receiptKey)}}}
	path := filepath.Join(t.TempDir(), "keys.json")
	raw, _ := json.Marshal(keys)
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	now := time.Unix(1800000000, 0).UTC()
	plan := "sha256:" + string(bytesRepeat('a', 64))
	id := Identity{TenantID: "tenant", OwnerID: "owner", ServerID: "server", StackID: "stack"}
	f := effect{SchemaVersion: "kombify.ril-effect.v1", OperationID: "op", Audience: "stackkits", TenantID: id.TenantID, OwnerSubject: id.OwnerID, CardID: "card", CardRevision: 1, ServerID: id.ServerID, StackID: id.StackID, ResolvedPlanHash: plan, Inputs: []json.RawMessage{}}
	f.Primitive.ID = "verify-stackkit-state"
	f.Primitive.ContractHash = plan
	f.Primitive.OperationClass = "product-verify"
	f.Target.Scope = "stack"
	fc := canonValue(t, f)
	eh := hash(append(append([]byte("kombify.ril-effect.v1\n"), fc...), '\n'))
	unsigned := map[string]any{"receiptId": "receipt", "decisionId": "decision", "operationId": "op", "issuedAt": now.Format(time.RFC3339), "outcome": "allowed", "optionKind": "allow_once", "answeredBy": map[string]any{"kind": "person", "id": "owner"}, "assurance": "L2", "intentDigest": plan, "lifetime": map[string]any{"scope": "once"}}
	tok := signed(t, receiptKey, "kombify.decision-receipt.v1", unsigned)
	parts := splitToken(tok)
	unsigned["signature"] = map[string]any{"alg": "ES256", "kid": "test-key", "value": parts[2]}
	rc := canonValue(t, unsigned)
	rh := hash(rc)
	l := lease{SchemaVersion: "kombify.ril-execution-lease.v1", Iss: "kombify-gateway", Aud: "stackkits", Purpose: "ril-execution", LeaseID: "lease", OperationID: "op", DecisionID: "decision", ReceiptDigest: rh, EffectDigest: eh, TenantID: "tenant", OwnerSubject: "owner", Requester: principal{Kind: "service", ID: "requester"}, Executor: "techstack", CardID: "card", CardRevision: 1, ServerID: "server", StackID: "stack", PrimitiveID: "verify-stackkit-state", Scope: "stackkit-verify", LifecycleRevision: 3, Iat: now.Unix(), Nbf: now.Unix(), Exp: now.Unix() + 90}
	lt := signed(t, key, "kombify.ril-execution-lease.v1", l)
	c := claim{SchemaVersion: "kombify.ril-execution-claim.v1", Iss: "kombify-gateway", Aud: "stackkits", Purpose: "ril-execution-claim", DecisionID: "decision", LeaseID: "lease", ExecutionID: "exec", Fence: "fence", EffectDigest: eh, ReceiptDigest: rh, LeaseDigest: hash([]byte(lt)), TenantID: "tenant", OwnerSubject: "owner", ServerID: "server", StackID: "stack", LifecycleRevision: 3, RuntimeLeaseExpiresAt: now.Unix() + 60, Iat: now.Unix(), Exp: now.Unix() + 20}
	ct := signed(t, key, "kombify.ril-execution-claim.v1", c)
	cmd := &agentpb.StackKitCommand{CommandId: "exec", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, ExpectedPlanHash: plan, RilExecutionProof: &agentpb.RILExecutionProof{LeaseJws: lt, ClaimJws: ct, ReceiptJson: rc, EffectJson: fc}}
	return WithIdentity(context.Background(), id), cmd, path, now
}
func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
func splitToken(v string) []string {
	var out []string
	start := 0
	for i := range v {
		if v[i] == '.' {
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}
func TestNodeProofClaimsAndExpiry(t *testing.T) {
	ctx, cmd, path, now := proofFixture(t)
	deadline, e := Verify(ctx, cmd, path, now)
	if e != nil || deadline.Unix() != now.Unix()+20 {
		t.Fatalf("valid proof: %v %v", deadline, e)
	}
	if _, e := Verify(ctx, cmd, path, now.Add(20*time.Second)); e == nil {
		t.Fatal("expired claim admitted")
	}
	cmd.CommandId = "other"
	if _, e := Verify(ctx, cmd, path, now); e == nil {
		t.Fatal("other command admitted")
	}
	cmd.CommandId = "exec"
	cmd.RilExecutionProof.EffectJson = append(cmd.RilExecutionProof.EffectJson, ' ')
	cmd.RilExecutionProof.ClaimJws += "x"
	if _, e := Verify(ctx, cmd, path, now); e == nil {
		t.Fatal("tampered signature admitted")
	}
}

func TestGatewayClaimGoldenVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/ril-execution-claim.v1.conformance.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Kid        string    `json:"kid"`
		PublicKey  publicKey `json:"publicKey"`
		LeaseToken string    `json:"leaseToken"`
		Lease      lease     `json:"lease"`
		Vectors    []struct {
			Name        string `json:"name"`
			Token       string `json:"token"`
			ExecutionID string `json:"executionId"`
			Fence       string `json:"fence"`
			Now         int64  `json:"now"`
			Valid       bool   `json:"valid"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.PublicKey.Kid = fixture.Kid
	fixture.PublicKey.Alg = "ES256"
	keys := keySet{Keys: []publicKey{fixture.PublicKey}}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			var c claim
			err := signedJWS(v.Token, "kombify.ril-execution-claim.v1", keys, &c)
			if err == nil {
				err = validateClaim(c, fixture.Lease, v.ExecutionID, v.Fence, hash([]byte(fixture.LeaseToken)), v.Now)
			}
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v, err=%v", v.Valid, err)
			}
		})
	}
}

type keyRoundTrip func(*http.Request) (*http.Response, error)

func (f keyRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestHostedPublicKeyBootstrapRollback(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now().Unix()
	data := keyFile{Generation: 2, IssuedAt: now, ValidUntil: now + 45, RILClaim: keySet{Keys: []publicKey{pub(key)}}, RILExecution: keySet{Keys: []publicKey{pub(key)}}, DecisionReceipt: keySet{Keys: []publicKey{pub(other)}}}
	var response []byte
	set := func() { response, _ = json.Marshal(data) }
	set()
	client := &http.Client{Transport: keyRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != purposeKeysOrigin {
			t.Fatal("untrusted origin")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(response))), Request: r, Header: make(http.Header)}, nil
	})}
	dir := filepath.Join(t.TempDir(), "private")
	if err := refreshPurposeKeys(context.Background(), dir, client); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, PurposeKeysFile))
	if err != nil {
		t.Fatal(err)
	}
	data.Generation = 1
	set()
	if refreshPurposeKeys(context.Background(), dir, client) == nil {
		t.Fatal("rollback accepted")
	}
	data.Generation = 2
	replacement, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	data.RILClaim.Keys = []publicKey{pub(replacement)}
	data.RILExecution.Keys = []publicKey{pub(replacement)}
	set()
	if refreshPurposeKeys(context.Background(), dir, client) == nil {
		t.Fatal("same generation replacement accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, PurposeKeysFile))
	if string(after) != string(saved) {
		t.Fatal("failed update changed key custody")
	}
}

func TestRetiringRILKeyCannotSignNewClaim(t *testing.T) {
	ctx, cmd, path, now := proofFixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var keys keyFile
	if json.Unmarshal(raw, &keys) != nil {
		t.Fatal("fixture")
	}
	replacement, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	active := pub(replacement)
	active.Kid = "new-active"
	keys.RILExecution.Keys = append(keys.RILExecution.Keys, active)
	keys.RILClaim.Keys = []publicKey{active}
	raw, _ = json.Marshal(keys)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(ctx, cmd, path, now); err == nil {
		t.Fatal("retiring key signed a new claim")
	}
}
