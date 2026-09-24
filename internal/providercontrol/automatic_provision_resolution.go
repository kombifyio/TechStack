package providercontrol

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const automaticProvisionResolutionQueueSize = 100

// automaticProvisionResolver converts the provider-neutral, read-only
// discovery result for a parked at-most-once provision into its append-only
// zero/one/many decision. It never retries provider creation. Multiple
// candidates remain quarantined and zero candidates remain eligible for a
// later observation.
type automaticProvisionResolver struct {
	service ProvisionResolutionApplication
	subject string
	onError func(error)
	queue   chan OperationRef
	queued  sync.Map
}

func newAutomaticProvisionResolver(
	service ProvisionResolutionApplication,
	subject string,
	onError func(error),
) (*automaticProvisionResolver, error) {
	subject = strings.TrimSpace(subject)
	if service == nil || subject == "" {
		return nil, fmt.Errorf("%w: automatic provision resolution service and subject are required", ErrInvalidRequest)
	}
	return &automaticProvisionResolver{
		service: service, subject: subject, onError: onError,
		queue: make(chan OperationRef, automaticProvisionResolutionQueueSize),
	}, nil
}

func (r *automaticProvisionResolver) enqueue(operation OperationRef) {
	if r == nil {
		return
	}
	operation.TenantID = strings.TrimSpace(operation.TenantID)
	operation.OperationID = strings.TrimSpace(operation.OperationID)
	if operation.TenantID == "" || operation.OperationID == "" {
		r.report(fmt.Errorf("%w: automatic provision resolution operation identity is required", ErrInvalidRequest))
		return
	}
	key := operation.TenantID + "\x00" + operation.OperationID
	if _, loaded := r.queued.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	select {
	case r.queue <- operation:
	default:
		r.queued.Delete(key)
		r.report(fmt.Errorf("providercontrol: automatic provision resolution queue is full"))
	}
}

func (r *automaticProvisionResolver) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case operation := <-r.queue:
			key := operation.TenantID + "\x00" + operation.OperationID
			err := r.resolve(ctx, operation)
			r.queued.Delete(key)
			if err != nil {
				r.report(fmt.Errorf(
					"providercontrol: automatically resolve parked provision %s/%s: %w",
					operation.TenantID, operation.OperationID, err,
				))
			}
		}
	}
}

func (r *automaticProvisionResolver) resolve(ctx context.Context, operation OperationRef) error {
	_, err := r.service.ResolveProvisionOperation(ctx, ProvisionResolutionWorkflowRequest{
		TenantID: operation.TenantID, OperationID: operation.OperationID,
		OperatorSubjectID: r.subject,
		Confirmation:      "adopt-provision:" + operation.OperationID,
	})
	return err
}

func (r *automaticProvisionResolver) report(err error) {
	if err != nil && r.onError != nil {
		r.onError(err)
	}
}
