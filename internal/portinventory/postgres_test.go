package portinventory

import (
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresAuthorityRejectsStaleServerGeneration(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresAdmissionRequest("stack-a", "plan-a")
	expectServerFence(mock, request.ServerRef, int64(4), "active")
	mock.ExpectRollback()

	_, err = NewPostgresAuthority(database).Admit(t.Context(), request)
	var stale *StaleServerGenerationError
	if !errors.As(err, &stale) || !errors.Is(err, ErrStaleServerGeneration) {
		t.Fatalf("Admit() error = %v, want typed stale generation", err)
	}
	if stale.RequestedGeneration != 3 || stale.ActualGeneration != 4 {
		t.Fatalf("stale generation = requested %d actual %d", stale.RequestedGeneration, stale.ActualGeneration)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityAtomicallyAdmitsClaimGeneration(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresAdmissionRequest("stack-a", "plan-a")
	expectServerFence(mock, request.ServerRef, int64(3), "active")
	expectEmptyInventory(mock, request.ServerRef)
	mock.ExpectExec("INSERT INTO server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-a", admissionDigest(mustNormalizeAdmission(t, request))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO server_port_reservations").
		WithArgs(sqlmock.AnyArg(), "tenant-a", "server-a", int64(3), "tcp", "*", int64(443), "exclusive", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO server_port_reservation_claims").
		WithArgs(sqlmock.AnyArg(), "tenant-a", sqlmock.AnyArg(), "server-a", int64(3), "stack-a", "plan-a", "https", "node-web", "public", `["route-a","route-b"]`, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	admission, err := NewPostgresAuthority(database).Admit(t.Context(), request)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if len(admission.Claims) != 1 || admission.Claims[0].State != ClaimStatePending {
		t.Fatalf("Admit() = %#v, want one pending claim", admission)
	}
	if admission.Claims[0].Requirement.BindAddress != "*" {
		t.Fatalf("normalized bind address = %q, want *", admission.Claims[0].Requirement.BindAddress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityAtomicallyAdmitsEmptyClaimGeneration(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresAdmissionRequest("stack-a", "plan-empty")
	request.Requirements = nil
	expectServerFence(mock, request.ServerRef, int64(3), "active")
	expectEmptyInventory(mock, request.ServerRef)
	mock.ExpectExec("INSERT INTO server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-empty", admissionDigest(mustNormalizeAdmission(t, request))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	admission, err := NewPostgresAuthority(database).Admit(t.Context(), request)
	if err != nil {
		t.Fatalf("Admit(empty) error = %v", err)
	}
	if len(admission.Claims) != 0 {
		t.Fatalf("Admit(empty) claims = %d, want zero", len(admission.Claims))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityEvaluateCurrentIsReadOnlyAndResolvesCanonicalGeneration(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresCurrentAdmissionRequest("stack-a", "plan-a")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT generation, lifecycle_state[[:space:]]+FROM servers[[:space:]]+WHERE tenant_id = \\$1 AND id = \\$2 AND stack_id = \\$3 AND owner_subject_id = \\$4").
		WithArgs("tenant-a", "server-a", "stack-a", "owner-a").WillReturnRows(sqlmock.NewRows([]string{"generation", "lifecycle_state"}).AddRow(int64(9), "active"))
	expectEmptyInventory(mock, ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 9})
	mock.ExpectRollback()

	result, err := NewPostgresAuthority(database).EvaluateCurrent(t.Context(), request)
	if err != nil {
		t.Fatalf("EvaluateCurrent() error = %v", err)
	}
	if result.GenerationRef.ServerGeneration != 9 || len(result.Admission.Claims) != 1 {
		t.Fatalf("EvaluateCurrent() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityAdmitCurrentResolvesAndPersistsCanonicalGenerationAtomically(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresCurrentAdmissionRequest("stack-a", "plan-a")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).WithArgs("tenant-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT generation, lifecycle_state[[:space:]]+FROM servers[[:space:]]+WHERE tenant_id = \\$1 AND id = \\$2 AND stack_id = \\$3 AND owner_subject_id = \\$4[[:space:]]+FOR UPDATE").
		WithArgs("tenant-a", "server-a", "stack-a", "owner-a").WillReturnRows(sqlmock.NewRows([]string{"generation", "lifecycle_state"}).AddRow(int64(9), "active"))
	serverRef := ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 9}
	expectEmptyInventory(mock, serverRef)
	normalized := mustNormalizeAdmission(t, AdmissionRequest{ServerRef: serverRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash, Requirements: request.Requirements})
	mock.ExpectExec("INSERT INTO server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(9), "stack-a", "plan-a", admissionDigest(normalized)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO server_port_reservations").
		WithArgs(sqlmock.AnyArg(), "tenant-a", "server-a", int64(9), "tcp", "*", int64(443), "exclusive", "").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO server_port_reservation_claims").
		WithArgs(sqlmock.AnyArg(), "tenant-a", sqlmock.AnyArg(), "server-a", int64(9), "stack-a", "plan-a", "https", "node-web", "public", `["route-a","route-b"]`, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(9), "stack-a", "plan-a").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("pending"))
	mock.ExpectCommit()

	result, err := NewPostgresAuthority(database).AdmitCurrent(t.Context(), request)
	if err != nil {
		t.Fatalf("AdmitCurrent() error = %v", err)
	}
	if result.GenerationRef.ServerGeneration != 9 || len(result.Admission.Claims) != 1 {
		t.Fatalf("AdmitCurrent() = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityFailsClosedOnWildcardOverlap(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	request := postgresAdmissionRequest("stack-a", "plan-a")
	request.Requirements[0].BindAddress = "127.0.0.1"
	expectServerFence(mock, request.ServerRef, int64(3), "active")
	mock.ExpectQuery("SELECT id, transport, bind_address, port, sharing, listener_group_ref, state[[:space:]]+FROM server_port_reservations").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transport", "bind_address", "port", "sharing", "listener_group_ref", "state"}).
			AddRow("reservation-existing", "tcp", "*", int64(443), "exclusive", "", "reserved"))
	mock.ExpectQuery("SELECT stack_id, resolved_plan_hash, claim_set_digest, state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"stack_id", "resolved_plan_hash", "claim_set_digest", "state"}).
			AddRow("stack-b", "plan-b", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "active"))
	mock.ExpectQuery("SELECT c.id, c.reservation_id, c.stack_id, c.resolved_plan_hash").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "reservation_id", "stack_id", "resolved_plan_hash", "requirement_id", "node_ref",
			"transport", "bind_address", "port", "sharing", "listener_group_ref", "exposure", "source_route_refs_json",
		}).AddRow("claim-existing", "reservation-existing", "stack-b", "plan-b", "https", "node-b", "tcp", "*", int64(443), "exclusive", "", "public", `[]`))
	mock.ExpectRollback()

	_, err = NewPostgresAuthority(database).Admit(t.Context(), request)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Admit() error = %v, want ConflictError", err)
	}
	if conflict.BindAddress != "127.0.0.1" || conflict.Port != 443 || conflict.Retryable {
		t.Fatalf("conflict = %#v", conflict)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityMarksExactGenerationMutationStarted(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ref := GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   "stack-a", ResolvedPlanHash: "plan-a",
	}
	expectServerFence(mock, ref.ServerRef, int64(3), "active")
	mock.ExpectQuery("SELECT state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-a").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("pending"))
	mock.ExpectExec("UPDATE server_port_claim_generations[[:space:]]+SET state = 'mutating'").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := NewPostgresAuthority(database).MarkMutationStarted(t.Context(), ref); err != nil {
		t.Fatalf("MarkMutationStarted() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityActivatesOneGenerationAndReleasesUnusedHeads(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ref := GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   "stack-a", ResolvedPlanHash: "plan-new",
	}
	expectServerFence(mock, ref.ServerRef, int64(3), "active")
	mock.ExpectQuery("SELECT state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-new").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("mutating"))
	mock.ExpectExec("UPDATE server_port_claim_generations[[:space:]]+SET state = 'active'").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-new").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE server_port_claim_generations[[:space:]]+SET state = 'released'").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-new").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE server_port_reservations AS reservation").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := NewPostgresAuthority(database).Activate(t.Context(), ref); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityReleasesExactGenerationAfterServerTeardown(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ref := GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   "stack-a", ResolvedPlanHash: "plan-a",
	}
	expectServerFence(mock, ref.ServerRef, int64(3), "decommissioned")
	mock.ExpectQuery("SELECT state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-a").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("active"))
	mock.ExpectExec("UPDATE server_port_claim_generations[[:space:]]+SET state = 'released'").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-a", ClaimStateActive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE server_port_reservations AS reservation").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := NewPostgresAuthority(database).ReleaseAfterTeardown(t.Context(), ref); err != nil {
		t.Fatalf("ReleaseAfterTeardown() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityReleasesExactHistoricalGenerationAfterServerAdvances(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ref := GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   "stack-a", ResolvedPlanHash: "plan-old",
	}
	expectServerFence(mock, ref.ServerRef, int64(4), "active")
	mock.ExpectQuery("SELECT state[[:space:]]+FROM server_port_claim_generations").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-old").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("active"))
	mock.ExpectExec("UPDATE server_port_claim_generations[[:space:]]+SET state = 'released'").
		WithArgs("tenant-a", "server-a", int64(3), "stack-a", "plan-old", ClaimStateActive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE server_port_reservations AS reservation").
		WithArgs("tenant-a", "server-a", int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := NewPostgresAuthority(database).ReleaseAfterTeardown(t.Context(), ref); err != nil {
		t.Fatalf("ReleaseAfterTeardown(historical) error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresAuthorityRejectsReleaseForFutureServerGeneration(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ref := GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 4},
		StackID:   "stack-a", ResolvedPlanHash: "plan-future",
	}
	expectServerFence(mock, ref.ServerRef, int64(3), "active")
	mock.ExpectRollback()
	if err := NewPostgresAuthority(database).ReleaseAfterTeardown(t.Context(), ref); !errors.Is(err, ErrStaleServerGeneration) {
		t.Fatalf("ReleaseAfterTeardown(future) error = %v, want ErrStaleServerGeneration", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func postgresAdmissionRequest(stackID, planHash string) AdmissionRequest {
	return AdmissionRequest{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   stackID, ResolvedPlanHash: planHash,
		Requirements: []Requirement{{
			ID: "https", NodeRef: "node-web", Transport: TransportTCP,
			BindAddress: "0.0.0.0", Port: 443, Sharing: SharingExclusive,
			Exposure: ExposurePublic, SourceRouteRefs: []string{"route-b", "route-a"},
		}},
	}
}

func postgresCurrentAdmissionRequest(stackID, planHash string) CurrentAdmissionRequest {
	request := postgresAdmissionRequest(stackID, planHash)
	return CurrentAdmissionRequest{
		TenantID: request.TenantID, ServerID: request.ServerID, OwnerSubjectID: "owner-a", StackID: request.StackID,
		ResolvedPlanHash: request.ResolvedPlanHash, Requirements: request.Requirements,
	}
}

func expectServerFence(mock sqlmock.Sqlmock, ref ServerRef, actualGeneration int64, lifecycleState string) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config('app.tenant_id', $1, true)")).
		WithArgs(ref.TenantID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(postgresServerLockKey(ref)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT generation, lifecycle_state[[:space:]]+FROM servers").
		WithArgs(ref.TenantID, ref.ServerID).
		WillReturnRows(sqlmock.NewRows([]string{"generation", "lifecycle_state"}).AddRow(actualGeneration, lifecycleState))
}

func expectEmptyInventory(mock sqlmock.Sqlmock, ref ServerRef) {
	mock.ExpectQuery("SELECT id, transport, bind_address, port, sharing, listener_group_ref, state[[:space:]]+FROM server_port_reservations").
		WithArgs(ref.TenantID, ref.ServerID, ref.ServerGeneration).
		WillReturnRows(sqlmock.NewRows([]string{"id", "transport", "bind_address", "port", "sharing", "listener_group_ref", "state"}))
	mock.ExpectQuery("SELECT stack_id, resolved_plan_hash, claim_set_digest, state[[:space:]]+FROM server_port_claim_generations").
		WithArgs(ref.TenantID, ref.ServerID, ref.ServerGeneration).
		WillReturnRows(sqlmock.NewRows([]string{"stack_id", "resolved_plan_hash", "claim_set_digest", "state"}))
	mock.ExpectQuery("SELECT c.id, c.reservation_id, c.stack_id, c.resolved_plan_hash").
		WithArgs(ref.TenantID, ref.ServerID, ref.ServerGeneration).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "reservation_id", "stack_id", "resolved_plan_hash", "requirement_id", "node_ref",
			"transport", "bind_address", "port", "sharing", "listener_group_ref", "exposure", "source_route_refs_json",
		}))
}

func mustNormalizeAdmission(t *testing.T, request AdmissionRequest) AdmissionRequest {
	t.Helper()
	request, err := normalizeAdmission(request)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
