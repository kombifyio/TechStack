package originca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func (s *memoryStore) Get(_ context.Context, tenantID, stackID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[tenantID+"/"+stackID]
	if !ok {
		return Record{}, ErrNotFound
	}
	return record, nil
}
func (s *memoryStore) Put(_ context.Context, record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]Record{}
	}
	s.records[record.TenantID+"/"+record.StackID] = record
	return nil
}
func (s *memoryStore) MarkInstalled(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (s *memoryStore) ClearRetired(context.Context, string, string, string) error { return nil }
func (s *memoryStore) Delete(_ context.Context, tenantID, stackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, tenantID+"/"+stackID)
	return nil
}

// fakeCloudflare records every request and answers certificate deletes with a
// configurable status.
type fakeCloudflare struct {
	mu            sync.Mutex
	requests      []string
	bodies        []string
	authorization []string
	deleteStatus  int
}

func (f *fakeCloudflare) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(body))
	f.authorization = append(f.authorization, r.Header.Get("Authorization"))
	status := f.deleteStatus
	f.mu.Unlock()
	if r.Method == http.MethodDelete {
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"x"}}`))
		} else {
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"boom"}],"result":null}`))
		}
		return
	}
	var request struct {
		CSR string `json:"csr"`
	}
	_ = json.Unmarshal(body, &request)
	block, _ := pem.Decode([]byte(request.CSR))
	csr, _ := x509.ParseCertificateRequest(block.Bytes)
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), DNSNames: csr.DNSNames}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, csr.PublicKey, caKey)
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	payload, _ := json.Marshal(map[string]interface{}{"success": true, "errors": []string{}, "result": map[string]interface{}{"id": "cf-1", "certificate": certificate}})
	_, _ = w.Write(payload)
}

func newCertClient(t *testing.T, f *fakeCloudflare) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(server.Close)
	client, err := NewClient(Config{APIToken: "secret-token-value", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testCSR(t *testing.T, hosts ...string) (csrPEM string, keyPEM string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{}, DNSNames: hosts}, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

// Decommission revokes every certificate Techstack still holds for the stack
// at Cloudflare, tolerates a retry against an already revoked one, and
// surfaces a real failure instead of dropping custody.
func TestDecommissionRevokesTheIssuedCertificate(t *testing.T) {
	cloudflare := &fakeCloudflare{}
	store := &memoryStore{}
	service := NewService(newCertClient(t, cloudflare), nil, store)
	_ = store.Put(context.Background(), Record{
		TenantID: "t", StackID: "s", CertificateID: "cert-current", Hosts: []string{"a.kombify.me"},
		NotAfter: time.Now().Add(time.Hour), RetiredIDs: []string{"cert-old"},
	})

	cloudflare.deleteStatus = http.StatusInternalServerError
	if err := service.Decommission(context.Background(), "t", "s"); err == nil {
		t.Fatal("a failed revocation must be surfaced")
	}
	if _, err := store.Get(context.Background(), "t", "s"); err != nil {
		t.Fatal("custody must survive a failed revocation so the retry can revoke")
	}

	cloudflare.deleteStatus = http.StatusNotFound
	cloudflare.requests = nil
	if err := service.Decommission(context.Background(), "t", "s"); err != nil {
		t.Fatalf("an already revoked certificate is success: %v", err)
	}
	if got := strings.Join(cloudflare.requests, ","); got != "DELETE /certificates/cert-current,DELETE /certificates/cert-old" {
		t.Fatalf("revocations = %s, want both certificates revoked", got)
	}
	if _, err := store.Get(context.Background(), "t", "s"); !errors.Is(err, ErrNotFound) {
		t.Fatal("custody must be gone once every certificate is revoked")
	}
}

// Only a CSR for kombify.me names ever reaches Cloudflare: key material and
// foreign hostnames are refused before any request leaves the process.
func TestOnlyACSRForManagedHostsReachesCloudflare(t *testing.T) {
	cloudflare := &fakeCloudflare{}
	client := newCertClient(t, cloudflare)
	hosts := []string{"app-auth.kombify.me", "o-203-0-113-10-443.origin.kombify.me"}

	csr, key := testCSR(t, hosts...)
	certificate, err := client.Issue(context.Background(), hosts, csr)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if certificate.ID != "cf-1" || len(cloudflare.bodies) != 1 || ContainsPrivateKey([]byte(cloudflare.bodies[0])) {
		t.Fatalf("issued %+v with bodies %v", certificate, cloudflare.bodies)
	}
	if cloudflare.authorization[0] != "Bearer secret-token-value" {
		t.Fatal("the token must authenticate the request")
	}

	foreign, _ := testCSR(t, "evil.example.com")
	for name, attempt := range map[string]func() error{
		"private key in place of a CSR": func() error { _, e := client.Issue(context.Background(), hosts, key); return e },
		"key appended to a CSR":         func() error { _, e := client.Issue(context.Background(), hosts, csr+key); return e },
		"foreign hostname": func() error {
			_, e := client.Issue(context.Background(), []string{"evil.example.com"}, foreign)
			return e
		},
		"foreign name inside the CSR": func() error { _, e := client.Issue(context.Background(), hosts, foreign); return e },
	} {
		if err := attempt(); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	if len(cloudflare.requests) != 1 {
		t.Fatalf("refused inputs must not reach Cloudflare, requests = %v", cloudflare.requests)
	}
}

// A missing token never degrades to a silent skip or an ACME order: issuance
// fails with an error that names the variable and no secret.
func TestMissingTokenFailsClosed(t *testing.T) {
	t.Setenv(TokenEnv, "")
	config, err := FromEnv()
	var missing *TokenMissingError
	if !errors.As(err, &missing) || config.APIToken != "" || !strings.Contains(err.Error(), TokenEnv) {
		t.Fatalf("FromEnv = %+v, %v; want a TokenMissingError naming %s", config, err, TokenEnv)
	}
	service := NewService(nil, err, &memoryStore{})
	csr, _ := testCSR(t, "a.kombify.me")
	if _, issueErr := service.Issue(context.Background(), []string{"a.kombify.me"}, csr); !errors.As(issueErr, &missing) {
		t.Fatalf("Issue error = %v, want the typed missing-token error", issueErr)
	}
	if err := service.Decommission(context.Background(), "t", "stack-without-certificate"); err != nil {
		t.Fatalf("destroy of a stack without a certificate needs no token: %v", err)
	}
}
