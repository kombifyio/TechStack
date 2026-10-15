package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ServiceOperationJobStore is the complete admission boundary for durable
// service work. A page of job history is never an implementation of this read.
type ServiceOperationJobStore interface {
	// Internal recovery read; never use its payload as an API response.
	GetServiceOperationJob(ctx context.Context, tenantID, stackID, serviceID, jobID string) (*Job, error)
	FindUnresolvedServiceJob(ctx context.Context, tenantID, stackID, serviceID, excludeJobID string) (string, error)
	CreateServiceOperationJob(ctx context.Context, req UpsertJobRequest, serviceID string) (*Job, error)
}

// Distinguish a competing operation from an existing idempotency key: the
// latter may be reconciled, while the former must never re-enqueue that key.
var ErrServiceOperationBusy = errors.New("controlplane: service operation remains unresolved")

func serviceJobTarget(result, payload map[string]any, serviceID string) bool {
	receipt, _ := result["service_action_receipt"].(map[string]any)
	receiptID, _ := receipt["service_id"].(string)
	payloadID, _ := payload["service_id"].(string)
	return receiptID == serviceID || payloadID == serviceID
}

func unresolvedServiceJob(job Job, tenantID, stackID, serviceID, excludeJobID string) bool {
	if job.TenantID != tenantID || job.StackID != stackID || job.ID == excludeJobID || !serviceJobTarget(job.Result, job.Payload, serviceID) {
		return false
	}
	unresolved, _ := job.Result["connector_outcome_unresolved"].(bool)
	state := strings.ToLower(strings.TrimSpace(job.State))
	return unresolved || state == jobStatePending || state == jobStateRunning || state == jobStateWaiting
}

func serviceJobScope(tenantID, stackID, serviceID string) error {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(stackID) == "" || strings.TrimSpace(serviceID) == "" || tenantID != strings.TrimSpace(tenantID) || stackID != strings.TrimSpace(stackID) || serviceID != strings.TrimSpace(serviceID) {
		return fmt.Errorf("controlplane: exact tenant, stack and service scope required")
	}
	return nil
}

func serviceJobReservation(req UpsertJobRequest, serviceID string) error {
	if err := serviceJobScope(req.TenantID, req.StackID, serviceID); err != nil {
		return err
	}
	receipt, _ := req.Result["service_action_receipt"].(map[string]any)
	for _, target := range []any{receipt["service_id"], req.Payload["service_id"]} {
		if target != nil {
			id, ok := target.(string)
			if !ok || (id != "" && id != serviceID) {
				return fmt.Errorf("controlplane: reservation has conflicting service targets")
			}
		}
	}
	if !serviceJobTarget(req.Result, req.Payload, serviceID) {
		return fmt.Errorf("controlplane: reservation does not target selected service")
	}
	return nil
}
