package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/stackkits/contracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
)

const createFailureThreshold = 3
const createFailureWindow = 15 * time.Minute
const createPauseDuration = 15 * time.Minute

// CreateProtection is a secret-free projection of existing native custody.
// It is never a permit; admission reads it again under the scope lock.
type CreateProtection struct {
	ProviderID          string                 `json:"provider_id"`
	Region              string                 `json:"region"`
	State               string                 `json:"state"`
	ReasonCode          string                 `json:"reason_code"`
	ConsecutiveFailures int                    `json:"consecutive_failures"`
	RetryAt             *time.Time             `json:"retry_at,omitempty"`
	LastFailure         *CreateFailure         `json:"last_failure,omitempty"`
	Recovery            string                 `json:"recovery"`
	Policy              CreateProtectionPolicy `json:"policy"`
}

type CreateProtectionPolicy struct {
	Threshold       int `json:"threshold"`
	WindowSeconds   int `json:"window_seconds"`
	CooldownSeconds int `json:"cooldown_seconds"`
}

type CreateFailure struct {
	Code string    `json:"code"`
	At   time.Time `json:"at"`
}

type CreateProtectionError struct{ Protection CreateProtection }

func (e CreateProtectionError) Error() string { return "providercontrol: " + e.Protection.ReasonCode }
func (e CreateProtectionError) Unwrap() error { return ErrProviderCreateBlocked }

// ProviderCreateRegionResolver derives the actual provider placement using
// the same immutable adapter configuration as create, without provider I/O.
type ProviderCreateRegionResolver interface {
	ProviderCreateRegion(offeringID, requestedRegion string) (string, error)
}

func (r *Registry) CanonicalCreateRegion(provider, offering, requested string) (string, error) {
	if r == nil {
		return "", ErrAdapterNotRegistered
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, adapter := range r.adapters {
		if adapter.providerID == provider && adapter.createRegion != nil {
			return adapter.createRegion(offering, requested)
		}
	}
	// Development executors have no managed provider configuration. Production
	// composition requires this resolver on every managed provider adapter.
	return strings.ToLower(strings.TrimSpace(requested)), nil
}

func (r *Runtime) ReadCreateProtection(ctx context.Context, tenant, provider, region string) (CreateProtection, error) {
	if r == nil || r.ledger == nil {
		return CreateProtection{}, ErrAdapterNotRegistered
	}
	_, provider, region, err := canonicalCreateScope(tenant, provider, region)
	if err != nil {
		return CreateProtection{}, err
	}
	r.registry.mu.RLock()
	configured := false
	for _, adapter := range r.registry.adapters {
		if adapter.providerID == provider && adapter.createRegion != nil {
			configured = true
			break
		}
	}
	r.registry.mu.RUnlock()
	if !configured {
		return CreateProtection{}, ErrProfileUnavailable
	}
	region, err = r.registry.CanonicalCreateRegion(provider, string(serverruntime.RuntimeOfferingStandard), region)
	if err != nil {
		return CreateProtection{}, err
	}
	return ReadCreateProtection(ctx, r.ledger.db, tenant, provider, region)
}

type createOutcome struct {
	OperationID     string
	RequestedAt     time.Time
	Head            providerexecutor.Receipt
	Failure         *CreateFailure
	Attempted       bool
	DispatchGuarded bool
	Resolved        bool
	Unknown         bool
}

func canonicalCreateScope(tenant, provider, region string) (string, string, string, error) {
	tenant, provider, region = strings.TrimSpace(tenant), strings.ToLower(strings.TrimSpace(provider)), strings.ToLower(strings.TrimSpace(region))
	if tenant == "" || !canonicalCatalogIdentifier(provider) || len(region) > 128 || strings.ContainsAny(region, "\r\n\x00") {
		return "", "", "", fmt.Errorf("%w: invalid create protection scope", ErrInvalidRequest)
	}
	return tenant, provider, region, nil
}

func qualifyingCreateFailure(code string) bool {
	switch code {
	case providerexecutor.ReasonCodeProviderAuth, providerexecutor.ReasonCodeProviderPermission,
		providerexecutor.ReasonCodeProviderQuota, providerexecutor.ReasonCodeProviderRateLimit,
		providerexecutor.ReasonCodeProviderTransient, providerexecutor.ReasonCodeProviderTimeout,
		providerexecutor.ReasonCodeProviderPartialCreate, providerexecutor.ReasonCodeProviderCleanupRequired:
		return true
	}
	return false
}

// Outcomes are ordered by immutable admission time, not completion time. An
// older completion therefore cannot reset a newer failure epoch. Each native
// operation contributes at most one failure, regardless of receipt replays.
func projectCreateProtection(provider, region string, now time.Time, outcomes []createOutcome) CreateProtection {
	outcomes = append([]createOutcome(nil), outcomes...)
	sort.SliceStable(outcomes, func(i, j int) bool { return outcomes[i].RequestedAt.Before(outcomes[j].RequestedAt) })
	p := CreateProtection{ProviderID: provider, Region: region, State: "ready", ReasonCode: "provider_create_ready", Recovery: "none", Policy: CreateProtectionPolicy{createFailureThreshold, int(createFailureWindow.Seconds()), int(createPauseDuration.Seconds())}}
	var failures []time.Time
	var observedFailures []*CreateFailure
	armed := false
	seen := make(map[string]bool)
	for _, outcome := range outcomes {
		if seen[outcome.OperationID] {
			continue
		}
		seen[outcome.OperationID] = true
		if outcome.Head.Status == providerexecutor.StatusSucceeded && outcome.Head.Phase == providerexecutor.PhasePresent {
			observedFailures = nil
			continue
		}
		if outcome.Attempted && outcome.Failure != nil && qualifyingCreateFailure(outcome.Failure.Code) {
			observedFailures = append(observedFailures, outcome.Failure)
		}
	}
	sort.SliceStable(observedFailures, func(i, j int) bool { return observedFailures[i].At.Before(observedFailures[j].At) })
	for _, failure := range observedFailures {
		at := failure.At
		kept := failures[:0]
		for _, previous := range failures {
			if !previous.Before(at.Add(-createFailureWindow)) {
				kept = append(kept, previous)
			}
		}
		failures = append(kept, at)
		p.ConsecutiveFailures++
		p.LastFailure = failure
		armed = armed || len(failures) >= createFailureThreshold
		if armed {
			retry := at.Add(createPauseDuration)
			p.RetryAt = &retry
		}
	}
	if p.RetryAt != nil {
		p.Recovery = "automatic_after_cooldown"
		if now.Before(*p.RetryAt) {
			p.State, p.ReasonCode = "paused", "provider_create_failure_pause"
		}
	}
	for _, outcome := range outcomes {
		if outcome.Resolved {
			continue
		}
		if outcome.Unknown {
			p.State, p.ReasonCode, p.Recovery = "outcome_unknown", "provider_create_outcome_unknown", "reconcile_existing_create"
			p.RetryAt = nil
			return p
		}
		p.State, p.ReasonCode, p.Recovery = "probe_in_flight", "provider_create_in_flight", "wait_for_current_create"
	}
	return p
}

// ReadCreateProtection reads canonical tenant-RLS-scoped operations. No state
// table, provider call, credentials or alternate RuntimeLease is introduced.
func ReadCreateProtection(ctx context.Context, db *sql.DB, tenant, provider, region string) (CreateProtection, error) {
	tenant, provider, region, err := canonicalCreateScope(tenant, provider, region)
	if err != nil {
		return CreateProtection{}, err
	}
	if db == nil {
		return CreateProtection{}, fmt.Errorf("providercontrol: create protection database missing")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return CreateProtection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT set_config($1,$2,true)`, tenantContextKey, tenant); err != nil {
		return CreateProtection{}, err
	}
	p, err := readCreateProtectionTx(ctx, tx, tenant, provider, region)
	if err != nil {
		return CreateProtection{}, err
	}
	return p, tx.Commit()
}

func requireCreateProtectionTx(ctx context.Context, tx *sql.Tx, tenant, provider, region string) error {
	tenant, provider, region, err := canonicalCreateScope(tenant, provider, region)
	if err != nil {
		return err
	}
	// Serialize all keys, leases, replicas and connections in this billing
	// account/provider/region. Changing credentials must not bypass a pause.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1),hashtext($2))`, tenant, "provider-create:"+provider+":"+region); err != nil {
		return err
	}
	p, err := readCreateProtectionTx(ctx, tx, tenant, provider, region)
	if err != nil {
		return err
	}
	if p.State != "ready" {
		return CreateProtectionError{p}
	}
	return nil
}

func readCreateProtectionTx(ctx context.Context, tx *sql.Tx, tenant, provider, region string) (CreateProtection, error) {
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return CreateProtection{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT o.operation_id, o.created_at, r.receipt_json,
		       EXISTS(SELECT 1 FROM provider_provision_dispatch_guards g WHERE g.tenant_id=o.tenant_id AND g.operation_id=o.operation_id AND g.guard_origin='first_claim'),
		       EXISTS(SELECT 1 FROM provider_provision_dispatch_guards g WHERE g.tenant_id=o.tenant_id AND g.lease_id=o.lease_id AND g.resource_generation_id=(o.command_json #>> '{command,resource_generation_id}')::uuid AND (g.operation_id<>o.operation_id OR g.dispatch_mode='at_most_once_dispatch_manual_reconcile' OR g.guard_origin='migration_quarantine')),
		       COALESCE(f.receipt_json->'reason'->>'code',''), f.issued_at,
		       EXISTS(SELECT 1 FROM provider_operation_resource_free_terminalizations t WHERE t.tenant_id=o.tenant_id AND t.operation_id=o.operation_id AND t.head_receipt_digest=o.head_receipt_digest),
		       EXISTS(SELECT 1 FROM provider_operations d WHERE d.tenant_id=o.tenant_id AND d.lease_id=o.lease_id AND d.operation='decommission' AND d.status='succeeded' AND d.phase='absent' AND d.command_json->'command'->>'resource_generation_id'=o.command_json->'command'->>'resource_generation_id'),
		       EXISTS(SELECT 1 FROM provider_operation_execution_claims c WHERE c.tenant_id=o.tenant_id AND c.operation_id=o.operation_id AND c.head_receipt_digest=o.head_receipt_digest AND c.state='active' AND c.lease_expires_at>clock_timestamp())
		FROM provider_operations o
		JOIN techstack_vm_leases l ON l.tenant_id=o.tenant_id AND l.id=o.lease_id
		JOIN provider_operation_receipts r ON r.tenant_id=o.tenant_id AND r.operation_id=o.operation_id AND r.sequence=o.head_sequence
		LEFT JOIN LATERAL (
		    SELECT receipt_json, issued_at FROM provider_operation_receipts f
		    WHERE f.tenant_id=o.tenant_id AND f.operation_id=o.operation_id AND f.status='failed'
		      AND f.receipt_json->'reason'->>'code' IN ('provider.auth','provider.permission','provider.quota','provider.rate_limit','provider.transient','provider.timeout','provider.partial_create','provider.cleanup_required')
		    ORDER BY f.sequence LIMIT 1
		) f ON true
		WHERE o.tenant_id=$1 AND o.operation='provision'
		  AND o.command_json->'command'->>'provider_id'=$2
		  AND (NOT (o.command_json ? 'create_region') OR lower(trim(o.command_json->>'create_region'))=$3)
		  AND COALESCE(l.lease_json->>'custody_class','')<>'customer_substrate'
		ORDER BY o.created_at, o.operation_id`, tenant, provider, region)
	if err != nil {
		return CreateProtection{}, fmt.Errorf("providercontrol: read create protection: %w", err)
	}
	defer rows.Close()
	var outcomes []createOutcome
	for rows.Next() {
		var o createOutcome
		var raw []byte
		var code string
		var failedAt sql.NullTime
		var resourceFree, deleted, activeClaim bool
		if err := rows.Scan(&o.OperationID, &o.RequestedAt, &raw, &o.Attempted, &o.DispatchGuarded, &code, &failedAt, &resourceFree, &deleted, &activeClaim); err != nil {
			return CreateProtection{}, err
		}
		if err := json.Unmarshal(raw, &o.Head); err != nil {
			return CreateProtection{}, err
		}
		if code != "" && failedAt.Valid {
			o.Failure = &CreateFailure{code, failedAt.Time}
		}
		o.classifyCustody(resourceFree, deleted, activeClaim)
		outcomes = append(outcomes, o)
	}
	if err := rows.Err(); err != nil {
		return CreateProtection{}, err
	}
	return projectCreateProtection(provider, region, now, outcomes), nil
}

func (o *createOutcome) classifyCustody(resourceFree, deleted, activeClaim bool) {
	o.Resolved = deleted || resourceFree || o.Head.Phase == providerexecutor.PhasePresent || (!o.Attempted && !o.DispatchGuarded && (o.Head.Phase == providerexecutor.PhaseDenied || o.Head.Phase == providerexecutor.PhaseFailed))
	// Definitive rejection without retained handles proves no provider effect.
	if o.Attempted && o.Head.Phase == providerexecutor.PhaseFailed && len(o.Head.Resources) == 0 && o.Head.Reason != nil {
		switch o.Head.Reason.Code {
		case providerexecutor.ReasonCodeProviderAuth, providerexecutor.ReasonCodeProviderPermission, providerexecutor.ReasonCodeProviderInvalidSpec, providerexecutor.ReasonCodeProviderQuota, providerexecutor.ReasonCodeProviderRateLimit, providerexecutor.ReasonCodeProviderConflict, providerexecutor.ReasonCodeProviderNotFound:
			o.Resolved = true
		}
	}
	// Centron deliberately leaves ambiguous AMO creates at accepted, while
	// other adapters retain planned. Neither head can admit another create
	// after its dispatch capability disappears.
	guardedWithoutClaim := (o.Head.Phase == providerexecutor.PhaseAccepted || o.Head.Phase == providerexecutor.PhasePlanned) && !activeClaim
	// Historical migration quarantine retains custody but is not evidence of
	// a newly admitted dispatch and must never inflate the failure counter.
	o.Unknown = !o.Resolved && (o.Attempted || o.DispatchGuarded) && (guardedWithoutClaim || o.Head.Phase == providerexecutor.PhaseFailed || (o.Head.Reason != nil && (o.Head.Reason.Code == providerexecutor.ReasonCodeProviderPartialCreate || o.Head.Reason.Code == providerexecutor.ReasonCodeProviderTimeout || o.Head.Reason.Code == providerexecutor.ReasonCodeProviderCleanupRequired)))
}
