//go:build !guardqualification

package guardrelease

// pinnedIdentity is the signer a Verifier requires when its caller names none:
// the release workflow, always. Only a binary built with the guardqualification
// tag (identity_qualification.go) can pin anything else.
func pinnedIdentity() (Identity, error) { return DefaultIdentity, nil }
