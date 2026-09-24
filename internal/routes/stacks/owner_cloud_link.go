package stacks

import (
	"strings"

	"github.com/kombifyio/techstack/internal/authprojection"
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
		map[string]any{"userID": authprojection.PocketBaseUserID(ownerID)},
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

// verifiedCurrentCloudProfile resolves the compatibility profile created by
// the authenticated Cloud SSO exchange. The exchange marks the local profile
// verified only after accepting the signed Cloud assertion, so browser JSON
// can neither select an email nor upgrade an unverified record.
func verifiedCurrentCloudProfile(app core.App, ownerID string) *cloudLinkIdentity {
	if app == nil || strings.TrimSpace(ownerID) == "" {
		return nil
	}
	user, err := authprojection.FindPocketBaseUser(app, ownerID)
	if err != nil || user == nil || !user.GetBool("verified") {
		return nil
	}
	email := strings.TrimSpace(user.Email())
	if email == "" {
		return nil
	}
	profile := &cloudLinkIdentity{
		ExternalID:    strings.TrimSpace(ownerID),
		Email:         email,
		EmailVerified: true,
		DisplayName:   strings.TrimSpace(user.GetString("name")),
	}
	if link := cloudLinkForOwner(app, ownerID); link != nil {
		profile.ExternalID = firstNonEmpty(strings.TrimSpace(link.ExternalID), profile.ExternalID)
		profile.Email = firstNonEmpty(strings.TrimSpace(link.Email), profile.Email)
		profile.DisplayName = firstNonEmpty(strings.TrimSpace(link.DisplayName), profile.DisplayName)
	}
	return profile
}

// requestsCloudLinkedOwner reports whether the normalized create request asks
// for the cloud-linked owner source, so the caller only pays the user_links
// lookup when it is actually needed.
func requestsCloudLinkedOwner(req normalizedCreateStackRequest) bool {
	bootstrap, ok := ownerBootstrapFromRequest(req)
	return ok && bootstrap.Source == ownerSourceCloudLinked
}

func requestsAutomaticCloudOwner(req normalizedCreateStackRequest) bool {
	bootstrap, ok := ownerBootstrapFromRequest(req)
	return ok && bootstrap.BootstrapMode == ownerBootstrapModeAuto && bootstrap.Source == ownerSourceCloud
}
