package clientpairing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTenant      = "tenant-home"
	testInstance    = "11111111-2222-4333-8444-555555555555"
	testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var testNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

type captureStore struct {
	created Code
}

func (s *captureStore) Create(_ context.Context, code Code) error {
	s.created = code
	return nil
}

func (*captureStore) Consume(context.Context, ConsumeRequest) (*Code, error) {
	return nil, errors.New("not implemented")
}

func TestIssueMatchesCoreEnvelopeAndPersistsOnlyHash(t *testing.T) {
	store := &captureStore{}
	service, err := New(store, WithClock(func() time.Time { return testNow }), WithEntropy(bytes.NewReader(bytes.Repeat([]byte{0x7a}, codeEntropyBytes))))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := service.Issue(context.Background(), IssueRequest{
		TenantID:             testTenant,
		InstanceID:           testInstance,
		IssuedBySubjectID:    "owner-1",
		Endpoint:             "https://techstack.home.example" + RedeemPath,
		TLSFingerprintSHA256: testFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Version != Version || envelope.Endpoint != "https://techstack.home.example"+RedeemPath {
		t.Fatalf("envelope identity = %#v", envelope)
	}
	if envelope.IssuedAt != testNow || envelope.ExpiresAt.Sub(envelope.IssuedAt) != DefaultLifetime {
		t.Fatalf("envelope lifetime = %s", envelope.ExpiresAt.Sub(envelope.IssuedAt))
	}
	if got, err := tenantFromCode(envelope.OneTimeCode); err != nil || got != testTenant {
		t.Fatalf("tenantFromCode() = %q, %v", got, err)
	}
	if envelope.OneTimeCode == store.created.CodeHash || store.created.CodeHash == "" {
		t.Fatal("store must receive a non-empty hash, never the raw code")
	}
	sum := sha256.Sum256([]byte(envelope.OneTimeCode))
	if store.created.CodeHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("stored hash = %q", store.created.CodeHash)
	}
	if store.created.TenantID != testTenant || store.created.InstanceID != testInstance || store.created.TLSFingerprintSHA256 != testFingerprint {
		t.Fatalf("stored binding = %#v", store.created)
	}
}

func TestIssueRejectsUnsafeEndpointAndLifetimeOverTenMinutes(t *testing.T) {
	service, err := New(NewMemoryStore(), WithClock(func() time.Time { return testNow }))
	if err != nil {
		t.Fatal(err)
	}
	base := IssueRequest{
		TenantID: testTenant, InstanceID: testInstance, IssuedBySubjectID: "owner-1",
		Endpoint: "https://techstack.home.example" + RedeemPath, TLSFingerprintSHA256: testFingerprint,
	}
	unsafe := base
	unsafe.Endpoint = "http://techstack.home.example" + RedeemPath
	if _, err := service.Issue(context.Background(), unsafe); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe endpoint error = %v", err)
	}
	tooLong := base
	tooLong.Lifetime = MaxLifetime + time.Nanosecond
	if _, err := service.Issue(context.Background(), tooLong); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("overlong lifetime error = %v", err)
	}
}

func TestRedeemIsOneTimeAndBindingScoped(t *testing.T) {
	now := testNow
	service, err := New(NewMemoryStore(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	envelope := issueTestEnvelope(t, service)

	wrongInstance := RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: "99999999-2222-4333-8444-555555555555", TLSFingerprintSHA256: testFingerprint,
	}
	if _, err := service.Redeem(context.Background(), wrongInstance); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("wrong instance error = %v", err)
	}
	wrongFingerprint := RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: testInstance, TLSFingerprintSHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	if _, err := service.Redeem(context.Background(), wrongFingerprint); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("wrong fingerprint error = %v", err)
	}
	wrongTenant := RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: "another-tenant",
		InstanceID: testInstance, TLSFingerprintSHA256: testFingerprint,
	}
	if _, err := service.Redeem(context.Background(), wrongTenant); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("wrong tenant error = %v", err)
	}

	redeemed, err := service.Redeem(context.Background(), RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: testInstance, TLSFingerprintSHA256: testFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	if redeemed.ConsumedAt == nil || *redeemed.ConsumedAt != now {
		t.Fatalf("redeemed = %#v", redeemed)
	}
	if _, err := service.Redeem(context.Background(), RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: testInstance, TLSFingerprintSHA256: testFingerprint,
	}); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("replay error = %v", err)
	}
}

func TestRedeemRejectsExpiredCode(t *testing.T) {
	now := testNow
	service, err := New(NewMemoryStore(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	envelope := issueTestEnvelope(t, service)
	now = envelope.ExpiresAt
	_, err = service.Redeem(context.Background(), RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: testInstance, TLSFingerprintSHA256: testFingerprint,
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry error = %v", err)
	}
}

func TestConcurrentRedeemHasExactlyOneSuccess(t *testing.T) {
	service, err := New(NewMemoryStore(), WithClock(func() time.Time { return testNow }))
	if err != nil {
		t.Fatal(err)
	}
	envelope := issueTestEnvelope(t, service)
	request := RedeemRequest{
		OneTimeCode: envelope.OneTimeCode, ExpectedTenantID: testTenant,
		InstanceID: testInstance, TLSFingerprintSHA256: testFingerprint,
	}

	const workers = 32
	var successes atomic.Int32
	var replays atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Redeem(context.Background(), request)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrAlreadyConsumed):
				replays.Add(1)
			default:
				t.Errorf("unexpected redeem error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 || replays.Load() != workers-1 {
		t.Fatalf("successes=%d replays=%d", successes.Load(), replays.Load())
	}
}

func issueTestEnvelope(t *testing.T, service *Service) Envelope {
	t.Helper()
	envelope, err := service.Issue(context.Background(), IssueRequest{
		TenantID: testTenant, InstanceID: testInstance, IssuedBySubjectID: "owner-1",
		Endpoint: "https://techstack.home.example" + RedeemPath, TLSFingerprintSHA256: testFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
