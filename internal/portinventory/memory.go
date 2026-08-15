package portinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Authority is the rollout-facing lifecycle boundary. Every implementation
// must serialize Admit for one exact server generation and preserve uncertain
// claim generations until explicit reconciliation or exact teardown.
type Authority interface {
	Admit(context.Context, AdmissionRequest) (Admission, error)
	MarkMutationStarted(context.Context, GenerationRef) error
	Activate(context.Context, GenerationRef) error
	MarkUncertain(context.Context, GenerationRef) error
	AbortBeforeMutation(context.Context, GenerationRef) error
	ReleaseAfterTeardown(context.Context, GenerationRef) error
}

// CurrentAuthority is the rollout-facing durable seam. Evaluations are
// read-only; admissions resolve and persist against the canonical current
// server generation in one authority transaction.
type CurrentAuthority interface {
	EvaluateCurrent(context.Context, CurrentAdmissionRequest) (CurrentAdmission, error)
	AdmitCurrent(context.Context, CurrentAdmissionRequest) (CurrentAdmission, error)
	MarkMutationStarted(context.Context, GenerationRef) error
	Activate(context.Context, GenerationRef) error
	MarkUncertain(context.Context, GenerationRef) error
	AbortBeforeMutation(context.Context, GenerationRef) error
	ReleaseAfterTeardown(context.Context, GenerationRef) error
}

// MemoryAuthority is the executable contract model used by focused rollout
// tests. PostgreSQL loads and persists the same inventoryState decisions.
type MemoryAuthority struct {
	mu      sync.Mutex
	servers map[string]*inventoryState
}

type inventoryState struct {
	ref          ServerRef
	reservations map[string]Reservation
	generations  map[string]ClaimGeneration
	claims       map[string]Claim
}

func NewMemoryAuthority() *MemoryAuthority {
	return &MemoryAuthority{servers: make(map[string]*inventoryState)}
}

func (a *MemoryAuthority) Admit(ctx context.Context, request AdmissionRequest) (Admission, error) {
	if err := ctx.Err(); err != nil {
		return Admission{}, err
	}
	request, err := normalizeAdmission(request)
	if err != nil {
		return Admission{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := serverKey(request.ServerRef)
	working := cloneInventoryState(a.servers[key], request.ServerRef)
	result, err := admitState(working, request)
	if err != nil {
		return Admission{}, err
	}
	a.servers[key] = working
	return cloneAdmission(result), nil
}

func (a *MemoryAuthority) MarkMutationStarted(ctx context.Context, ref GenerationRef) error {
	return a.transition(ctx, ref, []ClaimState{ClaimStatePending, ClaimStateMutating}, ClaimStateMutating)
}

func (a *MemoryAuthority) Activate(ctx context.Context, ref GenerationRef) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.servers[serverKey(ref.ServerRef)]
	if state == nil {
		return ErrClaimGenerationNotFound
	}
	key := claimGenerationKey(ref)
	generation, ok := state.generations[key]
	if !ok {
		return ErrClaimGenerationNotFound
	}
	if generation.State != ClaimStateMutating && generation.State != ClaimStateActive {
		return ErrInvalidTransition
	}
	generation.State = ClaimStateActive
	state.generations[key] = generation
	for otherKey, other := range state.generations {
		if otherKey != key && other.StackID == ref.StackID && other.State == ClaimStateActive {
			other.State = ClaimStateReleased
			state.generations[otherKey] = other
		}
	}
	gcReservations(state)
	return nil
}

func (a *MemoryAuthority) MarkUncertain(ctx context.Context, ref GenerationRef) error {
	return a.transition(ctx, ref, []ClaimState{ClaimStateMutating, ClaimStateUncertain}, ClaimStateUncertain)
}

func (a *MemoryAuthority) AbortBeforeMutation(ctx context.Context, ref GenerationRef) error {
	return a.release(ctx, ref, ClaimStatePending)
}

func (a *MemoryAuthority) ReleaseAfterTeardown(ctx context.Context, ref GenerationRef) error {
	return a.release(ctx, ref, ClaimStateMutating, ClaimStateActive, ClaimStateUncertain)
}

func (a *MemoryAuthority) Snapshot(ref ServerRef) Snapshot {
	ref = normalizeServerRef(ref)
	a.mu.Lock()
	defer a.mu.Unlock()
	result := Snapshot{ServerRef: ref}
	state := a.servers[serverKey(ref)]
	if state == nil {
		return result
	}
	for _, reservation := range state.reservations {
		if reservation.State == ReservationStateReserved {
			result.Reservations = append(result.Reservations, reservation)
		}
	}
	for _, generation := range state.generations {
		if generation.State == ClaimStateReleased {
			continue
		}
		result.ClaimGenerations = append(result.ClaimGenerations, generation)
		result.Claims = append(result.Claims, claimsForGeneration(state, generation)...)
	}
	sort.Slice(result.Reservations, func(i, j int) bool { return result.Reservations[i].ID < result.Reservations[j].ID })
	sort.Slice(result.ClaimGenerations, func(i, j int) bool {
		return claimGenerationKey(result.ClaimGenerations[i].GenerationRef) < claimGenerationKey(result.ClaimGenerations[j].GenerationRef)
	})
	sortClaims(result.Claims)
	return cloneSnapshot(result)
}

func (a *MemoryAuthority) transition(ctx context.Context, ref GenerationRef, allowed []ClaimState, next ClaimState) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.servers[serverKey(ref.ServerRef)]
	if state == nil {
		return ErrClaimGenerationNotFound
	}
	key := claimGenerationKey(ref)
	generation, ok := state.generations[key]
	if !ok {
		return ErrClaimGenerationNotFound
	}
	if !containsState(allowed, generation.State) {
		return ErrInvalidTransition
	}
	generation.State = next
	state.generations[key] = generation
	return nil
}

func (a *MemoryAuthority) release(ctx context.Context, ref GenerationRef, allowed ...ClaimState) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.servers[serverKey(ref.ServerRef)]
	if state == nil {
		return ErrClaimGenerationNotFound
	}
	key := claimGenerationKey(ref)
	generation, ok := state.generations[key]
	if !ok {
		return ErrClaimGenerationNotFound
	}
	if generation.State == ClaimStateReleased {
		return nil
	}
	if !containsState(allowed, generation.State) {
		return ErrInvalidTransition
	}
	generation.State = ClaimStateReleased
	state.generations[key] = generation
	gcReservations(state)
	return nil
}

func admitState(state *inventoryState, request AdmissionRequest) (Admission, error) {
	ref := GenerationRef{ServerRef: request.ServerRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash}
	key := claimGenerationKey(ref)
	digest := admissionDigest(request)
	if existing, ok := state.generations[key]; ok {
		if existing.State == ClaimStateReleased {
			return Admission{}, ErrInvalidTransition
		}
		if existing.ClaimSetDigest != digest {
			return Admission{}, ErrInvalidRequest
		}
		return Admission{Claims: claimsForGeneration(state, existing)}, nil
	}

	generation := ClaimGeneration{GenerationRef: ref, ClaimSetDigest: digest, State: ClaimStatePending}
	state.generations[key] = generation
	result := Admission{Claims: make([]Claim, 0, len(request.Requirements))}
	for _, requirement := range request.Requirements {
		claim, err := admitRequirement(state, request, requirement)
		if err != nil {
			return Admission{}, err
		}
		claim.State = ClaimStatePending
		result.Claims = append(result.Claims, claim)
	}
	sortClaims(result.Claims)
	return result, nil
}

func admitRequirement(state *inventoryState, request AdmissionRequest, requirement Requirement) (Claim, error) {
	claimID := stableID("claim", request.TenantID, request.ServerID, fmt.Sprint(request.ServerGeneration), request.StackID, request.ResolvedPlanHash, requirement.ID)
	var compatible *Reservation
	for _, reservation := range state.reservations {
		if reservation.State != ReservationStateReserved {
			continue
		}
		if reservation.Transport != requirement.Transport || reservation.Port != requirement.Port || !addressesOverlap(reservation.BindAddress, requirement.BindAddress) {
			continue
		}
		if reservation.BindAddress == requirement.BindAddress && reservation.Sharing == requirement.Sharing {
			switch requirement.Sharing {
			case SharingVirtualHost:
				if reservation.ListenerGroupRef == requirement.ListenerGroupRef {
					copy := reservation
					compatible = &copy
					continue
				}
			case SharingExclusive:
				if exclusiveReservationReusable(state, reservation.ID, request.StackID) {
					copy := reservation
					compatible = &copy
					continue
				}
			}
		}
		return Claim{}, newConflictError(requirement)
	}
	if compatible == nil {
		reservation := Reservation{
			ID:               stableID("reservation", request.TenantID, request.ServerID, fmt.Sprint(request.ServerGeneration), string(requirement.Transport), requirement.BindAddress, fmt.Sprint(requirement.Port), string(requirement.Sharing), requirement.ListenerGroupRef),
			ServerRef:        request.ServerRef,
			Transport:        requirement.Transport,
			BindAddress:      requirement.BindAddress,
			Port:             requirement.Port,
			Sharing:          requirement.Sharing,
			ListenerGroupRef: requirement.ListenerGroupRef,
			State:            ReservationStateReserved,
		}
		state.reservations[reservation.ID] = reservation
		compatible = &reservation
	}
	claim := Claim{
		ID:               claimID,
		ReservationID:    compatible.ID,
		ServerRef:        request.ServerRef,
		StackID:          request.StackID,
		ResolvedPlanHash: request.ResolvedPlanHash,
		Requirement:      requirement,
	}
	state.claims[claim.ID] = claim
	return claim, nil
}

func normalizeAdmission(request AdmissionRequest) (AdmissionRequest, error) {
	request.ServerRef = normalizeServerRef(request.ServerRef)
	request.StackID = strings.TrimSpace(request.StackID)
	request.ResolvedPlanHash = strings.TrimSpace(request.ResolvedPlanHash)
	request.Requirements = append([]Requirement(nil), request.Requirements...)
	if request.TenantID == "" || request.ServerID == "" || request.ServerGeneration < 1 || request.StackID == "" || request.ResolvedPlanHash == "" {
		return AdmissionRequest{}, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(request.Requirements))
	for index, requirement := range request.Requirements {
		normalized, err := NormalizeRequirement(requirement)
		if err != nil {
			return AdmissionRequest{}, err
		}
		if _, exists := seen[normalized.ID]; exists {
			return AdmissionRequest{}, ErrInvalidRequest
		}
		seen[normalized.ID] = struct{}{}
		request.Requirements[index] = normalized
	}
	sort.Slice(request.Requirements, func(i, j int) bool { return request.Requirements[i].ID < request.Requirements[j].ID })
	return request, nil
}

func normalizeGenerationRef(ref *GenerationRef) error {
	ref.ServerRef = normalizeServerRef(ref.ServerRef)
	ref.StackID = strings.TrimSpace(ref.StackID)
	ref.ResolvedPlanHash = strings.TrimSpace(ref.ResolvedPlanHash)
	if ref.TenantID == "" || ref.ServerID == "" || ref.ServerGeneration < 1 || ref.StackID == "" || ref.ResolvedPlanHash == "" {
		return ErrInvalidRequest
	}
	return nil
}

func normalizeServerRef(ref ServerRef) ServerRef {
	ref.TenantID = strings.TrimSpace(ref.TenantID)
	ref.ServerID = strings.TrimSpace(ref.ServerID)
	return ref
}

func cloneInventoryState(source *inventoryState, ref ServerRef) *inventoryState {
	result := &inventoryState{
		ref: ref, reservations: make(map[string]Reservation), generations: make(map[string]ClaimGeneration), claims: make(map[string]Claim),
	}
	if source == nil {
		return result
	}
	for id, reservation := range source.reservations {
		result.reservations[id] = reservation
	}
	for id, generation := range source.generations {
		result.generations[id] = generation
	}
	for id, claim := range source.claims {
		claim.Requirement.SourceRouteRefs = append([]string(nil), claim.Requirement.SourceRouteRefs...)
		result.claims[id] = claim
	}
	return result
}

func exclusiveReservationReusable(state *inventoryState, reservationID, stackID string) bool {
	found := false
	for _, claim := range state.claims {
		if claim.ReservationID != reservationID {
			continue
		}
		generation, ok := state.generations[claimGenerationKey(GenerationRef{ServerRef: claim.ServerRef, StackID: claim.StackID, ResolvedPlanHash: claim.ResolvedPlanHash})]
		if !ok || generation.State == ClaimStateReleased {
			continue
		}
		found = true
		if claim.StackID != stackID || generation.State != ClaimStateActive {
			return false
		}
	}
	return found
}

func claimsForGeneration(state *inventoryState, generation ClaimGeneration) []Claim {
	claims := make([]Claim, 0)
	for _, claim := range state.claims {
		if sameGeneration(claim, generation.GenerationRef) {
			claim.State = generation.State
			claim.Requirement.SourceRouteRefs = append([]string(nil), claim.Requirement.SourceRouteRefs...)
			claims = append(claims, claim)
		}
	}
	sortClaims(claims)
	return claims
}

func gcReservations(state *inventoryState) {
	live := make(map[string]struct{})
	for _, claim := range state.claims {
		generation, ok := state.generations[claimGenerationKey(GenerationRef{ServerRef: claim.ServerRef, StackID: claim.StackID, ResolvedPlanHash: claim.ResolvedPlanHash})]
		if ok && generation.State != ClaimStateReleased {
			live[claim.ReservationID] = struct{}{}
		}
	}
	for id, reservation := range state.reservations {
		if _, ok := live[id]; !ok {
			reservation.State = ReservationStateReleased
			state.reservations[id] = reservation
		}
	}
}

func newConflictError(requirement Requirement) *ConflictError {
	return &ConflictError{
		ErrorCode: ErrorCodeAllocationConflict, ReasonCode: ReasonCodeHostPortReserved,
		Transport: requirement.Transport, BindAddress: requirement.BindAddress, Port: requirement.Port,
		UserGuidance: UserGuidance{
			Title: "Host port already reserved",
			Body:  "This rollout requires a host listener that overlaps an existing reservation on the selected server.",
			NextSteps: []string{
				"Choose another server",
				"Change the StackKit listener declaration",
				"Remove the existing stack after verifying its teardown",
			},
		},
	}
}

func stableID(kind string, fields ...string) string {
	hash := sha256.New()
	hash.Write([]byte(kind))
	for _, field := range fields {
		hash.Write([]byte{0})
		hash.Write([]byte(field))
	}
	return kind + "_" + hex.EncodeToString(hash.Sum(nil))[:32]
}

func admissionDigest(request AdmissionRequest) string {
	payload, _ := json.Marshal(request)
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func serverKey(ref ServerRef) string {
	return strings.Join([]string{ref.TenantID, ref.ServerID, fmt.Sprint(ref.ServerGeneration)}, "\x00")
}

func claimGenerationKey(ref GenerationRef) string {
	return strings.Join([]string{serverKey(ref.ServerRef), ref.StackID, ref.ResolvedPlanHash}, "\x00")
}

func sameGeneration(claim Claim, ref GenerationRef) bool {
	return claim.ServerRef == ref.ServerRef && claim.StackID == ref.StackID && claim.ResolvedPlanHash == ref.ResolvedPlanHash
}

func addressesOverlap(left, right string) bool { return left == "*" || right == "*" || left == right }

func containsState(states []ClaimState, candidate ClaimState) bool {
	for _, state := range states {
		if state == candidate {
			return true
		}
	}
	return false
}

func sortClaims(claims []Claim) {
	sort.Slice(claims, func(i, j int) bool { return claims[i].ID < claims[j].ID })
}

func cloneAdmission(admission Admission) Admission {
	result := Admission{Claims: append([]Claim(nil), admission.Claims...)}
	for index := range result.Claims {
		result.Claims[index].Requirement.SourceRouteRefs = append([]string(nil), result.Claims[index].Requirement.SourceRouteRefs...)
	}
	return result
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	result := snapshot
	result.Reservations = append([]Reservation(nil), snapshot.Reservations...)
	result.ClaimGenerations = append([]ClaimGeneration(nil), snapshot.ClaimGenerations...)
	result.Claims = cloneAdmission(Admission{Claims: snapshot.Claims}).Claims
	return result
}
