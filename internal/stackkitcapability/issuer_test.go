package stackkitcapability

import (
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var conformanceSeed = [ed25519.SeedSize]byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

func TestIssueMatchesConformanceVector(t *testing.T) {
	t.Parallel()

	document, err := Issue(validRequest())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "advanced-capability-v1.json"))
	if err != nil {
		t.Fatalf("read conformance vector: %v", err)
	}
	want = []byte(strings.TrimSpace(string(want)))
	if string(document) != string(want) {
		t.Fatalf("Issue() document mismatch\n got: %s\nwant: %s", document, want)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(document, &envelope); err != nil {
		t.Fatalf("decode issued document: %v", err)
	}
	if len(envelope) != 13 {
		t.Fatalf("issued field count = %d, want 13", len(envelope))
	}
	for _, forbidden := range []string{"credential", "endpoint", "environment", "args", "command", "provider"} {
		if strings.Contains(strings.ToLower(string(document)), forbidden) {
			t.Fatalf("issued document contains forbidden marker %q", forbidden)
		}
	}
}

func TestIssueRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Request){
		"non-v7 capability": func(request *Request) {
			request.CapabilityID = "018f47a0-7b55-6cc2-8c4a-65f238d78a15"
		},
		"uppercase capability": func(request *Request) {
			request.CapabilityID = "018F47A0-7B55-7CC2-8C4A-65F238D78A15"
		},
		"bad issuer": func(request *Request) {
			request.IssuerID = "Kombify Cloud"
		},
		"bad stack": func(request *Request) {
			request.StackID = "../another-stack"
		},
		"non-local owner": func(request *Request) {
			request.OwnerRef = "owner/cloud/00112233445566778899aabbccddeeff"
		},
		"unsorted operations": func(request *Request) {
			request.AllowedOperations = []string{"terramate.change-set.create", "restore.drill"}
		},
		"duplicate operations": func(request *Request) {
			request.AllowedOperations = []string{"restore.drill", "restore.drill"}
		},
		"unknown operation": func(request *Request) {
			request.AllowedOperations = []string{"shell.execute"}
		},
		"network UI ref": func(request *Request) {
			request.UIManagerRef = "https://techstack.example/approvals/1"
		},
		"query in RIL ref": func(request *Request) {
			request.RILRef = "urn:kombify:ril:approval:42?token=secret"
		},
		"local path ref": func(request *Request) {
			request.RILRef = `C:\Temp\approval.json`
		},
		"subsecond issue": func(request *Request) {
			request.IssuedAt = request.IssuedAt.Add(time.Nanosecond)
		},
		"future issue": func(request *Request) {
			request.IssuedAt = request.Now.Add(futureTolerance + time.Second)
			request.ExpiresAt = request.IssuedAt.Add(time.Minute)
		},
		"non-UTC expiry": func(request *Request) {
			request.ExpiresAt = request.ExpiresAt.In(time.FixedZone("offset", 0))
		},
		"non-UTC trusted time": func(request *Request) {
			request.Now = request.Now.In(time.FixedZone("offset", 0))
		},
		"zero lifetime": func(request *Request) {
			request.ExpiresAt = request.IssuedAt
		},
		"already expired": func(request *Request) {
			request.Now = request.ExpiresAt
		},
		"overlong lifetime": func(request *Request) {
			request.ExpiresAt = request.IssuedAt.Add(maxLifetime + time.Second)
		},
		"missing signer": func(request *Request) {
			request.Signer = nil
		},
	}

	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := validRequest()
			mutate(&request)
			if _, err := Issue(request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Issue() error = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestIssueRejectsSignerMismatch(t *testing.T) {
	t.Parallel()

	request := validRequest()
	request.Signer = mismatchedSigner{
		public:  request.Signer.Public().(ed25519.PublicKey),
		private: ed25519.NewKeyFromSeed(bytesOf(0x80)),
	}
	if _, err := Issue(request); !errors.Is(err, ErrSigning) {
		t.Fatalf("Issue() error = %v, want ErrSigning", err)
	}
}

func TestConformanceMetadata(t *testing.T) {
	t.Parallel()

	request := validRequest()
	publicKey := request.Signer.Public().(ed25519.PublicKey)
	publicDigest := sha256.Sum256(publicKey)
	wantKeyID := "ed25519://sha256/" + hex.EncodeToString(publicDigest[:])

	vectorBytes, err := os.ReadFile(filepath.Join("testdata", "advanced-capability-v1.vector.json"))
	if err != nil {
		t.Fatalf("read vector metadata: %v", err)
	}
	var vector struct {
		SchemaVersion   string `json:"schemaVersion"`
		PublicKeyBase64 string `json:"publicKeyBase64"`
		KeyID           string `json:"keyId"`
		UnsignedJCS     string `json:"unsignedJcs"`
		DigestSHA256    string `json:"digestSha256"`
	}
	if err := json.Unmarshal(vectorBytes, &vector); err != nil {
		t.Fatalf("decode vector metadata: %v", err)
	}
	if vector.SchemaVersion != SchemaVersion {
		t.Fatalf("schemaVersion = %q, want %q", vector.SchemaVersion, SchemaVersion)
	}
	if vector.PublicKeyBase64 != base64.RawStdEncoding.EncodeToString(publicKey) {
		t.Fatalf("public key mismatch")
	}
	if vector.KeyID != wantKeyID {
		t.Fatalf("keyId = %q, want %q", vector.KeyID, wantKeyID)
	}

	envelope := envelopeForRequest(request, wantKeyID)
	unsigned, err := canonicalUnsigned(envelope)
	if err != nil {
		t.Fatalf("canonicalUnsigned() error = %v", err)
	}
	if vector.UnsignedJCS != string(unsigned) {
		t.Fatalf("unsigned JCS mismatch\n got: %s\nwant: %s", unsigned, vector.UnsignedJCS)
	}
	domainSeparated := append([]byte(SchemaVersion+"\x00"), unsigned...)
	digest := sha256.Sum256(domainSeparated)
	if vector.DigestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("digest mismatch")
	}
}

func validRequest() Request {
	issuedAt := time.Date(2026, time.July, 27, 15, 4, 5, 0, time.UTC)
	return Request{
		CapabilityID: "018f47a0-7b55-7cc2-8c4a-65f238d78a15",
		IssuerID:     "techstack",
		StackID:      "basement-main",
		OwnerRef:     "owner/local/00112233445566778899aabbccddeeff",
		AllowedOperations: []string{
			"drift.reconcile.advanced",
			"rollback.coordinated",
			"terramate.change-set.apply",
			"terramate.change-set.create",
		},
		UIManagerRef: "urn:kombify:techstack:ui-manager:approval:018f47a0-7b55-7cc2-8c4a-65f238d78a15",
		RILRef:       "urn:kombify:techstack:ril:decision:018f47a0-7b55-7cc2-8c4a-65f238d78a15",
		IssuedAt:     issuedAt,
		ExpiresAt:    issuedAt.Add(10 * time.Minute),
		Now:          issuedAt,
		Signer:       ed25519.NewKeyFromSeed(conformanceSeed[:]),
	}
}

func envelopeForRequest(request Request, keyID string) unsignedEnvelope {
	return unsignedEnvelope{
		CapabilityID:      request.CapabilityID,
		IssuerID:          request.IssuerID,
		StackID:           request.StackID,
		OwnerRef:          request.OwnerRef,
		AllowedOperations: request.AllowedOperations,
		UIManagerRef:      request.UIManagerRef,
		RILRef:            request.RILRef,
		IssuedAt:          request.IssuedAt.Format(time.RFC3339),
		ExpiresAt:         request.ExpiresAt.Format(time.RFC3339),
		KeyID:             keyID,
	}
}

type mismatchedSigner struct {
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func (signer mismatchedSigner) Public() crypto.PublicKey {
	return signer.public
}

func (signer mismatchedSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	return ed25519.Sign(signer.private, digest), nil
}

func bytesOf(start byte) []byte {
	value := make([]byte, ed25519.SeedSize)
	for index := range value {
		value[index] = start + byte(index)
	}
	return value
}
