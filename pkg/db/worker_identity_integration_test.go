package db_test

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/google/uuid"
)

func TestIntegrationWorkerIdentityIsTenantScopedAtomicAndReplayProof(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	suffix := uuid.NewString()
	tenantID, workerID := "identity-tenant-"+suffix, "identity-worker-"+suffix
	claim := controlplane.WorkerEnrollmentClaim{
		Binding: controlplane.WorkerEnrollmentBinding{TenantID: tenantID, WorkerID: workerID, OwnerSubjectID: "owner-1", ServerID: "server-" + suffix, RuntimeAgentID: workerID},
		Worker: controlplane.Worker{
			ID: workerID, TenantID: tenantID, OwnerSubjectID: "owner-1", Hostname: "node", Status: "pending", Type: "runtime",
			Capabilities: map[string]any{"server_id": "server-" + suffix, "runtime_agent_id": workerID},
		},
	}
	if _, err := store.ClaimWorkerEnrollment(t.Context(), claim); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	issue := controlplane.WorkerIdentityIssue{
		TenantID: tenantID, WorkerID: workerID, AttemptID: "attempt-0123456789ab", PairingTokenHash: "pairing-" + suffix,
		Thumbprint: "thumbprint-a", PublicKeyJWK: `{"crv":"Ed25519","kty":"OKP","x":"a"}`, CredentialSHA256: strings.Repeat("a", 64), IssuedAt: now,
	}
	if identity, err := store.IssueWorkerIdentity(t.Context(), issue); err != nil || identity.State != controlplane.WorkerIdentityIssuedUnconfirmed {
		t.Fatalf("issue = %+v, %v", identity, err)
	}

	// Concurrent repetitions serialize on the identity row: every one replaces
	// its predecessor, so exactly the credential stored last remains valid.
	const repetitions = 8
	hashes := make([]string, repetitions)
	var group sync.WaitGroup
	for index := range repetitions {
		hashes[index] = strings.Repeat(string(rune('b'+index)), 64)
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.ReissueUnconfirmedWorkerIdentity(t.Context(), controlplane.WorkerIdentityReissue{
				TenantID: tenantID, WorkerID: workerID, AttemptID: issue.AttemptID, PairingTokenHash: issue.PairingTokenHash,
				Thumbprint: issue.Thumbprint, NewCredentialSHA256: hashes[index], Now: now, Deadline: now.Add(time.Minute),
			}); err != nil {
				t.Errorf("reissue %d: %v", index, err)
			}
		}()
	}
	group.Wait()
	worker, err := store.GetWorker(t.Context(), tenantID, workerID)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := worker.Resources[controlplane.WorkerCredentialTokenSHA256ResourceKey].(string)
	matches := 0
	for _, hash := range hashes {
		if hash == stored {
			matches++
		}
	}
	identity, err := store.GetWorkerIdentity(t.Context(), tenantID, workerID)
	if err != nil || matches != 1 || identity.CredentialEpoch != 1+repetitions {
		t.Fatalf("stored credential matches %d submitted hashes, epoch %v, err %v", matches, identity, err)
	}

	// Confirmation closes the redemption; a late repetition is refused, and so
	// is one after the deadline.
	if _, err := store.ConfirmWorkerIdentity(t.Context(), tenantID, workerID, issue.Thumbprint, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReissueUnconfirmedWorkerIdentity(t.Context(), controlplane.WorkerIdentityReissue{
		TenantID: tenantID, WorkerID: workerID, AttemptID: issue.AttemptID, PairingTokenHash: issue.PairingTokenHash,
		Thumbprint: issue.Thumbprint, NewCredentialSHA256: strings.Repeat("f", 64), Now: now, Deadline: now.Add(time.Minute),
	}); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("reissue after confirmation = %v, want conflict", err)
	}

	// Another tenant sees no binding, and cannot consume or read proofs.
	if _, err := store.GetWorkerIdentity(t.Context(), "other-tenant-"+suffix, workerID); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("cross-tenant read = %v, want not found", err)
	}

	// A proof identifier is accepted once and expires with its window.
	jti := "proof-" + suffix + "-0123456789"
	if err := store.ConsumeWorkerProof(t.Context(), tenantID, workerID, jti, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ConsumeWorkerProof(t.Context(), tenantID, workerID, jti, now.Add(time.Minute)); !errors.Is(err, controlplane.ErrWorkerProofReplayed) {
		t.Fatalf("replayed proof = %v", err)
	}
	expired := "expired-" + suffix + "-0123456789"
	if err := store.ConsumeWorkerProof(t.Context(), tenantID, workerID, expired, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.ConsumeWorkerProof(t.Context(), tenantID, workerID, jti+"-next", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ConsumeWorkerProof(t.Context(), tenantID, workerID, expired, now.Add(time.Minute)); err != nil {
		t.Fatalf("an expired identifier was kept instead of pruned: %v", err)
	}

	// Rotation keeps the predecessor for a bounded overlap; revocation ends it.
	rotated, err := store.RotateWorkerIdentity(t.Context(), controlplane.WorkerIdentityRotation{
		TenantID: tenantID, WorkerID: workerID, OldThumbprint: issue.Thumbprint, NewThumbprint: "thumbprint-b",
		NewPublicJWK: `{"crv":"Ed25519","kty":"OKP","x":"b"}`, Now: now, Overlap: time.Minute,
	})
	if err != nil || rotated.Thumbprint != "thumbprint-b" || !rotated.AcceptsThumbprint("thumbprint-a", now) ||
		rotated.AcceptsThumbprint("thumbprint-a", now.Add(2*time.Minute)) {
		t.Fatalf("rotation = %+v, %v", rotated, err)
	}
	if err := store.RevokeWorkerIdentity(t.Context(), tenantID, workerID, now); err != nil {
		t.Fatal(err)
	}
	if revoked, err := store.GetWorkerIdentity(t.Context(), tenantID, workerID); err != nil || revoked.AcceptsThumbprint("thumbprint-b", now) {
		t.Fatalf("revoked identity = %+v, %v", revoked, err)
	}

	// A legacy bearer worker's key is held pending for the exact credential that
	// authenticated the request; only the owner's approval of that key binds it,
	// a second key waits behind the first, and a stale credential is refused.
	legacyID := "identity-legacy-" + suffix
	legacyClaim := claim
	legacyClaim.Binding.WorkerID, legacyClaim.Binding.RuntimeAgentID, legacyClaim.Binding.ServerID = legacyID, legacyID, "server-legacy-"+suffix
	legacyClaim.Worker.ID = legacyID
	legacyClaim.Worker.Capabilities = map[string]any{"server_id": "server-legacy-" + suffix, "runtime_agent_id": legacyID}
	legacyClaim.Worker.Resources = map[string]any{controlplane.WorkerCredentialTokenSHA256ResourceKey: strings.Repeat("c", 64)}
	if _, err := store.ClaimWorkerEnrollment(t.Context(), legacyClaim); err != nil {
		t.Fatal(err)
	}
	bind := controlplane.WorkerIdentityBind{
		TenantID: tenantID, WorkerID: legacyID, AttemptID: "attempt-legacy-0123456789", Thumbprint: "thumbprint-legacy",
		PublicKeyJWK: `{"crv":"Ed25519","kty":"OKP","x":"legacy"}`, ExpectedCredentialSHA256: strings.Repeat("d", 64), Now: now,
	}
	if _, err := store.BindWorkerIdentity(t.Context(), bind); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("bind with a stale credential = %v, want conflict", err)
	}
	bind.ExpectedCredentialSHA256 = strings.Repeat("c", 64)
	if pending, err := store.BindWorkerIdentity(t.Context(), bind); err != nil || pending.State != controlplane.WorkerIdentityUpgradePending ||
		pending.AcceptsThumbprint("thumbprint-legacy", now) {
		t.Fatalf("bind = %+v, %v; want a pending key that authenticates nothing", pending, err)
	}
	if _, err := store.BindWorkerIdentity(t.Context(), bind); err != nil {
		t.Fatalf("repeating the same bind must be idempotent: %v", err)
	}
	other := bind
	other.Thumbprint, other.PublicKeyJWK = "thumbprint-other", `{"crv":"Ed25519","kty":"OKP","x":"other"}`
	if _, err := store.BindWorkerIdentity(t.Context(), other); !errors.Is(err, controlplane.ErrWorkerIdentityUpgradePending) {
		t.Fatalf("a second key while one is pending = %v", err)
	}
	if listed, err := store.ListWorkerIdentityUpgrades(t.Context(), tenantID); err != nil || len(listed) != 1 || listed[0].Thumbprint != "thumbprint-legacy" {
		t.Fatalf("pending list = %+v, %v", listed, err)
	}
	decision := controlplane.WorkerIdentityUpgradeDecision{TenantID: tenantID, WorkerID: legacyID, Thumbprint: "thumbprint-other", ActorID: "owner-1", Now: now}
	if _, err := store.ApproveWorkerIdentityUpgrade(t.Context(), decision); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("approving a key that is not pending = %v", err)
	}
	decision.Thumbprint = "thumbprint-legacy"
	lapsed := decision
	lapsed.Now = now.Add(controlplane.WorkerIdentityUpgradeWindow + time.Minute)
	if _, err := store.ApproveWorkerIdentityUpgrade(t.Context(), lapsed); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("approving an expired request = %v", err)
	}
	approved, err := store.ApproveWorkerIdentityUpgrade(t.Context(), decision)
	if err != nil || approved.State != controlplane.WorkerIdentityBound || approved.ApprovedBy != "owner-1" || !approved.AcceptsThumbprint("thumbprint-legacy", now) {
		t.Fatalf("approval = %+v, %v", approved, err)
	}
	if _, err := store.BindWorkerIdentity(t.Context(), other); !errors.Is(err, controlplane.ErrWorkerIdentityBound) {
		t.Fatalf("second key after approval = %v, want already bound", err)
	}
	if err := store.RejectWorkerIdentityUpgrade(t.Context(), tenantID, legacyID); err != nil {
		t.Fatal(err)
	}
	if kept, err := store.GetWorkerIdentity(t.Context(), tenantID, legacyID); err != nil || kept.State != controlplane.WorkerIdentityBound {
		t.Fatalf("rejecting must not touch a bound key: %+v, %v", kept, err)
	}
}
