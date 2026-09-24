package providercontrol

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/outcome"
)

func providerProvisioningOutcome(providerID string, supportContext map[string]any) *outcome.Decision {
	return &outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: "provider_provisioning",
		Capability: "techstack.managed.runtime.provision", ProviderID: strings.TrimSpace(providerID), Retryable: false,
		UserGuidance: &outcome.Guidance{
			Title: "The provider is creating your server",
			Body:  "The request is safely recorded and provider provisioning is still in progress. Starting another request could create a duplicate resource.",
			NextSteps: []outcome.Step{{
				ID: "wait-provider", Label: "Keep this operation open; progress updates automatically", Kind: "note",
			}},
		},
		SupportContext: supportContext,
	}
}

func providerEnrollmentOutcome(providerID string, supportContext map[string]any) *outcome.Decision {
	return &outcome.Decision{
		Status: outcome.StatusPending, ReasonCode: "awaiting_guard_heartbeat",
		Capability: "techstack.managed.runtime.bootstrap", ProviderID: strings.TrimSpace(providerID), Retryable: false,
		UserGuidance: &outcome.Guidance{
			Title: "The server exists and bootstrap is being verified",
			Body:  "Provider creation succeeded. Techstack is waiting for the server's authenticated Guard heartbeat before it marks the runtime ready.",
			NextSteps: []outcome.Step{{
				ID: "wait-bootstrap", Label: "Keep the server online while bootstrap finishes", Kind: "note",
			}},
		},
		SupportContext: supportContext,
	}
}

func providerFailedOutcome(providerID string, supportContext map[string]any) *outcome.Decision {
	return &outcome.Decision{
		Status: outcome.StatusFailed, ErrorCode: "PROVIDER_PROVISION_FAILED", ReasonCode: "provider_failed",
		Capability: "techstack.managed.runtime.provision", ProviderID: strings.TrimSpace(providerID), Retryable: false,
		UserGuidance: &outcome.Guidance{
			Title: "The provider could not create the server",
			Body:  "Techstack stopped this operation after the provider returned a definitive failure. A blind retry is disabled because the existing provider receipt must be reviewed first.",
			NextSteps: []outcome.Step{{
				ID: "review-provider-receipt", Label: "Open support with the operation context below", Kind: "handoff",
			}},
		},
		SupportContext: supportContext,
	}
}
