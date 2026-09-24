package portinventory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// PostgresAuthority is the durable implementation of the provider-neutral
// host listener lifecycle.
type PostgresAuthority struct {
	db *sql.DB
}

func NewPostgresAuthority(db *sql.DB) *PostgresAuthority {
	return &PostgresAuthority{db: db}
}

func (a *PostgresAuthority) Admit(ctx context.Context, request AdmissionRequest) (Admission, error) {
	request, err := normalizeAdmission(request)
	if err != nil {
		return Admission{}, err
	}
	var admission Admission
	err = a.withServerGeneration(ctx, request.ServerRef, request.StackID, true, false, func(tx *sql.Tx) error {
		var persistErr error
		admission, persistErr = admitAndPersist(ctx, tx, request)
		return persistErr
	})
	return cloneAdmission(admission), err
}

// EvaluateCurrent evaluates the exact claim set against one read-only snapshot
// of the canonical current server generation. It never writes claim state.
func (a *PostgresAuthority) EvaluateCurrent(ctx context.Context, request CurrentAdmissionRequest) (CurrentAdmission, error) {
	request, err := normalizeCurrentAdmission(request)
	if err != nil {
		return CurrentAdmission{}, err
	}
	if a == nil || a.db == nil {
		return CurrentAdmission{}, fmt.Errorf("%w: database not configured", ErrInvalidRequest)
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return CurrentAdmission{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return CurrentAdmission{}, err
	}
	serverRef, err := currentServerRef(ctx, tx, request, false)
	if err != nil {
		return CurrentAdmission{}, err
	}
	admissionRequest, err := normalizeAdmission(AdmissionRequest{
		ServerRef: serverRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash, Requirements: request.Requirements,
	})
	if err != nil {
		return CurrentAdmission{}, err
	}
	state, _, err := loadInventoryState(ctx, tx, serverRef)
	if err != nil {
		return CurrentAdmission{}, err
	}
	evaluated := cloneInventoryState(state, serverRef)
	admission, err := admitState(evaluated, admissionRequest)
	if err != nil {
		return CurrentAdmission{}, err
	}
	generation := evaluated.generations[claimGenerationKey(GenerationRef{
		ServerRef: serverRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash,
	})]
	return currentAdmissionResult(admissionRequest, generation.State, admission), nil
}

// AdmitCurrent locks the Techstack lifecycle before the canonical server head
// and persists the exact claim generation atomically. A teardown snapshot uses
// the same first lock, so an admission is wholly before or after that snapshot.
func (a *PostgresAuthority) AdmitCurrent(ctx context.Context, request CurrentAdmissionRequest) (CurrentAdmission, error) {
	request, err := normalizeCurrentAdmission(request)
	if err != nil {
		return CurrentAdmission{}, err
	}
	if a == nil || a.db == nil {
		return CurrentAdmission{}, fmt.Errorf("%w: database not configured", ErrInvalidRequest)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return CurrentAdmission{}, err
	}
	defer tx.Rollback()
	if err = lockTechstack(ctx, tx, request.TenantID, request.StackID); err != nil {
		return CurrentAdmission{}, err
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return CurrentAdmission{}, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, postgresCurrentServerLockKey(request)); err != nil {
		return CurrentAdmission{}, err
	}
	serverRef, err := currentServerRef(ctx, tx, request, true)
	if err != nil {
		return CurrentAdmission{}, err
	}
	admissionRequest, err := normalizeAdmission(AdmissionRequest{
		ServerRef: serverRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash, Requirements: request.Requirements,
	})
	if err != nil {
		return CurrentAdmission{}, err
	}
	admission, err := admitAndPersist(ctx, tx, admissionRequest)
	if err != nil {
		return CurrentAdmission{}, err
	}
	generationState, err := loadClaimGenerationState(ctx, tx, GenerationRef{
		ServerRef: serverRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash,
	})
	if err != nil {
		return CurrentAdmission{}, err
	}
	if err = tx.Commit(); err != nil {
		return CurrentAdmission{}, err
	}
	return currentAdmissionResult(admissionRequest, generationState, admission), nil
}

// SnapshotForTeardown captures every non-released generation for one exact
// Owner-owned Techstack deployment. It excludes concurrent admission through
// the shared lifecycle lock and reads the complete batch in one query, so the
// durable job receipt can never contain a partial cross-server batch.
func (a *PostgresAuthority) SnapshotForTeardown(ctx context.Context, request TeardownSnapshotRequest) (TeardownSnapshot, error) {
	request, err := normalizeTeardownRequest(request)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	if a == nil || a.db == nil {
		return TeardownSnapshot{}, fmt.Errorf("%w: database not configured", ErrInvalidRequest)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	defer tx.Rollback()
	if err = lockTechstack(ctx, tx, request.TenantID, request.TechstackID); err != nil {
		return TeardownSnapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return TeardownSnapshot{}, err
	}
	owned, err := ownsTechstack(ctx, tx, request)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	if !owned {
		return TeardownSnapshot{}, ErrTeardownSnapshotMismatch
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT generation.server_id, generation.server_generation,
		       generation.resolved_plan_hash, generation.claim_set_digest,
		       server.owner_subject_id,
		       COALESCE((
		           SELECT jsonb_agg(nodes.node_ref ORDER BY nodes.node_ref)::text
		           FROM (
		               SELECT DISTINCT claim.node_ref
		               FROM server_port_reservation_claims AS claim
		               WHERE claim.tenant_id = generation.tenant_id
		                 AND claim.server_id = generation.server_id
		                 AND claim.server_generation = generation.server_generation
		                 AND claim.stack_id = generation.stack_id
		                 AND claim.resolved_plan_hash = generation.resolved_plan_hash
		           ) AS nodes
		       ), '[]')
		FROM server_port_claim_generations AS generation
		JOIN servers AS server
		  ON server.tenant_id = generation.tenant_id
		 AND server.id = generation.server_id
		WHERE generation.tenant_id = $1
		  AND generation.stack_id = $2
		  AND generation.state <> 'released'
		ORDER BY generation.server_id, generation.server_generation, generation.resolved_plan_hash
	`, request.TenantID, request.TechstackID)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	defer rows.Close()
	generations := make([]TeardownGeneration, 0)
	for rows.Next() {
		generation := TeardownGeneration{GenerationRef: GenerationRef{
			ServerRef: ServerRef{TenantID: request.TenantID}, StackID: request.TechstackID,
		}}
		var serverOwner string
		var nodeRefsJSON string
		if err := rows.Scan(
			&generation.ServerID, &generation.ServerGeneration, &generation.ResolvedPlanHash,
			&generation.ClaimSetDigest, &serverOwner, &nodeRefsJSON,
		); err != nil {
			return TeardownSnapshot{}, err
		}
		if err := json.Unmarshal([]byte(nodeRefsJSON), &generation.NodeRefs); err != nil {
			return TeardownSnapshot{}, err
		}
		if strings.TrimSpace(serverOwner) != request.OwnerSubjectID {
			return TeardownSnapshot{}, ErrTeardownSnapshotMismatch
		}
		generations = append(generations, generation)
	}
	if err := rows.Err(); err != nil {
		return TeardownSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return TeardownSnapshot{}, err
	}
	snapshot, err := sealTeardownSnapshot(request, generations)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeardownSnapshot{}, err
	}
	return snapshot, nil
}

// ReleaseTeardownSnapshot releases an immutable batch atomically. Every row is
// locked and its claim-set digest is compared before the first update, so a
// missing or replaced member rolls the complete operation back.
func (a *PostgresAuthority) ReleaseTeardownSnapshot(ctx context.Context, snapshot TeardownSnapshot) error {
	if err := ValidateTeardownSnapshot(snapshot); err != nil {
		return err
	}
	if a == nil || a.db == nil {
		return fmt.Errorf("%w: database not configured", ErrInvalidRequest)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockTechstack(ctx, tx, snapshot.TenantID, snapshot.TechstackID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", snapshot.TenantID); err != nil {
		return err
	}
	owned, err := ownsTechstack(ctx, tx, TeardownSnapshotRequest{
		TenantID: snapshot.TenantID, OwnerSubjectID: snapshot.OwnerSubjectID, TechstackID: snapshot.TechstackID,
	})
	if err != nil {
		return err
	}
	if !owned {
		return ErrTeardownSnapshotMismatch
	}
	serverRefs := teardownServerRefs(snapshot.Generations)
	for _, ref := range serverRefs {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, postgresServerLockKey(ref)); err != nil {
			return err
		}
	}
	states, err := matchExactTeardownBatch(ctx, tx, snapshot)
	if err != nil {
		return err
	}
	for index, generation := range snapshot.Generations {
		if states[index] == ClaimStateReleased {
			continue
		}
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE server_port_claim_generations
			SET state = 'released', released_at = COALESCE(released_at, clock_timestamp()),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
			  AND stack_id = $4 AND resolved_plan_hash = $5 AND state = $6
		`, generation.TenantID, generation.ServerID, generation.ServerGeneration,
			generation.StackID, generation.ResolvedPlanHash, states[index])
		if updateErr = requireOneRow(result, updateErr); updateErr != nil {
			return updateErr
		}
	}
	for _, ref := range serverRefs {
		if err := releaseUnusedReservations(ctx, tx, ref); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func lockTechstack(ctx context.Context, tx *sql.Tx, tenantID, techstackID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		stableID("portinventory-techstack-lock", tenantID, techstackID))
	return err
}

func ownsTechstack(ctx context.Context, tx *sql.Tx, request TeardownSnapshotRequest) (bool, error) {
	var marker int
	err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM stacks
		WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		FOR UPDATE
	`, request.TenantID, request.TechstackID, request.OwnerSubjectID).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && marker == 1, err
}

func matchExactTeardownBatch(ctx context.Context, tx *sql.Tx, snapshot TeardownSnapshot) ([]ClaimState, error) {
	expected := make(map[string]int, len(snapshot.Generations))
	for index, generation := range snapshot.Generations {
		expected[claimGenerationKey(generation.GenerationRef)] = index
	}
	states := make([]ClaimState, len(snapshot.Generations))
	seen := make(map[string]struct{}, len(snapshot.Generations))
	rows, err := tx.QueryContext(ctx, `
		SELECT server_id, server_generation, resolved_plan_hash, claim_set_digest, state
		FROM server_port_claim_generations
		WHERE tenant_id = $1 AND stack_id = $2
		ORDER BY server_id, server_generation, resolved_plan_hash
		FOR UPDATE
	`, snapshot.TenantID, snapshot.TechstackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		generation := TeardownGeneration{GenerationRef: GenerationRef{
			ServerRef: ServerRef{TenantID: snapshot.TenantID}, StackID: snapshot.TechstackID,
		}}
		var state ClaimState
		if err := rows.Scan(&generation.ServerID, &generation.ServerGeneration, &generation.ResolvedPlanHash, &generation.ClaimSetDigest, &state); err != nil {
			return nil, err
		}
		key := claimGenerationKey(generation.GenerationRef)
		index, included := expected[key]
		if !included {
			if state != ClaimStateReleased {
				return nil, ErrTeardownSnapshotMismatch
			}
			continue
		}
		if generation.ClaimSetDigest != snapshot.Generations[index].ClaimSetDigest || !validTeardownState(state) {
			return nil, ErrTeardownSnapshotMismatch
		}
		states[index] = state
		seen[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(seen) != len(expected) {
		return nil, ErrTeardownSnapshotMismatch
	}
	return states, nil
}

func validTeardownState(state ClaimState) bool {
	return containsState([]ClaimState{
		ClaimStatePending, ClaimStateMutating, ClaimStateActive, ClaimStateUncertain, ClaimStateReleased,
	}, state)
}

func teardownServerRefs(generations []TeardownGeneration) []ServerRef {
	byKey := make(map[string]ServerRef, len(generations))
	for _, generation := range generations {
		key := postgresServerLockKey(generation.ServerRef)
		byKey[key] = generation.ServerRef
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	refs := make([]ServerRef, 0, len(keys))
	for _, key := range keys {
		refs = append(refs, byKey[key])
	}
	return refs
}

func normalizeCurrentAdmission(request CurrentAdmissionRequest) (CurrentAdmissionRequest, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.StackID = strings.TrimSpace(request.StackID)
	request.ResolvedPlanHash = strings.TrimSpace(request.ResolvedPlanHash)
	request.Requirements = append([]Requirement(nil), request.Requirements...)
	if request.TenantID == "" || request.ServerID == "" || request.OwnerSubjectID == "" || request.StackID == "" || request.ResolvedPlanHash == "" {
		return CurrentAdmissionRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func currentServerRef(ctx context.Context, tx *sql.Tx, request CurrentAdmissionRequest, lock bool) (ServerRef, error) {
	query := `
		SELECT server.generation, server.lifecycle_state, stack.status
		FROM servers AS server
		JOIN stacks AS stack
		  ON stack.tenant_id = server.tenant_id
		 AND stack.id = server.stack_id
		 AND stack.owner_subject_id = server.owner_subject_id
		 AND stack.deleted_at IS NULL
		WHERE server.tenant_id = $1 AND server.id = $2
		  AND server.stack_id = $3 AND server.owner_subject_id = $4
	`
	if lock {
		query += ` FOR UPDATE OF server, stack`
	}
	var generation int64
	var lifecycleState, stackStatus string
	err := tx.QueryRowContext(ctx, query, request.TenantID, request.ServerID, request.StackID, request.OwnerSubjectID).Scan(&generation, &lifecycleState, &stackStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerRef{}, &StaleServerGenerationError{ServerID: request.ServerID}
	}
	if err != nil {
		return ServerRef{}, err
	}
	if state := strings.TrimSpace(lifecycleState); state == "decommissioning" || state == "decommissioned" {
		return ServerRef{}, ErrInvalidTransition
	}
	if state := strings.ToLower(strings.TrimSpace(stackStatus)); state == "stopping" || state == "stopped" {
		return ServerRef{}, ErrInvalidTransition
	}
	return ServerRef{TenantID: request.TenantID, ServerID: request.ServerID, ServerGeneration: generation}, nil
}

func currentAdmissionResult(request AdmissionRequest, state ClaimState, admission Admission) CurrentAdmission {
	return CurrentAdmission{
		GenerationRef: GenerationRef{ServerRef: request.ServerRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash},
		State:         state,
		Admission:     cloneAdmission(admission),
	}
}

func admitAndPersist(ctx context.Context, tx *sql.Tx, request AdmissionRequest) (Admission, error) {
	state, persisted, err := loadInventoryState(ctx, tx, request.ServerRef)
	if err != nil {
		return Admission{}, err
	}
	admission, err := admitState(state, request)
	if err != nil {
		return Admission{}, err
	}
	generationKey := claimGenerationKey(GenerationRef{
		ServerRef: request.ServerRef, StackID: request.StackID, ResolvedPlanHash: request.ResolvedPlanHash,
	})
	if _, exists := persisted.generations[generationKey]; !exists {
		if err = insertClaimGeneration(ctx, tx, state.generations[generationKey]); err != nil {
			return Admission{}, err
		}
		persisted.generations[generationKey] = struct{}{}
	}
	for _, claim := range admission.Claims {
		reservation := state.reservations[claim.ReservationID]
		if existingState, exists := persisted.reservations[reservation.ID]; !exists || existingState == ReservationStateReleased {
			if err = reservePort(ctx, tx, reservation); err != nil {
				return Admission{}, err
			}
			persisted.reservations[reservation.ID] = ReservationStateReserved
		}
		if _, exists := persisted.claims[claim.ID]; !exists {
			if err = insertReservationClaim(ctx, tx, claim); err != nil {
				return Admission{}, err
			}
			persisted.claims[claim.ID] = struct{}{}
		}
	}
	return admission, nil
}

type persistedInventory struct {
	reservations map[string]ReservationState
	generations  map[string]struct{}
	claims       map[string]struct{}
}

func loadInventoryState(ctx context.Context, tx *sql.Tx, ref ServerRef) (*inventoryState, persistedInventory, error) {
	state := cloneInventoryState(nil, ref)
	persisted := persistedInventory{
		reservations: make(map[string]ReservationState),
		generations:  make(map[string]struct{}),
		claims:       make(map[string]struct{}),
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, transport, bind_address, port, sharing, listener_group_ref, state
		FROM server_port_reservations
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		ORDER BY id
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration)
	if err != nil {
		return nil, persisted, err
	}
	for rows.Next() {
		var reservation Reservation
		var port int64
		if err = rows.Scan(&reservation.ID, &reservation.Transport, &reservation.BindAddress, &port, &reservation.Sharing, &reservation.ListenerGroupRef, &reservation.State); err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		reservation.ServerRef = ref
		reservation.Port, err = persistedPort(port)
		if err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		state.reservations[reservation.ID] = reservation
		persisted.reservations[reservation.ID] = reservation.State
	}
	if err = closeRows(rows); err != nil {
		return nil, persisted, err
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT stack_id, resolved_plan_hash, claim_set_digest, state
		FROM server_port_claim_generations
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		ORDER BY stack_id, resolved_plan_hash
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration)
	if err != nil {
		return nil, persisted, err
	}
	for rows.Next() {
		var generation ClaimGeneration
		generation.ServerRef = ref
		if err = rows.Scan(&generation.StackID, &generation.ResolvedPlanHash, &generation.ClaimSetDigest, &generation.State); err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		key := claimGenerationKey(generation.GenerationRef)
		state.generations[key] = generation
		persisted.generations[key] = struct{}{}
	}
	if err = closeRows(rows); err != nil {
		return nil, persisted, err
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT c.id, c.reservation_id, c.stack_id, c.resolved_plan_hash,
		       c.requirement_id, c.node_ref, r.transport, r.bind_address,
		       r.port, r.sharing, r.listener_group_ref, c.exposure,
		       c.source_route_refs_json::text
		FROM server_port_reservation_claims AS c
		JOIN server_port_reservations AS r
		  ON r.tenant_id = c.tenant_id
		 AND r.id = c.reservation_id
		 AND r.server_id = c.server_id
		 AND r.server_generation = c.server_generation
		WHERE c.tenant_id = $1 AND c.server_id = $2 AND c.server_generation = $3
		ORDER BY c.id
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration)
	if err != nil {
		return nil, persisted, err
	}
	for rows.Next() {
		var claim Claim
		var port int64
		var sourceRefsJSON string
		claim.ServerRef = ref
		if err = rows.Scan(
			&claim.ID, &claim.ReservationID, &claim.StackID, &claim.ResolvedPlanHash,
			&claim.Requirement.ID, &claim.Requirement.NodeRef, &claim.Requirement.Transport,
			&claim.Requirement.BindAddress, &port, &claim.Requirement.Sharing,
			&claim.Requirement.ListenerGroupRef, &claim.Requirement.Exposure, &sourceRefsJSON,
		); err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		claim.Requirement.Port, err = persistedPort(port)
		if err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		if err = json.Unmarshal([]byte(sourceRefsJSON), &claim.Requirement.SourceRouteRefs); err != nil {
			_ = rows.Close()
			return nil, persisted, err
		}
		state.claims[claim.ID] = claim
		persisted.claims[claim.ID] = struct{}{}
	}
	if err = closeRows(rows); err != nil {
		return nil, persisted, err
	}
	return state, persisted, nil
}

func persistedPort(value int64) (uint16, error) {
	if value < 1 || value > math.MaxUint16 {
		return 0, fmt.Errorf("persisted port %d: %w", value, ErrInvalidRequirement)
	}
	// #nosec G115 -- value is restricted to the valid TCP/UDP port range above.
	return uint16(value), nil
}

func closeRows(rows *sql.Rows) error {
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	return closeErr
}

func insertClaimGeneration(ctx context.Context, tx *sql.Tx, generation ClaimGeneration) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO server_port_claim_generations (
			tenant_id, server_id, server_generation, stack_id,
			resolved_plan_hash, claim_set_digest, state
		) VALUES ($1, $2, $3, $4, $5, $6, 'pending')
	`, generation.TenantID, generation.ServerID, generation.ServerGeneration,
		generation.StackID, generation.ResolvedPlanHash, generation.ClaimSetDigest)
	return requireOneRow(result, err)
}

func reservePort(ctx context.Context, tx *sql.Tx, reservation Reservation) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO server_port_reservations (
			id, tenant_id, server_id, server_generation, transport,
			bind_address, port, sharing, listener_group_ref, state
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'reserved')
		ON CONFLICT (id) DO UPDATE SET
			state = 'reserved', released_at = NULL, updated_at = clock_timestamp()
		WHERE server_port_reservations.tenant_id = EXCLUDED.tenant_id
		  AND server_port_reservations.server_id = EXCLUDED.server_id
		  AND server_port_reservations.server_generation = EXCLUDED.server_generation
		  AND server_port_reservations.transport = EXCLUDED.transport
		  AND server_port_reservations.bind_address = EXCLUDED.bind_address
		  AND server_port_reservations.port = EXCLUDED.port
		  AND server_port_reservations.sharing = EXCLUDED.sharing
		  AND server_port_reservations.listener_group_ref = EXCLUDED.listener_group_ref
		  AND server_port_reservations.state = 'released'
	`, reservation.ID, reservation.ServerRef.TenantID, reservation.ServerRef.ServerID, reservation.ServerRef.ServerGeneration,
		reservation.Transport, reservation.BindAddress, int64(reservation.Port), reservation.Sharing, reservation.ListenerGroupRef)
	return requireOneRow(result, err)
}

func insertReservationClaim(ctx context.Context, tx *sql.Tx, claim Claim) error {
	refsJSON, err := json.Marshal(claim.Requirement.SourceRouteRefs)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO server_port_reservation_claims (
			id, tenant_id, reservation_id, server_id, server_generation,
			stack_id, resolved_plan_hash, requirement_id, node_ref, exposure,
			source_route_refs_json, claim_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12)
	`, claim.ID, claim.ServerRef.TenantID, claim.ReservationID, claim.ServerRef.ServerID, claim.ServerRef.ServerGeneration,
		claim.StackID, claim.ResolvedPlanHash, claim.Requirement.ID, claim.Requirement.NodeRef,
		claim.Requirement.Exposure, string(refsJSON), persistedClaimDigest(claim))
	return requireOneRow(result, err)
}

func persistedClaimDigest(claim Claim) string {
	payload, _ := json.Marshal(struct {
		ReservationID string      `json:"reservation_id"`
		Requirement   Requirement `json:"requirement"`
	}{ReservationID: claim.ReservationID, Requirement: claim.Requirement})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func requireOneRow(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func (a *PostgresAuthority) MarkMutationStarted(ctx context.Context, ref GenerationRef) error {
	return a.transitionGeneration(ctx, ref, []ClaimState{ClaimStatePending, ClaimStateMutating, ClaimStateUncertain}, ClaimStateMutating, `
		UPDATE server_port_claim_generations
		SET state = 'mutating', mutation_started_at = COALESCE(mutation_started_at, clock_timestamp()),
		    uncertain_at = NULL, updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		  AND stack_id = $4 AND resolved_plan_hash = $5 AND state IN ('pending', 'uncertain')
	`)
}

func (a *PostgresAuthority) Activate(ctx context.Context, ref GenerationRef) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	return a.withServerGeneration(ctx, ref.ServerRef, "", true, false, func(tx *sql.Tx) error {
		state, err := loadClaimGenerationState(ctx, tx, ref)
		if err != nil {
			return err
		}
		if state != ClaimStateMutating && state != ClaimStateActive {
			return ErrInvalidTransition
		}
		if state == ClaimStateMutating {
			result, updateErr := tx.ExecContext(ctx, `
				UPDATE server_port_claim_generations
				SET state = 'active', activated_at = COALESCE(activated_at, clock_timestamp()),
				    updated_at = clock_timestamp()
				WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
				  AND stack_id = $4 AND resolved_plan_hash = $5 AND state = 'mutating'
			`, ref.TenantID, ref.ServerID, ref.ServerGeneration, ref.StackID, ref.ResolvedPlanHash)
			if updateErr = requireOneRow(result, updateErr); updateErr != nil {
				return updateErr
			}
		}
		if _, err = tx.ExecContext(ctx, `
			UPDATE server_port_claim_generations
			SET state = 'released', released_at = COALESCE(released_at, clock_timestamp()),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
			  AND stack_id = $4 AND resolved_plan_hash <> $5 AND state IN ('active', 'uncertain')
		`, ref.TenantID, ref.ServerID, ref.ServerGeneration, ref.StackID, ref.ResolvedPlanHash); err != nil {
			return err
		}
		return releaseUnusedReservations(ctx, tx, ref.ServerRef)
	})
}

func (a *PostgresAuthority) MarkUncertain(ctx context.Context, ref GenerationRef) error {
	return a.transitionGeneration(ctx, ref, []ClaimState{ClaimStateMutating, ClaimStateUncertain}, ClaimStateUncertain, `
		UPDATE server_port_claim_generations
		SET state = 'uncertain', uncertain_at = COALESCE(uncertain_at, clock_timestamp()),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		  AND stack_id = $4 AND resolved_plan_hash = $5 AND state = 'mutating'
	`)
}

func (a *PostgresAuthority) AbortBeforeMutation(ctx context.Context, ref GenerationRef) error {
	return a.releaseGeneration(ctx, ref, []ClaimState{ClaimStatePending})
}

func (a *PostgresAuthority) ReleaseAfterTeardown(ctx context.Context, ref GenerationRef) error {
	return a.releaseGeneration(ctx, ref, []ClaimState{ClaimStateMutating, ClaimStateActive, ClaimStateUncertain})
}

func (a *PostgresAuthority) transitionGeneration(
	ctx context.Context,
	ref GenerationRef,
	allowed []ClaimState,
	next ClaimState,
	statement string,
) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	return a.withServerGeneration(ctx, ref.ServerRef, "", true, false, func(tx *sql.Tx) error {
		state, err := loadClaimGenerationState(ctx, tx, ref)
		if err != nil {
			return err
		}
		if !containsState(allowed, state) {
			return ErrInvalidTransition
		}
		if state == next {
			return nil
		}
		result, err := tx.ExecContext(ctx, statement,
			ref.TenantID, ref.ServerID, ref.ServerGeneration, ref.StackID, ref.ResolvedPlanHash)
		return requireOneRow(result, err)
	})
}

func (a *PostgresAuthority) releaseGeneration(ctx context.Context, ref GenerationRef, allowed []ClaimState) error {
	if err := normalizeGenerationRef(&ref); err != nil {
		return err
	}
	return a.withServerGeneration(ctx, ref.ServerRef, "", false, true, func(tx *sql.Tx) error {
		state, err := loadClaimGenerationState(ctx, tx, ref)
		if err != nil {
			return err
		}
		if state == ClaimStateReleased {
			return nil
		}
		if !containsState(allowed, state) {
			return ErrInvalidTransition
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE server_port_claim_generations
			SET state = 'released', released_at = COALESCE(released_at, clock_timestamp()),
			    updated_at = clock_timestamp()
			WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
			  AND stack_id = $4 AND resolved_plan_hash = $5 AND state = $6
		`, ref.TenantID, ref.ServerID, ref.ServerGeneration, ref.StackID, ref.ResolvedPlanHash, state)
		if err = requireOneRow(result, err); err != nil {
			return err
		}
		return releaseUnusedReservations(ctx, tx, ref.ServerRef)
	})
}

func loadClaimGenerationState(ctx context.Context, tx *sql.Tx, ref GenerationRef) (ClaimState, error) {
	var state ClaimState
	err := tx.QueryRowContext(ctx, `
		SELECT state
		FROM server_port_claim_generations
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		  AND stack_id = $4 AND resolved_plan_hash = $5
		FOR UPDATE
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration, ref.StackID, ref.ResolvedPlanHash).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrClaimGenerationNotFound
	}
	return state, err
}

func releaseUnusedReservations(ctx context.Context, tx *sql.Tx, ref ServerRef) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE server_port_reservations AS reservation
		SET state = 'released', released_at = COALESCE(released_at, clock_timestamp()),
		    updated_at = clock_timestamp()
		WHERE reservation.tenant_id = $1
		  AND reservation.server_id = $2
		  AND reservation.server_generation = $3
		  AND reservation.state = 'reserved'
		  AND NOT EXISTS (
			SELECT 1
			FROM server_port_reservation_claims AS claim
			JOIN server_port_claim_generations AS generation
			  ON generation.tenant_id = claim.tenant_id
			 AND generation.server_id = claim.server_id
			 AND generation.server_generation = claim.server_generation
			 AND generation.stack_id = claim.stack_id
			 AND generation.resolved_plan_hash = claim.resolved_plan_hash
			WHERE claim.tenant_id = reservation.tenant_id
			  AND claim.reservation_id = reservation.id
			  AND claim.server_id = reservation.server_id
			  AND claim.server_generation = reservation.server_generation
			  AND generation.state <> 'released'
		  )
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration)
	return err
}

func (a *PostgresAuthority) withServerGeneration(
	ctx context.Context,
	ref ServerRef,
	techstackID string,
	requireAllocatable bool,
	allowHistorical bool,
	fn func(*sql.Tx) error,
) error {
	if a == nil || a.db == nil {
		return fmt.Errorf("%w: database not configured", ErrInvalidRequest)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if techstackID != "" {
		if err = lockTechstack(ctx, tx, ref.TenantID, techstackID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", ref.TenantID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, postgresServerLockKey(ref)); err != nil {
		return err
	}
	var actualGeneration int64
	var lifecycleState string
	err = tx.QueryRowContext(ctx, `
		SELECT generation, lifecycle_state
		FROM servers
		WHERE tenant_id = $1 AND id = $2
		FOR UPDATE
	`, ref.TenantID, ref.ServerID).Scan(&actualGeneration, &lifecycleState)
	if errors.Is(err, sql.ErrNoRows) {
		return &StaleServerGenerationError{ServerID: ref.ServerID, RequestedGeneration: ref.ServerGeneration}
	}
	if err != nil {
		return err
	}
	if actualGeneration != ref.ServerGeneration && (!allowHistorical || ref.ServerGeneration > actualGeneration) {
		return &StaleServerGenerationError{ServerID: ref.ServerID, RequestedGeneration: ref.ServerGeneration, ActualGeneration: actualGeneration}
	}
	if state := strings.TrimSpace(lifecycleState); requireAllocatable && (state == "decommissioning" || state == "decommissioned") {
		return ErrInvalidTransition
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func postgresServerLockKey(ref ServerRef) string {
	// serverKey is optimized for in-memory maps and contains NUL separators,
	// which PostgreSQL text values reject. A stable digest preserves the exact
	// tenant/server/generation fence without relying on delimiter escaping.
	return stableID(
		"portinventory-server-generation-lock",
		ref.TenantID,
		ref.ServerID,
		fmt.Sprint(ref.ServerGeneration),
	)
}

func postgresCurrentServerLockKey(request CurrentAdmissionRequest) string {
	return stableID("portinventory-current-server-lock", request.TenantID, request.ServerID)
}

var _ Authority = (*PostgresAuthority)(nil)
var _ LifecycleAuthority = (*PostgresAuthority)(nil)
