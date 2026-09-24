package providercontrol

import (
	"context"
	"testing"
)

type automaticResolutionServiceFake struct {
	requests []ProvisionResolutionWorkflowRequest
}

func (f *automaticResolutionServiceFake) ResolveProvisionOperation(
	_ context.Context, request ProvisionResolutionWorkflowRequest,
) (ProvisionResolutionWorkflowResult, error) {
	f.requests = append(f.requests, request)
	return ProvisionResolutionWorkflowResult{}, nil
}

func TestAutomaticProvisionResolverUsesProviderNeutralDiscoveryAndExactDecision(t *testing.T) {
	service := &automaticResolutionServiceFake{}
	resolver, err := newAutomaticProvisionResolver(service, "system:provider-control-worker", nil)
	if err != nil {
		t.Fatalf("newAutomaticProvisionResolver: %v", err)
	}
	operation := OperationRef{TenantID: "tenant-1", OperationID: "operation-1"}
	if err := resolver.resolve(t.Context(), operation); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(service.requests) != 1 || service.requests[0].TenantID != operation.TenantID ||
		service.requests[0].OperationID != operation.OperationID ||
		service.requests[0].Confirmation != "adopt-provision:operation-1" ||
		service.requests[0].OperatorSubjectID != "system:provider-control-worker" {
		t.Fatalf("resolution = %+v", service.requests)
	}
}
