package stacks

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Owner-bootstrap denial identifiers. They ride in the same structured error
// envelope as the managed-runtime denials (pkg/monthlyruntime/denials.go) so
// every create-stack denial surface parses exactly one shape:
// error_code, reason_code, required_features, missing_features, retryable,
// user_guidance{title, body, next_steps}.
const (
	ownerBootstrapDeniedErrorCode = "owner_bootstrap_denied"

	reasonCloudLinkMissing            = "cloud_link_missing"
	reasonCloudLinkEmailUnverified    = "cloud_link_email_unverified"
	reasonOwnerEmailMissing           = "owner_email_missing"
	reasonCloudProfileEmailMissing    = "cloud_profile_email_missing"
	reasonOwnerSourceInvalid          = "owner_source_invalid"
	reasonOwnerBootstrapModeInvalid   = "owner_bootstrap_mode_invalid"
	reasonRecoveryMaterialUnavailable = "recovery_material_unavailable"

	ownerBootstrapPhase      = "owner_bootstrap"
	ownerBootstrapPhaseLabel = "Owner bootstrap"

	// detailsKeyReasonCode is the envelope key shared with the managed-runtime
	// denial contract (pkg/monthlyruntime/denials.go).
	detailsKeyReasonCode = "reason_code"
	detailsKeyRetryable  = "retryable"
)

// ownerBootstrapDenial is a structured, fail-closed refusal produced before
// any stack record is persisted. It carries everything the frontend needs to
// render actionable guidance instead of a bare "creation failed".
type ownerBootstrapDenial struct {
	Status     int
	Message    string
	ReasonCode string
	Retryable  bool
	Guidance   map[string]any
}

// Details builds the canonical structured error envelope for the denial.
func (d *ownerBootstrapDenial) Details() map[string]any {
	return map[string]any{
		"phase":              ownerBootstrapPhase,
		"phase_label":        ownerBootstrapPhaseLabel,
		"error_code":         ownerBootstrapDeniedErrorCode,
		detailsKeyReasonCode: d.ReasonCode,
		"required_features":  []string{},
		"missing_features":   []string{},
		"retryable":          d.Retryable,
		"user_guidance":      d.Guidance,
	}
}

// write emits the denial as the HTTP response for the create/import request.
func (d *ownerBootstrapDenial) write(e *httpx.Event) error {
	code := ksapi.ErrCodeBadRequest
	switch d.Status {
	case http.StatusForbidden:
		code = ksapi.ErrCodeForbidden
	case http.StatusConflict:
		code = ksapi.ErrCodeConflict
	case http.StatusInternalServerError:
		code = ksapi.ErrCodeInternal
	}
	return httpx.Error(e, d.Status, code, d.Message, d.Details())
}

// ownerGuidance builds the user_guidance envelope. Centralizing the map keys
// keeps the denial constructors free of repeated literals and guarantees a
// uniform guidance shape.
func ownerGuidance(title, body string, nextSteps ...string) map[string]any {
	return map[string]any{
		"title":      title,
		"body":       body,
		"next_steps": nextSteps,
	}
}

func denyCloudLinkMissing() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusForbidden,
		Message:    "No linked kombify Cloud profile is available for the cloud-linked owner",
		ReasonCode: reasonCloudLinkMissing,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Link your kombify Cloud profile first",
			"The cloud-linked owner derives its identity from a kombify Cloud profile connected to this account, and no usable link was found.",
			"Open the owner step and connect your kombify Cloud profile.",
			"Alternatively, switch the owner source to a local owner.",
		),
	}
}

func denyCloudLinkEmailUnverified() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusForbidden,
		Message:    "The linked kombify Cloud profile email is not verified",
		ReasonCode: reasonCloudLinkEmailUnverified,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Verify your kombify Cloud email",
			"The owner seed only accepts a verified email address from the linked kombify Cloud profile.",
			"Verify the email address in your kombify Cloud account.",
			"Re-link the profile, then retry the stack creation.",
		),
	}
}

func denyOwnerEmailMissing() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusBadRequest,
		Message:    "Owner email is required for custom owner bootstrap",
		ReasonCode: reasonOwnerEmailMissing,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Add an owner email",
			"A local owner needs an email address; it seeds the Pocket ID owner account of the new stack.",
			"Enter an owner email in the owner step and retry.",
		),
	}
}

func denyCloudProfileEmailMissing() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusBadRequest,
		Message:    "Complete your kombify Cloud profile with a verified email before creating a managed stack",
		ReasonCode: reasonCloudProfileEmailMissing,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Complete your kombify Cloud profile",
			"The automatic owner bootstrap seeds the stack owner from your kombify Cloud profile, which has no email yet.",
			"Add and verify an email address in your kombify Cloud profile.",
			"Retry the stack creation afterwards.",
		),
	}
}

func denyOwnerSourceInvalid(message string) *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusBadRequest,
		Message:    message,
		ReasonCode: reasonOwnerSourceInvalid,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Unsupported owner source",
			message,
			"Choose a supported owner source in the owner step.",
		),
	}
}

func denyOwnerBootstrapModeInvalid() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusBadRequest,
		Message:    "Owner bootstrap mode must be 'auto', 'custom', or 'none'",
		ReasonCode: reasonOwnerBootstrapModeInvalid,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Unsupported owner bootstrap mode",
			"Owner bootstrap mode must be 'auto', 'custom', or 'none'.",
			"Re-run the creation wizard so it submits a supported owner configuration.",
		),
	}
}

func denyRecoveryMaterialUnavailable() *ownerBootstrapDenial {
	return &ownerBootstrapDenial{
		Status:     http.StatusInternalServerError,
		Message:    "Failed to generate recovery material",
		ReasonCode: reasonRecoveryMaterialUnavailable,
		Retryable:  true,
		Guidance: ownerGuidance(
			"Recovery material could not be generated",
			"The server could not generate the recovery passphrase hash for the owner seed.",
			"Retry the stack creation.",
			"If it keeps failing, check the server logs and contact support.",
		),
	}
}
