package backupstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeStoreProvisioner struct {
	store       *Store
	provisioned int
	revoked     []string
	revokeErr   error
}

func (f *fakeStoreProvisioner) Provision(context.Context, string) (*Store, error) {
	f.provisioned++
	return f.store, nil
}
func (f *fakeStoreProvisioner) RevokeToken(_ context.Context, tokenID string) error {
	f.revoked = append(f.revoked, tokenID)
	return f.revokeErr
}

type fakeCustodyRepository struct {
	existing    CustodyEvidence
	evidenceErr error
	upserted    Credentials
	upsertErr   error
}

func (f *fakeCustodyRepository) Evidence(context.Context, string, string) (CustodyEvidence, error) {
	return f.existing, f.evidenceErr
}
func (f *fakeCustodyRepository) Upsert(_ context.Context, credentials Credentials) (CustodyEvidence, error) {
	f.upserted = credentials
	return CustodyEvidence{BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"), ObservedAt: time.Now()}, f.upsertErr
}

func TestCustodianReusesVerifiedCustody(t *testing.T) {
	want := CustodyEvidence{BindingEvidence: []byte("existing"), ObservedAt: time.Now()}
	provisioner := &fakeStoreProvisioner{}
	repository := &fakeCustodyRepository{existing: want}
	custodian, _ := NewCustodian(provisioner, repository)
	got, err := custodian.Ensure(context.Background(), "tenant-a", "stack-a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.BindingEvidence) != "existing" || provisioner.provisioned != 0 {
		t.Fatalf("existing custody was not reused: %#v provisioned=%d", got, provisioner.provisioned)
	}
}

func TestCustodianPersistsBeforeReturningEvidence(t *testing.T) {
	provisioner := &fakeStoreProvisioner{store: &Store{Bucket: "bucket", Endpoint: "https://r2.example", TokenID: "token", AccessKeyID: "access", SecretAccessKey: "secret"}}
	repository := &fakeCustodyRepository{evidenceErr: ErrCustodyNotFound}
	custodian, _ := NewCustodian(provisioner, repository)
	custodian.password = func() (string, error) { return "repo-password", nil }
	evidence, err := custodian.Ensure(context.Background(), "tenant-a", "stack-a")
	if err != nil {
		t.Fatal(err)
	}
	if string(evidence.BindingEvidence) != "binding" || repository.upserted.SecretAccessKey != "secret" || repository.upserted.RepositoryPassword != "repo-password" {
		t.Fatalf("custody was not persisted exactly: %#v %#v", evidence, repository.upserted)
	}
	if len(provisioner.revoked) != 0 {
		t.Fatalf("successful token was revoked: %v", provisioner.revoked)
	}
}

func TestCustodianRevokesOnlyFreshTokenWhenPersistenceFails(t *testing.T) {
	provisioner := &fakeStoreProvisioner{store: &Store{TokenID: "fresh-token"}}
	repository := &fakeCustodyRepository{evidenceErr: ErrCustodyNotFound, upsertErr: errors.New("database unavailable")}
	custodian, _ := NewCustodian(provisioner, repository)
	custodian.password = func() (string, error) { return "repo-password", nil }
	_, err := custodian.Ensure(context.Background(), "tenant-a", "stack-a")
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("expected persistence failure, got %v", err)
	}
	if len(provisioner.revoked) != 1 || provisioner.revoked[0] != "fresh-token" {
		t.Fatalf("fresh token was not revoked: %v", provisioner.revoked)
	}
}

func TestGeneratedRepositoryPasswordHasFullEntropy(t *testing.T) {
	password, err := generateRepositoryPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 43 {
		t.Fatalf("repository password too short: %d", len(password))
	}
}
