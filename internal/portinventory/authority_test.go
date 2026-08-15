package portinventory

import (
	"context"
	"errors"
	"testing"
)

func TestAuthorityRejectsConflictingHostPortBeforeMutation(t *testing.T) {
	authority := NewMemoryAuthority()
	first := admissionRequest("stack-a", "plan-a", Requirement{
		ID:          "https",
		Transport:   TransportTCP,
		BindAddress: "0.0.0.0",
		Port:        443,
		Sharing:     SharingExclusive,
		Exposure:    ExposurePublic,
	})
	if _, err := authority.Admit(context.Background(), first); err != nil {
		t.Fatalf("Admit(first) error = %v", err)
	}

	second := admissionRequest("stack-b", "plan-b", Requirement{
		ID:          "admin",
		Transport:   TransportTCP,
		BindAddress: "*",
		Port:        443,
		Sharing:     SharingExclusive,
		Exposure:    ExposureRemotePrivate,
	})
	_, err := authority.Admit(context.Background(), second)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Admit(second) error = %v, want ConflictError", err)
	}
	if conflict.ErrorCode != ErrorCodeAllocationConflict || conflict.ReasonCode != ReasonCodeHostPortReserved {
		t.Fatalf("conflict codes = %q/%q", conflict.ErrorCode, conflict.ReasonCode)
	}
	if conflict.UserGuidance.Title == "" || len(conflict.UserGuidance.NextSteps) == 0 {
		t.Fatalf("conflict guidance = %+v, want actionable next steps", conflict.UserGuidance)
	}

	snapshot := authority.Snapshot(ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3})
	if got := len(snapshot.Claims); got != 1 {
		t.Fatalf("claims after rejected admission = %d, want 1", got)
	}
}

func TestAuthorityAllowsExactSuccessorClaimForSameStack(t *testing.T) {
	authority := NewMemoryAuthority()
	requirement := Requirement{
		ID:          "https",
		Transport:   TransportTCP,
		BindAddress: "0.0.0.0",
		Port:        443,
		Sharing:     SharingExclusive,
		Exposure:    ExposurePublic,
	}
	first, err := authority.Admit(context.Background(), admissionRequest("stack-a", "plan-a", requirement))
	if err != nil {
		t.Fatalf("Admit(first) error = %v", err)
	}
	firstRef := GenerationRef{ServerRef: admissionRequest("stack-a", "plan-a").ServerRef, StackID: "stack-a", ResolvedPlanHash: "plan-a"}
	if err := authority.MarkMutationStarted(context.Background(), firstRef); err != nil {
		t.Fatalf("MarkMutationStarted(first) error = %v", err)
	}
	if err := authority.Activate(context.Background(), firstRef); err != nil {
		t.Fatalf("Activate(first) error = %v", err)
	}
	second, err := authority.Admit(context.Background(), admissionRequest("stack-a", "plan-b", requirement))
	if err != nil {
		t.Fatalf("Admit(successor) error = %v", err)
	}
	if first.Claims[0].ReservationID != second.Claims[0].ReservationID {
		t.Fatalf("successor reservation = %q, want reused %q", second.Claims[0].ReservationID, first.Claims[0].ReservationID)
	}
}

func TestAuthorityDoesNotReuseExclusiveReservationFromUncertainGeneration(t *testing.T) {
	authority := NewMemoryAuthority()
	requirement := Requirement{ID: "https", Transport: TransportTCP, BindAddress: "*", Port: 443, Sharing: SharingExclusive, Exposure: ExposurePublic}
	firstReq := admissionRequest("stack-a", "plan-a", requirement)
	if _, err := authority.Admit(context.Background(), firstReq); err != nil {
		t.Fatalf("Admit(first) error = %v", err)
	}
	firstRef := GenerationRef{ServerRef: firstReq.ServerRef, StackID: firstReq.StackID, ResolvedPlanHash: firstReq.ResolvedPlanHash}
	if err := authority.MarkMutationStarted(context.Background(), firstRef); err != nil {
		t.Fatalf("MarkMutationStarted(first) error = %v", err)
	}
	if err := authority.MarkUncertain(context.Background(), firstRef); err != nil {
		t.Fatalf("MarkUncertain(first) error = %v", err)
	}
	requirement.ID = "https-successor"
	if _, err := authority.Admit(context.Background(), admissionRequest("stack-a", "plan-b", requirement)); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Admit(successor over uncertain) error = %v, want ErrAllocationConflict", err)
	}
}

func TestAuthorityRejectsDuplicateExclusiveSocketInsideOnePlan(t *testing.T) {
	authority := NewMemoryAuthority()
	first := Requirement{ID: "listener-a", Transport: TransportTCP, BindAddress: "*", Port: 443, Sharing: SharingExclusive, Exposure: ExposurePublic}
	second := first
	second.ID = "listener-b"
	if _, err := authority.Admit(context.Background(), admissionRequest("stack-a", "plan-a", first, second)); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Admit(duplicate exclusive socket) error = %v, want ErrAllocationConflict", err)
	}
}

func TestAuthoritySharesOnlyCompilerOwnedListenerGroup(t *testing.T) {
	authority := NewMemoryAuthority()
	shared := Requirement{
		ID:               "route-a",
		Transport:        TransportTCP,
		BindAddress:      "0.0.0.0",
		Port:             443,
		Sharing:          SharingVirtualHost,
		ListenerGroupRef: "edge-main",
		Exposure:         ExposurePublic,
	}
	if _, err := authority.Admit(context.Background(), admissionRequest("stack-a", "plan-a", shared)); err != nil {
		t.Fatalf("Admit(first shared) error = %v", err)
	}
	shared.ID = "route-b"
	if _, err := authority.Admit(context.Background(), admissionRequest("stack-b", "plan-b", shared)); err != nil {
		t.Fatalf("Admit(same listener group) error = %v", err)
	}
	shared.ID = "route-c"
	shared.ListenerGroupRef = "edge-other"
	if _, err := authority.Admit(context.Background(), admissionRequest("stack-c", "plan-c", shared)); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Admit(other listener group) error = %v, want ErrAllocationConflict", err)
	}
}

func TestAuthorityRetainsReservationAfterMutationBecomesUncertain(t *testing.T) {
	authority := NewMemoryAuthority()
	req := admissionRequest("stack-a", "plan-a", Requirement{
		ID:          "http",
		Transport:   TransportTCP,
		BindAddress: "127.0.0.1",
		Port:        8080,
		Sharing:     SharingExclusive,
		Exposure:    ExposureLocal,
	})
	if _, err := authority.Admit(context.Background(), req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	ref := GenerationRef{ServerRef: req.ServerRef, StackID: req.StackID, ResolvedPlanHash: req.ResolvedPlanHash}
	if err := authority.MarkMutationStarted(context.Background(), ref); err != nil {
		t.Fatalf("MarkMutationStarted() error = %v", err)
	}
	if err := authority.MarkUncertain(context.Background(), ref); err != nil {
		t.Fatalf("MarkUncertain() error = %v", err)
	}

	other := admissionRequest("stack-b", "plan-b", req.Requirements[0])
	other.Requirements[0].ID = "other-http"
	if _, err := authority.Admit(context.Background(), other); !errors.Is(err, ErrAllocationConflict) {
		t.Fatalf("Admit(other after uncertain) error = %v, want ErrAllocationConflict", err)
	}
	if got := authority.Snapshot(req.ServerRef).Claims[0].State; got != ClaimStateUncertain {
		t.Fatalf("claim state = %q, want %q", got, ClaimStateUncertain)
	}
}

func TestAuthorityAbortBeforeMutationAndExactReleaseDoNotFreeNewerGeneration(t *testing.T) {
	authority := NewMemoryAuthority()
	requirement := Requirement{
		ID:          "http",
		Transport:   TransportTCP,
		BindAddress: "0.0.0.0",
		Port:        8080,
		Sharing:     SharingExclusive,
		Exposure:    ExposureRemotePrivate,
	}
	oldReq := admissionRequest("stack-a", "plan-a", requirement)
	if _, err := authority.Admit(context.Background(), oldReq); err != nil {
		t.Fatalf("Admit(old) error = %v", err)
	}
	oldRef := GenerationRef{ServerRef: oldReq.ServerRef, StackID: oldReq.StackID, ResolvedPlanHash: oldReq.ResolvedPlanHash}
	if err := authority.AbortBeforeMutation(context.Background(), oldRef); err != nil {
		t.Fatalf("AbortBeforeMutation() error = %v", err)
	}

	newReq := admissionRequest("stack-a", "plan-b", requirement)
	if _, err := authority.Admit(context.Background(), newReq); err != nil {
		t.Fatalf("Admit(new) error = %v", err)
	}
	newRef := GenerationRef{ServerRef: newReq.ServerRef, StackID: newReq.StackID, ResolvedPlanHash: newReq.ResolvedPlanHash}
	if err := authority.MarkMutationStarted(context.Background(), newRef); err != nil {
		t.Fatalf("MarkMutationStarted(new) error = %v", err)
	}
	if err := authority.Activate(context.Background(), newRef); err != nil {
		t.Fatalf("Activate(new) error = %v", err)
	}
	if err := authority.ReleaseAfterTeardown(context.Background(), oldRef); err != nil {
		t.Fatalf("ReleaseAfterTeardown(old replay) error = %v", err)
	}

	snapshot := authority.Snapshot(newReq.ServerRef)
	if got := len(snapshot.Reservations); got != 1 {
		t.Fatalf("reservations after stale release = %d, want 1", got)
	}
	if got := snapshot.Claims[0].State; got != ClaimStateActive {
		t.Fatalf("new claim state = %q, want %q", got, ClaimStateActive)
	}
	if err := authority.ReleaseAfterTeardown(context.Background(), newRef); err != nil {
		t.Fatalf("ReleaseAfterTeardown(new) error = %v", err)
	}
	if got := len(authority.Snapshot(newReq.ServerRef).Reservations); got != 0 {
		t.Fatalf("reservations after exact release = %d, want 0", got)
	}
}

func TestAuthorityDoesNotReAdmitAReleasedExactGeneration(t *testing.T) {
	authority := NewMemoryAuthority()
	req := admissionRequest("stack-a", "plan-a", Requirement{
		ID:          "http",
		Transport:   TransportTCP,
		BindAddress: "*",
		Port:        8080,
		Sharing:     SharingExclusive,
		Exposure:    ExposureRemotePrivate,
	})
	if _, err := authority.Admit(context.Background(), req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	ref := GenerationRef{ServerRef: req.ServerRef, StackID: req.StackID, ResolvedPlanHash: req.ResolvedPlanHash}
	if err := authority.AbortBeforeMutation(context.Background(), ref); err != nil {
		t.Fatalf("AbortBeforeMutation() error = %v", err)
	}
	if _, err := authority.Admit(context.Background(), req); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Admit(released exact generation) error = %v, want ErrInvalidTransition", err)
	}
}

func TestAuthoritySealsExactPlanRequirementSet(t *testing.T) {
	authority := NewMemoryAuthority()
	req := admissionRequest("stack-a", "plan-a", Requirement{
		ID: "http", Transport: TransportTCP, BindAddress: "*", Port: 8080,
		Sharing: SharingExclusive, Exposure: ExposureRemotePrivate,
	})
	if _, err := authority.Admit(context.Background(), req); err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	changed := req
	changed.Requirements = append(append([]Requirement(nil), req.Requirements...), Requirement{
		ID: "metrics", Transport: TransportTCP, BindAddress: "127.0.0.1", Port: 9090,
		Sharing: SharingExclusive, Exposure: ExposureLocal,
	})
	if _, err := authority.Admit(context.Background(), changed); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Admit(changed exact plan) error = %v, want ErrInvalidRequest", err)
	}
}

func TestAuthorityEmptySuccessorReleasesSupersededReservations(t *testing.T) {
	authority := NewMemoryAuthority()
	first := admissionRequest("stack-a", "plan-a", Requirement{
		ID: "http", Transport: TransportTCP, BindAddress: "*", Port: 8080,
		Sharing: SharingExclusive, Exposure: ExposureRemotePrivate,
	})
	if _, err := authority.Admit(context.Background(), first); err != nil {
		t.Fatalf("Admit(first) error = %v", err)
	}
	firstRef := GenerationRef{ServerRef: first.ServerRef, StackID: first.StackID, ResolvedPlanHash: first.ResolvedPlanHash}
	if err := authority.MarkMutationStarted(context.Background(), firstRef); err != nil {
		t.Fatalf("MarkMutationStarted(first) error = %v", err)
	}
	if err := authority.Activate(context.Background(), firstRef); err != nil {
		t.Fatalf("Activate(first) error = %v", err)
	}

	empty := admissionRequest("stack-a", "plan-b")
	admitted, err := authority.Admit(context.Background(), empty)
	if err != nil {
		t.Fatalf("Admit(empty successor) error = %v", err)
	}
	if len(admitted.Claims) != 0 {
		t.Fatalf("Admit(empty successor) claims = %d, want zero", len(admitted.Claims))
	}
	if replay, replayErr := authority.Admit(context.Background(), empty); replayErr != nil || len(replay.Claims) != 0 {
		t.Fatalf("Admit(empty replay) = %#v, error %v", replay, replayErr)
	}
	changed := empty
	changed.Requirements = []Requirement{{
		ID: "metrics", Transport: TransportTCP, BindAddress: "127.0.0.1", Port: 9090,
		Sharing: SharingExclusive, Exposure: ExposureLocal,
	}}
	if _, err := authority.Admit(context.Background(), changed); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Admit(changed empty generation) error = %v, want ErrInvalidRequest", err)
	}
	emptyRef := GenerationRef{ServerRef: empty.ServerRef, StackID: empty.StackID, ResolvedPlanHash: empty.ResolvedPlanHash}
	if err := authority.MarkMutationStarted(context.Background(), emptyRef); err != nil {
		t.Fatalf("MarkMutationStarted(empty successor) error = %v", err)
	}
	if err := authority.Activate(context.Background(), emptyRef); err != nil {
		t.Fatalf("Activate(empty successor) error = %v", err)
	}
	snapshot := authority.Snapshot(empty.ServerRef)
	if len(snapshot.Reservations) != 0 || len(snapshot.Claims) != 0 || len(snapshot.ClaimGenerations) != 1 {
		t.Fatalf("Snapshot(empty successor) = %#v, want only one claim-free active generation", snapshot)
	}
}

func TestAuthorityDoesNotAliasCallerRequirementSlices(t *testing.T) {
	authority := NewMemoryAuthority()
	req := admissionRequest("stack-a", "plan-a", Requirement{
		ID: "http", Transport: TransportTCP, BindAddress: "*", Port: 8080,
		Sharing: SharingExclusive, Exposure: ExposureRemotePrivate,
		SourceRouteRefs: []string{"route-b", "route-a"},
	})
	admission, err := authority.Admit(context.Background(), req)
	if err != nil {
		t.Fatalf("Admit() error = %v", err)
	}
	if req.Requirements[0].SourceRouteRefs[0] != "route-b" {
		t.Fatalf("Admit mutated caller route refs: %+v", req.Requirements[0].SourceRouteRefs)
	}
	admission.Claims[0].Requirement.SourceRouteRefs[0] = "tampered"
	snapshot := authority.Snapshot(req.ServerRef)
	if got := snapshot.Claims[0].Requirement.SourceRouteRefs[0]; got != "route-a" {
		t.Fatalf("caller mutation changed authority state: %q", got)
	}
}

func TestAuthorityRejectsReleaseForUnknownGeneration(t *testing.T) {
	authority := NewMemoryAuthority()
	ref := GenerationRef{ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3}, StackID: "stack-a", ResolvedPlanHash: "never-admitted"}
	if err := authority.ReleaseAfterTeardown(context.Background(), ref); !errors.Is(err, ErrClaimGenerationNotFound) {
		t.Fatalf("ReleaseAfterTeardown(unknown) error = %v, want ErrClaimGenerationNotFound", err)
	}
}

func TestNormalizeRequirementRejectsProviderOrArtifactInferenceInputs(t *testing.T) {
	for _, tc := range []Requirement{
		{ID: "bad-port", Transport: TransportTCP, BindAddress: "*", Port: 0, Sharing: SharingExclusive, Exposure: ExposureRemotePrivate},
		{ID: "bad-group", Transport: TransportTCP, BindAddress: "*", Port: 443, Sharing: SharingVirtualHost, Exposure: ExposurePublic},
		{ID: "bad-transport", Transport: "http", BindAddress: "*", Port: 443, Sharing: SharingExclusive, Exposure: ExposurePublic},
	} {
		if _, err := NormalizeRequirement(tc); !errors.Is(err, ErrInvalidRequirement) {
			t.Fatalf("NormalizeRequirement(%+v) error = %v, want ErrInvalidRequirement", tc, err)
		}
	}
}

func admissionRequest(stackID, planHash string, requirements ...Requirement) AdmissionRequest {
	return AdmissionRequest{
		ServerRef: ServerRef{
			TenantID:         "tenant-a",
			ServerID:         "server-a",
			ServerGeneration: 3,
		},
		StackID:          stackID,
		ResolvedPlanHash: planHash,
		Requirements:     requirements,
	}
}
