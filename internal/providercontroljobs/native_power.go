package providercontroljobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	nativePowerSource = "provider_control_power_intent"
	// nativePowerAdvanceSteps bounds the synchronous part of a power request:
	// requested->accepted, the single side-effecting step, and one read-only
	// observation. The provider-control reconciler finishes convergence.
	nativePowerAdvanceSteps = 3
)

// NativePowerController converges one exact managed lease generation to a
// power state through a generation-bound provider-control reconcile. It never
// creates, replaces or deletes a provider resource and never retries a
// provider call itself: the coordinator's at-most-once claim owns dispatch.
type NativePowerController struct {
	database    *sql.DB
	application providercontrol.OperationApplication
	servers     *controlplane.PostgresStore
	now         func() time.Time
}

// NativePowerConfig composes the controller from the same database and
// application instances as the provider-control runtime.
type NativePowerConfig struct {
	Database    *sql.DB
	Application providercontrol.OperationApplication
}

func NewNativePowerController(cfg NativePowerConfig) (*NativePowerController, error) {
	if cfg.Database == nil || cfg.Application == nil {
		return nil, fmt.Errorf("providercontroljobs: native power database and application are required")
	}
	return &NativePowerController{
		database: cfg.Database, application: cfg.Application,
		servers: controlplane.NewPostgresStore(cfg.Database),
		now:     func() time.Time { return time.Now().UTC() },
	}, nil
}

type nativePowerCandidate struct {
	TenantID           string
	LeaseID            string
	LeaseRevision      uint64
	ServerID           string
	ServerRevision     int64
	ServerGeneration   int64
	ResourceGeneration string
	ProviderID         string
	Targets            []providerexecutor.ResourceTarget
	Attempt            int
	SpecRevision       uint64
	RequestedAt        time.Time
	Existing           *providercontrol.OperationRecord
}

// RequestPower admits or replays one power transition and advances it a
// bounded number of steps. A same-target request while that transition is
// still converging replays it; a different target is refused.
func (p *NativePowerController) RequestPower(
	ctx context.Context,
	req monthlyruntime.PowerRequest,
) (monthlyruntime.PowerOperation, error) {
	if p == nil || p.database == nil || p.application == nil {
		return monthlyruntime.PowerOperation{}, monthlyruntime.ErrPowerNotAdmitted
	}
	state := providercontrol.PowerState(req.State)
	req.TenantID, req.OwnerID, req.LeaseID = strings.TrimSpace(req.TenantID), strings.TrimSpace(req.OwnerID), strings.TrimSpace(req.LeaseID)
	if req.TenantID == "" || req.OwnerID == "" || req.LeaseID == "" || !state.Valid() {
		return monthlyruntime.PowerOperation{}, fmt.Errorf("%w: tenant, owner, lease and target state are required", monthlyruntime.ErrPowerNotAdmitted)
	}
	candidate, err := p.preparePower(ctx, req, state)
	if err != nil {
		return monthlyruntime.PowerOperation{}, err
	}
	record := providercontrol.OperationRecord{}
	replay := candidate.Existing != nil
	if replay {
		record = *candidate.Existing
	} else {
		payload, specErr := providercontrol.NewPowerDesiredSpec(candidate.ProviderID, state)
		if specErr != nil {
			return monthlyruntime.PowerOperation{}, specErr
		}
		ledgerRevision, valid := positiveRevision(candidate.ServerRevision)
		if !valid {
			return monthlyruntime.PowerOperation{}, fmt.Errorf("%w: runtime server has no native revision", monthlyruntime.ErrPowerNotAdmitted)
		}
		record, _, err = p.application.Start(ctx, providercontrol.StartRequest{
			TenantID: candidate.TenantID, LeaseID: candidate.LeaseID, LeaseRevision: candidate.LeaseRevision,
			RuntimeServerID: candidate.ServerID, ResourceGenerationID: candidate.ResourceGeneration,
			Operation:      providerexecutor.OperationReconcile,
			IdempotencyKey: nativePowerIdempotencyKey(candidate.LeaseID, candidate.ResourceGeneration, candidate.Attempt, state),
			LedgerRevision: ledgerRevision,
			Targets:        candidate.Targets,
			DesiredSpec: &providercontrol.DesiredSpecRevision{
				TenantID: candidate.TenantID, LeaseID: candidate.LeaseID, Revision: candidate.SpecRevision,
				Ref:     "desired-spec://techstack/leases/" + candidate.LeaseID + "/revisions/" + strconv.FormatUint(candidate.SpecRevision, 10),
				Digest:  providercontrol.DesiredSpecDigest(payload),
				Payload: payload, CreatedAt: candidate.RequestedAt,
			},
			RequestedAt: candidate.RequestedAt,
		})
		if err != nil {
			return monthlyruntime.PowerOperation{}, err
		}
	}
	// The durable operation exists, so project the owner's intent onto the
	// canonical RuntimeServer. A projection conflict never undoes the ledgered
	// operation; the next request or status read converges it again.
	if intentErr := p.projectPowerIntent(ctx, candidate, state); intentErr != nil {
		slog.Warn("provider_control_power_intent_projection_failed",
			"tenant_id", candidate.TenantID, "lease_id", candidate.LeaseID, "error", intentErr.Error())
	}
	record = p.advancePower(ctx, record)
	operation := powerOperationView(record, state)
	operation.Replay = replay
	return operation, nil
}

// LatestPower returns the newest power reconcile of the lease's current
// generation, or nil when the generation has never been power-cycled.
func (p *NativePowerController) LatestPower(ctx context.Context, tenantID, leaseID string) (*monthlyruntime.PowerOperation, error) {
	if p == nil || p.database == nil || p.application == nil {
		return nil, nil
	}
	tenantID, leaseID = strings.TrimSpace(tenantID), strings.TrimSpace(leaseID)
	var operationID, desired string
	err := p.withTenant(ctx, tenantID, true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT operation.operation_id, COALESCE(spec.spec_json->>'power_state', '')
			FROM provider_operations AS operation
			JOIN techstack_vm_leases AS lease
			  ON lease.tenant_id = operation.tenant_id AND lease.id = operation.lease_id
			LEFT JOIN provider_desired_spec_revisions AS spec
			  ON spec.tenant_id = operation.tenant_id AND spec.lease_id = operation.lease_id
			 AND spec.revision = operation.desired_spec_revision
			WHERE operation.tenant_id = $1 AND operation.lease_id = $2
			  AND operation.operation = 'reconcile'
			  AND operation.command_json #>> '{command,resource_generation_id}' = lease.resource_generation_id::text
			ORDER BY operation.created_at DESC, operation.operation_id DESC
			LIMIT 1
		`, tenantID, leaseID).Scan(&operationID, &desired)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: load latest power operation: %w", err)
	}
	record, err := p.application.Get(ctx, tenantID, operationID)
	if err != nil {
		return nil, err
	}
	view := powerOperationView(record, providercontrol.PowerState(desired))
	return &view, nil
}

func (p *NativePowerController) advancePower(ctx context.Context, record providercontrol.OperationRecord) providercontrol.OperationRecord {
	for step := 0; step < nativePowerAdvanceSteps; step++ {
		if record.Head.Status != providerexecutor.StatusPending {
			return record
		}
		advanced, changed, err := p.application.Advance(ctx, record.Command.TenantID, record.Command.OperationID)
		if err != nil || !changed {
			// Contention, a closed activation gate or a transient ledger error
			// leaves the durable operation for the reconciler; the caller sees
			// the current head, never a fabricated success.
			if err == nil {
				return advanced
			}
			return record
		}
		record = advanced
	}
	return record
}

func (p *NativePowerController) preparePower(
	ctx context.Context,
	req monthlyruntime.PowerRequest,
	state providercontrol.PowerState,
) (nativePowerCandidate, error) {
	candidate := nativePowerCandidate{TenantID: req.TenantID, LeaseID: req.LeaseID}
	err := p.withTenant(ctx, req.TenantID, false, func(tx *sql.Tx) error {
		var ownerID, subjectID, serverLifecycle, serverDesired, leaseDesired string
		var leaseRevision int64
		var cancelled bool
		var pausable bool
		if err := tx.QueryRowContext(ctx, `
			SELECT lease.lease_revision, lease.server_id, lease.resource_generation_id::text,
			       LOWER(BTRIM(lease.provider_id)), lease.owner_subject_id,
			       COALESCE(lease.lease_json->'subject'->>'id', ''),
			       lease.cancelled_at IS NOT NULL, lease.desired_state,
			       server.revision, server.generation, server.lifecycle_state, server.desired_state,
			       EXISTS (
			           SELECT 1
			           FROM provider_catalog_profiles AS profile
			           JOIN provider_catalog_versions AS version
			             ON version.catalog_version = profile.catalog_version
			           WHERE version.status = 'active'
			             AND profile.provider_id = LOWER(BTRIM(lease.provider_id))
			             AND profile.offering_id = COALESCE(lease.lease_json->'metadata'->>'runtime_offering_id', '')
			             AND profile.can_pause
			             AND profile.stop_effect = 'pause'
			       )
			FROM techstack_vm_leases AS lease
			JOIN runtime_lease_execution_authorities AS authority
			  ON authority.tenant_id = lease.tenant_id
			 AND authority.lease_id = lease.id
			 AND authority.execution_authority = 'techstack_provider_control'
			JOIN servers AS server
			  ON server.tenant_id = lease.tenant_id
			 AND server.id = lease.server_id
			 AND server.lease_id = lease.id
			WHERE lease.tenant_id = $1 AND lease.id = $2
			FOR UPDATE OF lease, server
		`, req.TenantID, req.LeaseID).Scan(
			&leaseRevision, &candidate.ServerID, &candidate.ResourceGeneration,
			&candidate.ProviderID, &ownerID, &subjectID, &cancelled, &leaseDesired,
			&candidate.ServerRevision, &candidate.ServerGeneration, &serverLifecycle, &serverDesired,
			&pausable,
		); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: lease is not under native provider control", monthlyruntime.ErrPowerNotAdmitted)
			}
			return fmt.Errorf("providercontroljobs: lock power candidate: %w", err)
		}
		revision, valid := positiveRevision(leaseRevision)
		owned := ownerID == req.OwnerID || subjectID == req.OwnerID
		if !valid || !owned || cancelled ||
			leaseDesired == "absent" || leaseDesired == "archived" ||
			serverDesired == string(serverregistry.DesiredAbsent) ||
			serverLifecycle != string(serverregistry.LifecycleActive) || !pausable {
			return fmt.Errorf("%w: lease=%t owner=%t cancelled=%t lifecycle=%s pausable=%t",
				monthlyruntime.ErrPowerNotAdmitted, valid, owned, cancelled, serverLifecycle, pausable)
		}
		candidate.LeaseRevision = revision
		existing, attempts, err := p.latestPowerTx(ctx, tx, candidate)
		if err != nil {
			return err
		}
		if existing != nil && existing.record.Head.Status == providerexecutor.StatusPending {
			if existing.desired != state {
				return monthlyruntime.ErrPowerTransitionInProgress
			}
			candidate.Existing = &existing.record
			return nil
		}
		candidate.Attempt = attempts + 1
		targets, err := loadPresentGenerationTargetsTx(ctx, tx, candidate)
		if err != nil {
			return err
		}
		candidate.Targets = targets
		var specRevision int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(revision), 0) + 1 FROM provider_desired_spec_revisions
			WHERE tenant_id = $1 AND lease_id = $2
		`, req.TenantID, req.LeaseID).Scan(&specRevision); err != nil {
			return fmt.Errorf("providercontroljobs: allocate power desired spec revision: %w", err)
		}
		nextRevision, valid := positiveRevision(specRevision)
		if !valid {
			return fmt.Errorf("%w: desired spec revision is invalid", monthlyruntime.ErrPowerNotAdmitted)
		}
		candidate.SpecRevision = nextRevision
		var databaseNow time.Time
		if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return fmt.Errorf("providercontroljobs: read power request time: %w", err)
		}
		candidate.RequestedAt = databaseNow.UTC().Truncate(time.Microsecond)
		return nil
	})
	return candidate, err
}

type latestPowerRecord struct {
	record  providercontrol.OperationRecord
	desired providercontrol.PowerState
}

func (p *NativePowerController) latestPowerTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate nativePowerCandidate,
) (*latestPowerRecord, int, error) {
	var attempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM provider_operations
		WHERE tenant_id = $1 AND lease_id = $2 AND operation = 'reconcile'
		  AND command_json #>> '{command,resource_generation_id}' = $3
	`, candidate.TenantID, candidate.LeaseID, candidate.ResourceGeneration).Scan(&attempts); err != nil {
		return nil, 0, fmt.Errorf("providercontroljobs: count power operations: %w", err)
	}
	if attempts == 0 {
		return nil, 0, nil
	}
	var operationID, desired string
	if err := tx.QueryRowContext(ctx, `
		SELECT operation.operation_id, COALESCE(spec.spec_json->>'power_state', '')
		FROM provider_operations AS operation
		LEFT JOIN provider_desired_spec_revisions AS spec
		  ON spec.tenant_id = operation.tenant_id AND spec.lease_id = operation.lease_id
		 AND spec.revision = operation.desired_spec_revision
		WHERE operation.tenant_id = $1 AND operation.lease_id = $2 AND operation.operation = 'reconcile'
		  AND operation.command_json #>> '{command,resource_generation_id}' = $3
		ORDER BY operation.created_at DESC, operation.operation_id DESC
		LIMIT 1
	`, candidate.TenantID, candidate.LeaseID, candidate.ResourceGeneration).Scan(&operationID, &desired); err != nil {
		return nil, 0, fmt.Errorf("providercontroljobs: load latest power operation: %w", err)
	}
	record, err := p.application.Get(ctx, candidate.TenantID, operationID)
	if err != nil {
		return nil, 0, err
	}
	return &latestPowerRecord{record: record, desired: providercontrol.PowerState(desired)}, attempts, nil
}

// projectPowerIntent records the owner's desired power state on the
// canonical RuntimeServer so read models and allowed actions follow the
// intent. It re-reads the server head under lock and is idempotent.
func (p *NativePowerController) projectPowerIntent(
	ctx context.Context,
	candidate nativePowerCandidate,
	state providercontrol.PowerState,
) error {
	desired := serverregistry.DesiredRunning
	if state == providercontrol.PowerStateStopped {
		desired = serverregistry.DesiredStopped
	}
	return p.withTenant(ctx, candidate.TenantID, false, func(tx *sql.Tx) error {
		var revision, generation int64
		var current string
		if err := tx.QueryRowContext(ctx, `
			SELECT revision, generation, desired_state FROM servers
			WHERE tenant_id = $1 AND id = $2 AND lease_id = $3
			FOR UPDATE
		`, candidate.TenantID, candidate.ServerID, candidate.LeaseID).Scan(&revision, &generation, &current); err != nil {
			return err
		}
		if current == string(desired) {
			return nil
		}
		return p.applyPowerIntentTx(ctx, tx, candidate, revision, generation, desired, state)
	})
}

func (p *NativePowerController) applyPowerIntentTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate nativePowerCandidate,
	revision, generation int64,
	desired serverregistry.DesiredState,
	state providercontrol.PowerState,
) error {
	_, err := p.servers.ApplyServerEventTx(ctx, tx, controlplane.ServerEvent{
		TenantID: candidate.TenantID, ServerID: candidate.ServerID,
		ExpectedRevision: revision, Generation: generation,
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    nativePowerSource, SourceID: nativePowerSource + ":" + candidate.LeaseID,
		ObservedAt: p.now(),
		Runtime: controlplane.ServerRuntime{
			DesiredState:      string(desired),
			DesiredReasonCode: "owner_requested_power_" + string(state),
		},
		Evidence: map[string]any{
			"lease_id":               candidate.LeaseID,
			"resource_generation_id": candidate.ResourceGeneration,
			"power_attempt":          candidate.Attempt,
		},
	})
	if err != nil {
		return fmt.Errorf("providercontroljobs: persist RuntimeServer power intent: %w", err)
	}
	return nil
}

// loadPresentGenerationTargetsTx returns the exact resource graph of the
// generation's succeeded provision. The ledger re-validates the same graph
// when it seals the reconcile command.
func loadPresentGenerationTargetsTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate nativePowerCandidate,
) ([]providerexecutor.ResourceTarget, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT resource.binding_id, resource.kind, resource.native_ref,
		       COALESCE(resource.parent_binding_id, ''), resource.ownership_hash, resource.disposition
		FROM provider_operations AS operation
		JOIN provider_operation_resources AS resource
		  ON resource.tenant_id = operation.tenant_id
		 AND resource.operation_id = operation.operation_id
		WHERE operation.tenant_id = $1
		  AND operation.lease_id = $2
		  AND operation.operation = 'provision'
		  AND operation.status = 'succeeded'
		  AND operation.phase = 'present'
		  AND operation.command_json #>> '{command,resource_generation_id}' = $3
		ORDER BY resource.binding_id
	`, candidate.TenantID, candidate.LeaseID, candidate.ResourceGeneration)
	if err != nil {
		return nil, fmt.Errorf("providercontroljobs: load power targets: %w", err)
	}
	defer rows.Close()
	targets := make([]providerexecutor.ResourceTarget, 0, 5)
	for rows.Next() {
		var target providerexecutor.ResourceTarget
		var disposition string
		if err := rows.Scan(&target.BindingID, &target.Kind, &target.NativeRef,
			&target.ParentBindingID, &target.OwnershipHash, &disposition); err != nil {
			return nil, fmt.Errorf("providercontroljobs: scan power target: %w", err)
		}
		target.Disposition = providerexecutor.ResourceDisposition(disposition)
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("providercontroljobs: iterate power targets: %w", err)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("%w: generation has no present provider graph", monthlyruntime.ErrPowerNotAdmitted)
	}
	return targets, nil
}

func (p *NativePowerController) withTenant(ctx context.Context, tenantID string, readOnly bool, fn func(*sql.Tx) error) error {
	tx, err := p.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return fmt.Errorf("providercontroljobs: begin power transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return fmt.Errorf("providercontroljobs: scope power tenant: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// nativePowerIdempotencyKey is one semantic attempt: the lease, the exact
// resource generation, the attempt ordinal within that generation and the
// target. Replaying an in-flight request reuses the durable record instead.
func nativePowerIdempotencyKey(leaseID, resourceGeneration string, attempt int, state providercontrol.PowerState) string {
	return "native-power:" + leaseID + ":" + resourceGeneration + ":" + strconv.Itoa(attempt) + ":" + string(state)
}

func powerOperationView(record providercontrol.OperationRecord, desired providercontrol.PowerState) monthlyruntime.PowerOperation {
	view := monthlyruntime.PowerOperation{
		OperationID:          record.Command.OperationID,
		DesiredPowerState:    monthlyruntime.PowerState(desired),
		Status:               string(record.Head.Status),
		Phase:                string(record.Head.Phase),
		ResourceGenerationID: record.Command.ResourceGenerationID,
		ReceiptSequence:      record.Head.Sequence,
		ReceiptDigest:        record.Head.ReceiptDigest,
		RequestedAt:          record.Command.RequestedAt.UTC(),
	}
	if record.Head.Reason != nil {
		view.ReasonCode = record.Head.Reason.Code
		view.Retryable = record.Head.Reason.Retryable
	}
	return view
}

var _ monthlyruntime.PowerController = (*NativePowerController)(nil)
