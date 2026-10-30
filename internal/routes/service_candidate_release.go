package routes

import (
	"context"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/jobs"
)

func (h *serviceRuntimeHandlers) adoptionReleaseForStack(ctx context.Context, tenant, stack string) (*agentpb.StackKitReleasePin, error) {
	if jobs.StageCandidateReleaseConfigured() {
		return jobs.ApplicationAdoptionReleaseForStack(ctx, tenant, stack, false)
	}
	return h.applicationAdmission()
}
