// Package executioncustody carries the existing Techstack issuer and provider
// authority across a process seam. It owns no wallet, executor or lifecycle.
package executioncustody

import (
	"regexp"

	"github.com/kombifyio/techstack/internal/advancedissuer"
)

type Request = advancedissuer.Request
type Capability = advancedissuer.Capability
type TrustBinding = advancedissuer.TrustBinding
type Issuer = advancedissuer.Issuer

const (
	EnvIssuerKey             = advancedissuer.EnvIssuerKey
	EnvKeyCustodyDatabaseURL = advancedissuer.EnvKeyCustodyDatabaseURL
	TrustedIssuerID          = advancedissuer.TrustedIssuerID
	TrustBundleSchemaVersion = advancedissuer.TrustBundleSchemaVersion
	DefaultTTL               = advancedissuer.DefaultTTL
)

var (
	ErrTrustBindingMissing = advancedissuer.ErrTrustBindingMissing
	stackIDPattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	ownerRefPattern        = regexp.MustCompile(`^owner/local/[0-9a-f]{32}$`)
	deploymentIDPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	callerKeyIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

func (grant CallerGrant) admits(tenant, deployment, owner string) bool {
	if tenant != grant.TenantID || owner == "" || owner != grant.OwnerID {
		return false
	}
	for _, allowed := range grant.DeploymentIDs {
		if deployment == allowed {
			return true
		}
	}
	return false
}
