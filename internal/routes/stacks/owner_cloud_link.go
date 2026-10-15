package stacks

import (
	"context"
	"github.com/kombifyio/techstack/internal/routes/auth"
	"strings"
)

func runtimeCloudLinkForOwner(ctx context.Context, runtime auth.RuntimeStore, tenantID, ownerID string) *cloudLinkIdentity {
	if runtime.Validate() != nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerID) == "" {
		return nil
	}
	profile, err := runtime.Persistence.GetCloudProfile(ctx, tenantID, runtime.InstanceID, ownerID)
	if err != nil || profile == nil || profile.Link == nil || !profile.Link.EmailVerified {
		return nil
	}
	link := profile.Link
	return &cloudLinkIdentity{ExternalID: link.SubjectID, Email: link.Email, EmailVerified: true, DisplayName: link.DisplayName}
}

func runtimeVerifiedCloudProfile(ctx context.Context, runtime auth.RuntimeStore, tenantID, ownerID string) *cloudLinkIdentity {
	if runtime.Validate() != nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerID) == "" {
		return nil
	}
	profile, err := runtime.Persistence.GetCloudProfile(ctx, tenantID, runtime.InstanceID, ownerID)
	if err != nil || profile == nil || profile.ProviderKey != "cloud" || !profile.EmailVerified || strings.TrimSpace(profile.Email) == "" {
		return nil
	}
	return &cloudLinkIdentity{ExternalID: ownerID, Email: profile.Email, EmailVerified: true, DisplayName: profile.DisplayName}
}

// cloudLinkIdentity is the server-side verified identity of a kombify Cloud
// profile linked to a local operator in the canonical instance projection.
// It is the only trusted identity source for the cloud-linked owner bootstrap.
type cloudLinkIdentity struct {
	ExternalID    string
	Email         string
	EmailVerified bool
	DisplayName   string
}

// requestsCloudLinkedOwner reports whether the normalized create request asks
// for the cloud-linked owner source, so the caller loads its verified profile
// only when needed.
func requestsCloudLinkedOwner(req normalizedCreateStackRequest) bool {
	bootstrap, ok := ownerBootstrapFromRequest(req)
	return ok && bootstrap.Source == ownerSourceCloudLinked
}

func requestsAutomaticCloudOwner(req normalizedCreateStackRequest) bool {
	bootstrap, ok := ownerBootstrapFromRequest(req)
	return ok && bootstrap.BootstrapMode == ownerBootstrapModeAuto && bootstrap.Source == ownerSourceCloud
}
