package stacks

import (
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// cloudLinkIdentity is the server-side verified identity of a kombify Cloud
// profile linked to a local operator account (user_links, provider "cloud").
// It is the only trusted identity source for the cloud-linked owner bootstrap.
type cloudLinkIdentity struct {
	ExternalID    string
	Email         string
	EmailVerified bool
	DisplayName   string
}

// cloudLinkForOwner loads the operator's kombify Cloud link. It returns nil
// when no link exists or the record is unusable; callers translate that into
// the structured cloud_link_missing denial.
func cloudLinkForOwner(app core.App, ownerID string) *cloudLinkIdentity {
	if app == nil || strings.TrimSpace(ownerID) == "" {
		return nil
	}
	record, err := app.FindFirstRecordByFilter(
		"user_links",
		"user = {:userID} && provider = 'cloud'",
		map[string]any{"userID": strings.TrimSpace(ownerID)},
	)
	if err != nil || record == nil {
		return nil
	}
	return &cloudLinkIdentity{
		ExternalID:    strings.TrimSpace(record.GetString("external_id")),
		Email:         strings.TrimSpace(record.GetString("external_email")),
		EmailVerified: record.GetBool("email_verified"),
		DisplayName:   strings.TrimSpace(record.GetString("external_name")),
	}
}

// requestsCloudLinkedOwner reports whether the normalized create request asks
// for the cloud-linked owner source, so the caller only pays the user_links
// lookup when it is actually needed.
func requestsCloudLinkedOwner(req normalizedCreateStackRequest) bool {
	bootstrap, ok := ownerBootstrapFromRequest(req)
	return ok && bootstrap.Source == ownerSourceCloudLinked
}
