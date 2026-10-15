package guardidentity

import "fmt"

// Structured denial codes returned by the control plane for worker requests.
// They are stable wire identifiers; guidance text is localized by clients.
const (
	CodeProofInvalid    = "guard_proof_invalid"
	CodeProofReplayed   = "guard_proof_replayed"
	CodeIdentityRevoked = "guard_identity_revoked"
	CodeTokenExpired    = "guard_token_expired"
	// CodeIdentityAlreadyBound denies a key bind or redemption that would
	// replace a different, already confirmed Guard key.
	CodeIdentityAlreadyBound = "guard_identity_already_bound"
	// CodeIdentityUnavailable reports a control plane that cannot enforce host
	// identity (for example a missing signing secret).
	CodeIdentityUnavailable = "guard_identity_unavailable"
	// CodeIdentityUpgradeRequired denies work that needs a key-bound identity
	// (command execution) to a worker that still authenticates with its bearer
	// credential alone.
	CodeIdentityUpgradeRequired = "guard_identity_upgrade_required"
	// CodeIdentityUpgradePending answers a key bind that waits for the owner.
	CodeIdentityUpgradePending = "guard_identity_upgrade_pending"
)

// Reason values refine CodeProofInvalid. They name the failed invariant and
// never echo a received value.
const (
	ReasonProofRequired     = "proof_required"
	ReasonProofMalformed    = "proof_malformed"
	ReasonProofSignature    = "proof_signature"
	ReasonProofMethod       = "proof_method"
	ReasonProofURI          = "proof_uri"
	ReasonProofClock        = "proof_clock_skew"
	ReasonProofTokenBinding = "proof_token_binding"
	ReasonKeyMismatch       = "key_mismatch"
	ReasonIdentityUnbound   = "identity_not_bound"
	ReasonTokenInvalid      = "access_token_invalid"
	// ReasonOtherKeyPending refines CodeIdentityUpgradePending: a different key
	// than the caller's awaits the owner.
	ReasonOtherKeyPending = "other_key_pending"
)

// Error is a typed denial. Code is one of the Code* constants and Reason the
// invariant that failed. It is safe to surface to the caller.
type Error struct {
	Code   string
	Reason string
}

func (e *Error) Error() string {
	if e.Reason == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Reason)
}

// Denied builds a typed denial.
func Denied(code, reason string) *Error { return &Error{Code: code, Reason: reason} }
