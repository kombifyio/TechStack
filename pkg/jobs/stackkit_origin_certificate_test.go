package jobs

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/originca"
)

type memoryOriginStore struct {
	mu      sync.Mutex
	records map[string]originca.Record
}

func (s *memoryOriginStore) key(tenantID, stackID string) string { return tenantID + "/" + stackID }
func (s *memoryOriginStore) Get(_ context.Context, tenantID, stackID string) (originca.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[s.key(tenantID, stackID)]
	if !ok {
		return originca.Record{}, originca.ErrNotFound
	}
	return record, nil
}
func (s *memoryOriginStore) Put(_ context.Context, record originca.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]originca.Record{}
	}
	s.records[s.key(record.TenantID, record.StackID)] = record
	return nil
}
func (s *memoryOriginStore) MarkInstalled(_ context.Context, tenantID, stackID, id, sha string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[s.key(tenantID, stackID)]
	record.InstalledAt, record.CertificateSHA256 = at, sha
	s.records[s.key(tenantID, stackID)] = record
	return nil
}
func (s *memoryOriginStore) ClearRetired(_ context.Context, tenantID, stackID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.records[s.key(tenantID, stackID)]
	kept := []string{}
	for _, retired := range record.RetiredIDs {
		if retired != id {
			kept = append(kept, retired)
		}
	}
	record.RetiredIDs = kept
	s.records[s.key(tenantID, stackID)] = record
	return nil
}
func (s *memoryOriginStore) Delete(_ context.Context, tenantID, stackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, s.key(tenantID, stackID))
	return nil
}

// recordingCA signs the CSRs it is given, like Cloudflare, and records every
// call in order so the test can assert the sequence and the inputs.
type recordingCA struct {
	events []string
	inputs []string
	serial int64
}

func (ca *recordingCA) Issue(_ context.Context, hosts []string, csrPEM string) (originca.Certificate, error) {
	ca.inputs = append(ca.inputs, csrPEM)
	block, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return originca.Certificate{}, err
	}
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca.serial++
	template := &x509.Certificate{
		SerialNumber: big.NewInt(ca.serial), Subject: pkix.Name{CommonName: "test origin ca"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second),
		DNSNames: hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, csr.PublicKey, caKey)
	if err != nil {
		return originca.Certificate{}, err
	}
	id := fmt.Sprintf("cf-cert-%d", ca.serial)
	ca.events = append(ca.events, "issue:"+id)
	leaf, _ := x509.ParseCertificate(der)
	return originca.Certificate{ID: id, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Hosts: hosts, ExpiresOn: leaf.NotAfter.UTC()}, nil
}

func (ca *recordingCA) Revoke(_ context.Context, id string) error {
	ca.events = append(ca.events, "revoke:"+id)
	return nil
}

// originNode is the node: it generates and keeps the key, returns only a CSR,
// and confirms an install of the certificate that matches its key.
type originNode struct {
	ca       *recordingCA
	key      *ecdsa.PrivateKey
	keyPEM   []byte
	commands []*agentpb.StackKitCommand
}

func (n *originNode) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	n.commands = append(n.commands, command)
	result := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, Release: command.Release, CommandResultSchemaVersion: "stackkit.command-result/v1"}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST:
		n.ca.events = append(n.ca.events, "node:request")
		n.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		keyDER, _ := x509.MarshalECPrivateKey(n.key)
		n.keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: command.OriginHosts}, n.key)
		digest := sha256.Sum256(csrDER)
		result.CommandResultJson, _ = json.Marshal(map[string]interface{}{
			"schemaVersion": "stackkit.command-result/v1", "command": "stackkit advanced origin-certificate request", "status": "success",
			"data": map[string]interface{}{
				"schemaVersion": "stackkit.origin-certificate/v1", "phase": "request", "hosts": command.OriginHosts, "keyAlgorithm": "ecdsa-p256",
				"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), "csrSha256": "sha256:" + hex.EncodeToString(digest[:]),
			},
		})
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL:
		n.ca.events = append(n.ca.events, "node:install")
		block, _ := pem.Decode(command.OriginCertificatePem)
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(leaf.Raw)
		result.CommandResultJson, _ = json.Marshal(map[string]interface{}{
			"schemaVersion": "stackkit.command-result/v1", "command": "stackkit advanced origin-certificate install", "status": "success",
			"data": map[string]interface{}{
				"schemaVersion": "stackkit.origin-certificate/v1", "phase": "install", "hosts": leaf.DNSNames, "serialHex": leaf.SerialNumber.Text(16),
				"notBefore": leaf.NotBefore.UTC().Format(time.RFC3339), "notAfter": leaf.NotAfter.UTC().Format(time.RFC3339),
				"certificateSha256": "sha256:" + hex.EncodeToString(digest[:]),
			},
		})
	default:
		return nil, fmt.Errorf("unexpected operation %s", command.Operation)
	}
	return result, nil
}

func originRollout(t *testing.T, hosts []string) (*managedOriginCertificate, *originNode, *recordingCA, *memoryOriginStore) {
	t.Helper()
	ca, store := &recordingCA{}, &memoryOriginStore{}
	return &managedOriginCertificate{service: originca.NewService(ca, nil, store), issuer: staticAdvancedIssuer{}, hosts: hosts}, &originNode{ca: ca}, ca, store
}

func originBase() StackKitLifecycleRequest {
	return StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		StackKit: "basement-kit", SpecPath: "stack-spec.yaml",
	}
}

// The key exists on the node only. Everything Techstack hands to Cloudflare,
// persists in custody, sends to the node or returns in a result is a CSR, the
// public certificate or an id.
func TestOriginCertificateKeyNeverLeavesTheNode(t *testing.T) {
	hosts := []string{"app-auth.kombify.me", "app-id.kombify.me", "o-203-0-113-10-443.origin.kombify.me"}
	rollout, node, ca, store := originRollout(t, hosts)
	release := releaseWithAdvancedCatalog(t)

	result, err := rollout.run(context.Background(), node, "job-1", originBase(), release)
	if err != nil {
		t.Fatalf("origin certificate rollout: %v", err)
	}
	if len(node.keyPEM) == 0 {
		t.Fatal("the node generated no key")
	}
	custody, _ := store.Get(context.Background(), "tenant-1", "stack-1")
	encoded, _ := json.Marshal(map[string]interface{}{"custody": custody, "result": result, "ca": ca.inputs})
	for _, command := range node.commands {
		raw, _ := json.Marshal(command)
		encoded = append(encoded, raw...)
	}
	keyDER, _ := x509.MarshalECPrivateKey(node.key)
	if originca.ContainsPrivateKey(encoded) || bytes.Contains(encoded, keyDER) || strings.Contains(string(encoded), hex.EncodeToString(node.key.D.Bytes())) {
		t.Fatal("private key material crossed the control plane")
	}
	if custody.CertificateID != "cf-cert-1" || custody.InstalledAt.IsZero() {
		t.Fatalf("custody = %+v, want the issued id recorded as installed", custody)
	}
}

// The superseded certificate is revoked only after the node confirmed the
// install of its replacement, never before.
func TestOriginCertificateReissueRevokesTheOldOneAfterTheNodeInstall(t *testing.T) {
	rollout, node, ca, store := originRollout(t, []string{"app-auth.kombify.me", "o-203-0-113-10-443.origin.kombify.me"})
	release := releaseWithAdvancedCatalog(t)
	if _, err := rollout.run(context.Background(), node, "job-1", originBase(), release); err != nil {
		t.Fatal(err)
	}
	ca.events = nil

	rollout.hosts = []string{"app-auth.kombify.me", "app-photos.kombify.me", "o-203-0-113-10-443.origin.kombify.me"}
	if _, err := rollout.run(context.Background(), node, "job-2", originBase(), release); err != nil {
		t.Fatal(err)
	}
	want := "node:request,issue:cf-cert-2,node:install,revoke:cf-cert-1"
	if got := strings.Join(ca.events, ","); got != want {
		t.Fatalf("sequence = %s, want %s", got, want)
	}
	if custody, _ := store.Get(context.Background(), "tenant-1", "stack-1"); custody.CertificateID != "cf-cert-2" || len(custody.RetiredIDs) != 0 {
		t.Fatalf("custody = %+v, want the new id and nothing left to revoke", custody)
	}
}

// originAwareSender answers the origin certificate operations like a node and
// forwards every other command to the wrapped sender.
type originAwareSender struct {
	inner StackKitCommandSender
	node  *originNode
}

func (s originAwareSender) SendStackKitCommand(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_REQUEST ||
		command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_ORIGIN_CERTIFICATE_INSTALL {
		return s.node.SendStackKitCommand(ctx, agentID, command)
	}
	return s.inner.SendStackKitCommand(ctx, agentID, command)
}

func newOriginAwareSender(inner StackKitCommandSender) (StackKitCommandSender, *originca.Service) {
	ca := &recordingCA{}
	return originAwareSender{inner: inner, node: &originNode{ca: ca}}, originca.NewService(ca, nil, &memoryOriginStore{})
}

// A node whose release predates ADR-0047 renders its own ACME router: the
// managed rollout must not dispatch an origin-certificate operation that
// release cannot serve (the rollout used to fail before apply).
func TestManagedRolloutSkipsOriginCertificateOnNodeACMEReleases(t *testing.T) {
	configureTestStackKitRelease(t)
	catalogPath := filepath.Join(os.Getenv(stackKitReleaseCacheEnv), stackkitrelease.AdvancedOperationsFileName)
	raw, err := os.ReadFile(catalogPath) // #nosec G304 -- test temp dir.
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	kept := []interface{}{}
	for _, entry := range document["operations"].([]interface{}) {
		if !strings.HasPrefix(fmt.Sprint(entry.(map[string]interface{})["operation"]), "public-tls.origin-certificate.") {
			kept = append(kept, entry)
		}
	}
	document["operations"] = kept
	if raw, err = json.Marshal(document); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := configuredTargetStackKitRelease()
	if err != nil || release == nil {
		t.Fatalf("release: %v", err)
	}
	queue := NewQueue(1, logger.New("error", ""))
	t.Cleanup(queue.Stop)
	job := &Job{ID: "job-node-acme", TargetID: "stack-1", Result: map[string]interface{}{}}
	queue.jobs[job.ID] = job
	rollout := &deployRollout{cfg: &ProvisionConfig{}, job: job, q: queue, routeHosts: []string{"demo.kombify.me"}}

	hook, err := rollout.managedOriginCertificate(*release)
	if err != nil || hook != nil {
		t.Fatalf("node-ACME release must skip the origin certificate, got hook=%v err=%v", hook, err)
	}
}

// An origin certificate that cannot be installed must not stop the stack: the
// rollout applies, completes degraded and names public TLS for the repair.
func TestManagedRolloutCompletesDegradedWhenTheOriginCertificateFails(t *testing.T) {
	sender := &recordingStackKitCommandSender{applyStatus: "applied"}
	request := typedStackKitApplyRequest()
	hosts := []string{"app-auth.kombify.me", "o-203-0-113-10-443.origin.kombify.me"}
	request.originCertificate = &managedOriginCertificate{
		service: originca.NewService(nil, &originca.TokenMissingError{}, &memoryOriginStore{}),
		issuer:  staticAdvancedIssuer{}, hosts: hosts,
	}

	result, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t))
	if err != nil {
		t.Fatalf("rollout failed instead of completing degraded: %v", err)
	}
	applied := false
	for _, command := range sender.commands {
		applied = applied || command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY
	}
	if !applied || resultString(result, "status") != "completed_degraded" {
		t.Fatalf("applied=%v status=%q, want apply dispatched and completed_degraded", applied, resultString(result, "status"))
	}
	if repair := DegradedPublicTLSHosts(result); !slices.Contains(repair, "app-auth.kombify.me") {
		t.Fatalf("degraded public TLS hosts = %v, want the route hostnames for the repair", repair)
	}
}

// degradedApplySender answers APPLY like a StackKits release whose ledger
// completed degraded: a non-zero exit with the public TLS unit waiting for its
// origin certificate.
type degradedApplySender struct{ recordingStackKitCommandSender }

func (s *degradedApplySender) SendStackKitCommand(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	result, err := s.recordingStackKitCommandSender.SendStackKitCommand(ctx, agentID, command)
	if err != nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		return result, err
	}
	result.Success, result.ExitCode = false, 1
	result.CommandResultJson, _ = json.Marshal(map[string]interface{}{
		"schemaVersion": "stackkit.command-result/v1", "command": "stackkit apply", "status": "failed",
		"data": map[string]interface{}{
			"schemaVersion": "stackkit.apply-result/v2", "status": "completed_degraded",
			"outcomes": map[string]interface{}{"overall": "completed_degraded", "units": []interface{}{map[string]interface{}{
				"subject": map[string]interface{}{"moduleRef": "stackkits-public-tls-contract", "requirementId": "public-tls"},
				"outcome": "degraded", "failure": map[string]interface{}{"class": "origin_certificate_missing", "retryable": true},
			}}},
		},
	})
	return result, nil
}

// StackKits exits non-zero while a unit is incomplete; a ledger that says the
// core applied completes the rollout degraded with the unit named for repair.
func TestManagedRolloutCompletesDegradedWhenStackKitsReportsADegradedApply(t *testing.T) {
	sender, service := newOriginAwareSender(&degradedApplySender{})
	request := typedStackKitApplyRequest()
	request.originCertificate = &managedOriginCertificate{service: service, issuer: staticAdvancedIssuer{},
		hosts: []string{"app-auth.kombify.me", "o-203-0-113-10-443.origin.kombify.me"}}

	result, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, releaseWithAdvancedCatalog(t))
	if err != nil {
		t.Fatalf("a degraded StackKits Apply failed the rollout: %v", err)
	}
	if resultString(result, "status") != "completed_degraded" || !slices.Contains(DegradedPublicTLSHosts(result), "app-auth.kombify.me") {
		t.Fatalf("status=%q public TLS hosts=%v, want completed_degraded with public TLS named for repair", resultString(result, "status"), DegradedPublicTLSHosts(result))
	}
}
