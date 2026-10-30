package jobs

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

func relocationCandidateRelease(ctx context.Context, req WorkloadRelocationRequest) (*stackkitrelease.Release, error) {
	source, err := TargetStackKitReleaseForStack(ctx, req.TenantID, req.Source.StackID)
	if err != nil || source == nil {
		return source, err
	}
	destination, err := TargetStackKitReleaseForStack(ctx, req.TenantID, req.Destination.StackID)
	if err != nil || destination == nil {
		return nil, err
	}
	have, want := source.Receipt(), destination.Receipt()
	if have.Kit != want.Kit || have.Version != want.Version || have.Platform != want.Platform || have.ArchiveSHA256 != want.ArchiveSHA256 || have.IndexSHA256 != want.IndexSHA256 {
		return nil, fmt.Errorf("move participants have different admitted StackKits releases")
	}
	return source, nil
}
