package portinventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RecordGuardPorts persists one monotone, complete-or-explicitly-partial Guard
// listener observation. The authenticated Agent identity is resolved to the
// canonical current server generation inside the tenant transaction.
func (a *PostgresAuthority) RecordGuardPorts(ctx context.Context, input GuardObservation) error {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.RuntimeAgentID = strings.TrimSpace(input.RuntimeAgentID)
	input.SourceEpoch = strings.TrimSpace(input.SourceEpoch)
	input.ObservedAt = input.ObservedAt.UTC()
	if a == nil || a.db == nil || input.TenantID == "" || input.RuntimeAgentID == "" || input.SourceEpoch == "" ||
		input.SourceSequence < 1 || input.InventoryRevision < 1 || input.ObservedAt.IsZero() {
		return ErrInvalidRequest
	}
	facts, parsedComplete := parseAgentPortFacts(input.OpenPorts)
	listenersComplete := input.ListenersComplete && parsedComplete

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", input.TenantID); err != nil {
		return err
	}
	var ref ServerRef
	if err = tx.QueryRowContext(ctx, `
		SELECT id, generation
		FROM servers
		WHERE tenant_id = $1 AND worker_id = $2
		FOR UPDATE
	`, input.TenantID, input.RuntimeAgentID).Scan(&ref.ServerID, &ref.ServerGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: authenticated Agent is not bound to a RuntimeServer", ErrInvalidRequest)
		}
		return err
	}
	ref.TenantID = input.TenantID

	var latestSequence int64
	err = tx.QueryRowContext(ctx, `
		SELECT source_sequence
		FROM server_port_runtime_observations
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3 AND source_epoch = $4
		ORDER BY source_sequence DESC
		LIMIT 1
		FOR UPDATE
	`, input.TenantID, ref.ServerID, ref.ServerGeneration, input.SourceEpoch).Scan(&latestSequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && input.SourceSequence <= latestSequence {
		return tx.Commit()
	}

	var observationID int64
	if err = tx.QueryRowContext(ctx, `
		INSERT INTO server_port_runtime_observations (
			tenant_id, server_id, server_generation, source_epoch, source_sequence,
			inventory_revision, listeners_complete, exposures_complete,
			observed_at, expires_at, evidence_json
		) VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8, $9, '{}'::jsonb)
		RETURNING id
	`, input.TenantID, ref.ServerID, ref.ServerGeneration, input.SourceEpoch, input.SourceSequence,
		input.InventoryRevision, listenersComplete, input.ObservedAt, input.ObservedAt.Add(portObservationTTL)).Scan(&observationID); err != nil {
		return err
	}
	for _, fact := range facts {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO server_port_runtime_facts (
				tenant_id, observation_id, server_id, server_generation, fact_kind,
				transport, bind_address, port, exposure, evidence_json
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '{}'::jsonb)
		`, input.TenantID, observationID, ref.ServerID, ref.ServerGeneration, fact.Kind,
			fact.Transport, fact.BindAddress, fact.Port, fact.Exposure); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *PostgresAuthority) ReadCurrent(ctx context.Context, request InventoryRequest, now time.Time) (Inventory, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	if a == nil || a.db == nil || request.TenantID == "" || request.ServerID == "" {
		return Inventory{}, ErrInvalidRequest
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Inventory{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return Inventory{}, err
	}

	result := Inventory{TenantID: request.TenantID, ServerID: request.ServerID, Allocations: []Allocation{}}
	if err = tx.QueryRowContext(ctx, `
		SELECT generation
		FROM servers
		WHERE tenant_id = $1 AND id = $2 AND ($3 = '' OR owner_subject_id = $3)
	`, request.TenantID, request.ServerID, request.OwnerSubjectID).Scan(&result.ServerGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Inventory{}, sql.ErrNoRows
		}
		return Inventory{}, err
	}

	allocations, err := readDesiredAllocations(ctx, tx, ServerRef{TenantID: result.TenantID, ServerID: result.ServerID, ServerGeneration: result.ServerGeneration})
	if err != nil {
		return Inventory{}, err
	}
	observationID, err := readLatestObservation(ctx, tx, &result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Inventory{}, err
	}
	facts := []RuntimeFact{}
	if err == nil {
		facts, err = readObservationFacts(ctx, tx, result.TenantID, observationID)
		if err != nil {
			return Inventory{}, err
		}
	}
	result.Allocations = projectAllocations(allocations, facts, result, now)
	if err = tx.Commit(); err != nil {
		return Inventory{}, err
	}
	return result, nil
}

func readDesiredAllocations(ctx context.Context, tx *sql.Tx, ref ServerRef) ([]Allocation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT claim.id, claim.stack_id, claim.resolved_plan_hash, claim.node_ref,
			reservation.transport, reservation.bind_address, reservation.port,
			reservation.sharing, reservation.listener_group_ref, claim.exposure,
			claim.source_route_refs_json, reservation.state, generation.state
		FROM server_port_reservation_claims AS claim
		JOIN server_port_reservations AS reservation
		  ON reservation.tenant_id = claim.tenant_id AND reservation.id = claim.reservation_id
		JOIN server_port_claim_generations AS generation
		  ON generation.tenant_id = claim.tenant_id
		 AND generation.server_id = claim.server_id
		 AND generation.server_generation = claim.server_generation
		 AND generation.stack_id = claim.stack_id
		 AND generation.resolved_plan_hash = claim.resolved_plan_hash
		WHERE claim.tenant_id = $1 AND claim.server_id = $2 AND claim.server_generation = $3
		  AND generation.state <> 'released' AND reservation.state = 'reserved'
		ORDER BY reservation.port, reservation.transport, claim.id
	`, ref.TenantID, ref.ServerID, ref.ServerGeneration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Allocation{}
	for rows.Next() {
		var allocation Allocation
		var sourceRoutes []byte
		if err = rows.Scan(
			&allocation.ID, &allocation.StackID, &allocation.ResolvedPlanHash, &allocation.NodeRef,
			&allocation.Transport, &allocation.BindAddress, &allocation.Port, &allocation.Sharing,
			&allocation.ListenerGroupRef, &allocation.Exposure, &sourceRoutes,
			&allocation.ReservationState, &allocation.ClaimState,
		); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(sourceRoutes, &allocation.SourceRouteRefs); err != nil {
			return nil, err
		}
		allocation.Desired = true
		result = append(result, allocation)
	}
	return result, rows.Err()
}

func readLatestObservation(ctx context.Context, tx *sql.Tx, result *Inventory) (int64, error) {
	var observationID int64
	var observedAt, expiresAt time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT id, inventory_revision, listeners_complete, exposures_complete, observed_at, expires_at
		FROM server_port_runtime_observations
		WHERE tenant_id = $1 AND server_id = $2 AND server_generation = $3
		ORDER BY inventory_revision DESC, id DESC
		LIMIT 1
	`, result.TenantID, result.ServerID, result.ServerGeneration).Scan(
		&observationID, &result.InventoryRevision, &result.ListenersComplete,
		&result.ExposuresComplete, &observedAt, &expiresAt,
	)
	if err != nil {
		return 0, err
	}
	observedAt, expiresAt = observedAt.UTC(), expiresAt.UTC()
	result.ObservedAt, result.ExpiresAt = &observedAt, &expiresAt
	return observationID, nil
}

func readObservationFacts(ctx context.Context, tx *sql.Tx, tenantID string, observationID int64) ([]RuntimeFact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT fact_kind, transport, bind_address, port, exposure
		FROM server_port_runtime_facts
		WHERE tenant_id = $1 AND observation_id = $2
		ORDER BY port, transport, bind_address, fact_kind
	`, tenantID, observationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RuntimeFact{}
	for rows.Next() {
		var fact RuntimeFact
		if err = rows.Scan(&fact.Kind, &fact.Transport, &fact.BindAddress, &fact.Port, &fact.Exposure); err != nil {
			return nil, err
		}
		result = append(result, fact)
	}
	return result, rows.Err()
}

func projectAllocations(desired []Allocation, facts []RuntimeFact, inventory Inventory, now time.Time) []Allocation {
	stale := inventory.ExpiresAt != nil && !now.Before(*inventory.ExpiresAt)
	usedFacts := make(map[int]bool, len(facts))
	for index := range desired {
		desired[index].ObservedState = evidenceStateFor(desired[index], FactKindObserved, facts, inventory.ListenersComplete, stale, usedFacts)
		desired[index].ExposedState = evidenceStateFor(desired[index], FactKindExposed, facts, inventory.ExposuresComplete, stale, usedFacts)
		switch desired[index].ObservedState {
		case EvidencePresent:
			desired[index].DriftState = DriftConsistent
		case EvidenceMissing:
			desired[index].DriftState = DriftMissing
		default:
			desired[index].DriftState = DriftUnknown
		}
	}
	for factIndex, fact := range facts {
		if usedFacts[factIndex] || fact.Kind != FactKindObserved {
			continue
		}
		state := EvidencePresent
		drift := DriftUnexpected
		if stale {
			state = EvidenceStale
			drift = DriftUnknown
		}
		desired = append(desired, Allocation{
			ID:        stableID("runtime-fact", string(fact.Transport), fact.BindAddress, fmt.Sprint(fact.Port)),
			Transport: fact.Transport, BindAddress: fact.BindAddress, Port: fact.Port,
			Exposure: fact.Exposure, ObservedState: state, ExposedState: EvidenceUnknown,
			DriftState: drift,
		})
	}
	sort.Slice(desired, func(i, j int) bool {
		if desired[i].Port != desired[j].Port {
			return desired[i].Port < desired[j].Port
		}
		if desired[i].Transport != desired[j].Transport {
			return desired[i].Transport < desired[j].Transport
		}
		return desired[i].ID < desired[j].ID
	})
	return desired
}

func evidenceStateFor(allocation Allocation, kind FactKind, facts []RuntimeFact, complete, stale bool, used map[int]bool) EvidenceState {
	if stale {
		return EvidenceStale
	}
	for index, fact := range facts {
		if fact.Kind == kind && fact.Transport == allocation.Transport && fact.BindAddress == allocation.BindAddress && fact.Port == allocation.Port {
			used[index] = true
			return EvidencePresent
		}
	}
	if complete {
		return EvidenceMissing
	}
	return EvidenceUnknown
}
