package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/google/uuid"
)

func recommendationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The recommendation lifecycle on the live managed Postgres: one live record
// per advice and scope, acceptance that revalidates, supersession on a
// material change, rejection that suppresses the same decision, and governed
// action listings that never show recommendations.
func TestIntegrationLayoutRecommendationLifecycle(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	database, err := Open(Config{Backend: StoreBackendPostgres, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	tenantID, ownerID := "rec-tenant-"+suffix, "auth0|rec-owner-"+suffix
	if _, err := database.ExecContext(ctx, `INSERT INTO techstack_tenants (id, display_name) VALUES ($1,'Recommendation tenant')`, tenantID); err != nil {
		t.Fatal(err)
	}
	authority := actions.NewPostgresAuthority(database.DB)
	now := time.Now().UTC()
	proposal := func(name, material string) actions.ProposeRecommendation {
		return actions.ProposeRecommendation{
			TenantID: tenantID, OwnerSubjectID: ownerID, Scope: "stack:test-" + suffix, Title: "Recommended distribution",
			ProposalDigest: recommendationDigest("proposal:" + name), SnapshotDigest: recommendationDigest("snapshot:" + name),
			MaterialDigest: recommendationDigest("material:" + material), Payload: []byte(`{"option":"` + name + `"}`), Now: now, Verified: true,
		}
	}

	first, err := authority.Propose(ctx, proposal("a", "a"))
	if err != nil || first.State != actions.RecommendationOpen {
		t.Fatalf("propose: %v %+v", err, first)
	}
	again, err := authority.Propose(ctx, proposal("a", "a"))
	if err != nil || again.ID != first.ID {
		t.Fatalf("the same advice must stay one record: %v %s vs %s", err, again.ID, first.ID)
	}
	second, err := authority.Propose(ctx, proposal("b", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if old, _ := authority.GetRecommendation(ctx, tenantID, ownerID, first.ID, now); old.State != actions.RecommendationSuperseded {
		t.Fatalf("older advice in the scope must be superseded, got %s", old.State)
	}

	// An equal decision on a fresh snapshot accepts; a second accept replays.
	fresh := proposal("b-fresh", "b")
	outcome, err := authority.Accept(ctx, actions.AcceptRecommendation{TenantID: tenantID, OwnerSubjectID: ownerID, ID: second.ID, ProposalDigest: second.ProposalDigest, Fresh: fresh, CorrelationID: "run-" + suffix, Now: now})
	if err != nil || outcome.Accepted == nil || outcome.Accepted.State != actions.RecommendationAccepted || outcome.Accepted.Acceptance.RevalidatedSnapshotDigest != fresh.SnapshotDigest {
		t.Fatalf("accept: %v %+v", err, outcome)
	}
	replay, err := authority.Accept(ctx, actions.AcceptRecommendation{TenantID: tenantID, OwnerSubjectID: ownerID, ID: second.ID, ProposalDigest: second.ProposalDigest, Fresh: fresh, CorrelationID: "run-" + suffix, Now: now})
	if err != nil || !replay.Replayed {
		t.Fatalf("accept replay: %v %+v", err, replay)
	}
	if _, err := authority.MarkExecuting(ctx, tenantID, ownerID, second.ID, "job-"+suffix, now); err != nil {
		t.Fatal(err)
	}
	done, err := authority.Finish(ctx, tenantID, ownerID, second.ID, true, "", now)
	if err != nil || done.State != actions.RecommendationDone || done.ExecutionID != "job-"+suffix {
		t.Fatalf("finish: %v %+v", err, done)
	}

	// A material change at acceptance supersedes and asks again.
	third, err := authority.Propose(ctx, proposal("c", "c"))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := authority.Accept(ctx, actions.AcceptRecommendation{TenantID: tenantID, OwnerSubjectID: ownerID, ID: third.ID, ProposalDigest: third.ProposalDigest, Fresh: proposal("d", "d"), Now: now})
	if err != nil || changed.Accepted != nil || changed.Superseded.State != actions.RecommendationSuperseded || changed.Replacement.State != actions.RecommendationOpen || changed.Superseded.SupersededBy != changed.Replacement.ID {
		t.Fatalf("material change: %v %+v", err, changed)
	}
	if _, err := authority.Accept(ctx, actions.AcceptRecommendation{TenantID: tenantID, OwnerSubjectID: ownerID, ID: changed.Replacement.ID, ProposalDigest: third.ProposalDigest, Fresh: proposal("d", "d"), Now: now}); !errors.Is(err, actions.ErrRecommendationDigest) {
		t.Fatalf("accepting advice that was not shown must fail, got %v", err)
	}

	// Rejection suppresses the same decision.
	if _, err := authority.Reject(ctx, tenantID, ownerID, changed.Replacement.ID, "", now); err != nil {
		t.Fatal(err)
	}
	suppressed, err := authority.Propose(ctx, proposal("d-again", "d"))
	if err != nil || suppressed.State != actions.RecommendationRejected {
		t.Fatalf("rejected decision must stay suppressed: %v %+v", err, suppressed)
	}

	cards, err := authority.List(ctx, tenantID, ownerID)
	if err != nil || len(cards) != 0 {
		t.Fatalf("governed listings must not show recommendations: %v %d", err, len(cards))
	}
	records, err := authority.ListRecommendations(ctx, tenantID, ownerID, "", now)
	if err != nil || len(records) < 5 {
		t.Fatalf("list: %v %d", err, len(records))
	}
}
