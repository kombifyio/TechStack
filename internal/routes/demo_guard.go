package routes

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

// authorizeDemoAutomation is the single authentication/configuration boundary
// shared by reset and every ensure variant. All demo automation uses the same
// configured tenant and constant-time-checked reset secret.
func authorizeDemoAutomation(e *httpx.Event, operation string) (string, error) {
	currentSecret := strings.TrimSpace(os.Getenv(envDemoResetSecret))
	nextSecret := strings.TrimSpace(os.Getenv(envDemoResetSecretNext))
	tenantID := demoguard.DemoTenantID()
	if (currentSecret == "" && nextSecret == "") || tenantID == "" {
		return "", httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Demo "+strings.TrimSpace(operation)+" is not configured", nil)
	}
	provided := e.Request.Header.Get(demoResetSecretHeader)
	if !demoAutomationSecretMatches(provided, currentSecret, nextSecret) {
		return "", httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Invalid or missing demo reset secret", nil)
	}
	return tenantID, nil
}

func demoAutomationSecretMatches(provided, current, next string) bool {
	if provided == "" {
		return false
	}
	providedBytes := []byte(provided)
	currentMatch := subtle.ConstantTimeCompare(providedBytes, []byte(current))
	nextMatch := subtle.ConstantTimeCompare(providedBytes, []byte(next))
	return (current != "" && currentMatch == 1) || (next != "" && nextMatch == 1)
}

// demoRestrictedRequest reports whether the authenticated request principal
// belongs to the shared public demo account (by tenant/org or subject id).
// Inert when no demo environment is configured.
func demoRestrictedRequest(e *httpx.Event, ownerID string) bool {
	tenantID := ""
	if e != nil && e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil {
			tenantID = id.OrgID
		}
	}
	if tenantID == "" && e != nil && e.Auth != nil {
		tenantID = e.Auth.GetString("org_id")
	}
	return demoguard.IsDemoSubject(tenantID, ownerID)
}

// demoRestrictedDetails is the structured denial envelope for actions that are
// disabled on the shared public demo account. It mirrors the managed-runtime
// envelope key set (FEATURE-ENTITLEMENT-UX-STANDARD).
func demoRestrictedDetails(capability string) map[string]any {
	return map[string]any{
		"phase":             "demo_account_guard",
		"phase_label":       "Demo account restriction",
		"error_code":        "demo_account_restricted",
		"reason_code":       "demo_account",
		"capability":        capability,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         false,
		"user_guidance": map[string]any{
			"title": "Disabled on the demo account",
			"body":  "This is the shared kombify live demo. Destructive and shell-level actions are disabled here.",
			"next_steps": []string{
				"Create your own kombify account to use this feature.",
			},
		},
	}
}
