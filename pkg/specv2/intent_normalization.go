package specv2

import "strings"

const defaultWizardName = "homelab"

// IntentAdjustment records how marketing-led Wizard intent was translated
// onto the supported creation contract. It is guidance/provenance, not an
// error: the original intent remains available to the caller and run ledger.
type IntentAdjustment struct {
	Field     string `json:"field"`
	Requested string `json:"requested,omitempty"`
	Applied   string `json:"applied,omitempty"`
	Reason    string `json:"reason"`
}

// NormalizeCreationIntent maps ordinary unsupported Wizard answers onto safe
// supported defaults before closed-contract validation and projection. It does
// not repair structural protocol errors such as an unknown schema, run kind,
// or assignment mode.
func NormalizeCreationIntent(intent WizardIntent) (WizardIntent, []IntentAdjustment) {
	normalized := intent
	adjustments := make([]IntentAdjustment, 0)

	if strings.TrimSpace(normalized.Name) == "" {
		normalized.Name = defaultWizardName
		adjustments = append(adjustments, IntentAdjustment{
			Field: "name", Applied: defaultWizardName, Reason: "empty_name_defaulted",
		})
	}

	if normalized.KitAssignment.Mode == KitAssignmentFound || normalized.KitAssignment.Mode == KitAssignmentJoin {
		requested := strings.TrimSpace(normalized.KitAssignment.KitSlug)
		if requested != "" && IsKnownKitSlug(requested) {
			normalized.KitAssignment.KitSlug = requested
		} else if normalized.KitAssignment.Mode == KitAssignmentFound || requested != "" {
			applied := defaultCreationKit(normalized.Server.Transport)
			normalized.KitAssignment.KitSlug = applied
			adjustments = append(adjustments, IntentAdjustment{
				Field: "kit_assignment.kit_slug", Requested: requested, Applied: applied,
				Reason: "unsupported_kit_defaulted",
			})
		}
	}

	roles := make([]string, 0, len(normalized.Server.Roles))
	for _, requested := range normalized.Server.Roles {
		role := NormalizeRole(requested)
		if role == "" {
			adjustments = append(adjustments, IntentAdjustment{
				Field: "server.roles", Requested: requested,
				Reason: "unsupported_role_ignored",
			})
			continue
		}
		roles = appendUniqueIntentValue(roles, role)
		if strings.TrimSpace(requested) != role {
			adjustments = append(adjustments, IntentAdjustment{
				Field: "server.roles", Requested: requested, Applied: role,
				Reason: "role_normalized",
			})
		}
	}
	normalized.Server.Roles = roles

	transport := strings.TrimSpace(normalized.Server.Transport)
	switch transport {
	case "", TransportInstallCommand, TransportConnectRemote, TransportKombifyCloud, TransportHypervisor:
		normalized.Server.Transport = transport
	default:
		normalized.Server.Transport = TransportInstallCommand
		adjustments = append(adjustments, IntentAdjustment{
			Field: "server.transport", Requested: transport, Applied: TransportInstallCommand,
			Reason: "unsupported_transport_defaulted",
		})
	}

	environmentClass := strings.ToLower(strings.TrimSpace(normalized.Server.EnvironmentClass))
	switch environmentClass {
	case "", environmentClassLocal, environmentClassCloud:
		normalized.Server.EnvironmentClass = environmentClass
	default:
		normalized.Server.EnvironmentClass = ""
		adjustments = append(adjustments, IntentAdjustment{
			Field: "server.environment_class", Requested: environmentClass,
			Reason: "unsupported_environment_class_unclassified",
		})
	}

	normalized.Access.Mode = strings.ToLower(strings.TrimSpace(normalized.Access.Mode))
	for index := range normalized.Access.Publications {
		normalized.Access.Publications[index].ServiceID = strings.TrimSpace(normalized.Access.Publications[index].ServiceID)
		normalized.Access.Publications[index].Exposure = strings.ToLower(strings.TrimSpace(normalized.Access.Publications[index].Exposure))
	}
	normalized.Household.Profile = strings.ToLower(strings.TrimSpace(normalized.Household.Profile))
	for index := range normalized.Household.PlannedPeople {
		normalized.Household.PlannedPeople[index].ClientRef = strings.TrimSpace(normalized.Household.PlannedPeople[index].ClientRef)
		normalized.Household.PlannedPeople[index].Name = strings.TrimSpace(normalized.Household.PlannedPeople[index].Name)
		normalized.Household.PlannedPeople[index].Email = strings.TrimSpace(normalized.Household.PlannedPeople[index].Email)
	}

	return normalized, adjustments
}

func defaultCreationKit(transport string) string {
	if strings.TrimSpace(transport) == TransportKombifyCloud {
		return KitSlugCloud
	}
	return KitSlugBasement
}

func appendUniqueIntentValue(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
