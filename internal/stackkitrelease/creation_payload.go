package stackkitrelease

// CreationCatalogPayload is the Wizard's view of one release catalog: the
// response GET /api/v1/stackkits/use-cases serves and, rendered from the
// release assets at repin, the per-release Creation preview reads. Both share
// this one projection so the preview shows exactly what that Wizard offers.
//
// deliverable and statuses come from the release's compatibility manifest
// (specv2.DeliverableGoals, specv2.GoalDeliveryStatuses); deliverabilityKnown
// is false when no manifest is configured, and then nothing is claimed.
func CreationCatalogPayload(
	catalog UseCaseCatalog,
	deliverable map[string]string,
	deliverabilityKnown bool,
	statuses map[string]string,
) map[string]any {
	useCases := make([]map[string]any, 0, len(catalog.UseCases))
	for _, useCase := range catalog.UseCases {
		entry := map[string]any{
			"id":          useCase.ID,
			"title":       useCase.Title,
			"description": useCase.Description,
			"components":  useCase.Components,
		}
		if useCase.Components == nil {
			entry["components"] = []UseCaseComponent{}
		}
		if len(useCase.AgentSurface) > 0 {
			entry["agentSurface"] = useCase.AgentSurface
		}
		// StackKits' own per-tier verdict with its own reason. This is what the
		// large card's advanced drawer shows at the technical depth level; the
		// compact card never sees it.
		if len(useCase.ComputeTiers) > 0 {
			entry["compute_tiers"] = useCase.ComputeTiers
		}
		// What an operator decides about this use case, and where to read
		// more. Both come from the catalog and differ per use case.
		if settings := useCase.ConfigurationSettings(); len(settings) > 0 {
			entry["settings"] = settings
		}
		if len(useCase.Capabilities) > 0 {
			entry["capabilities"] = useCase.Capabilities
		}
		if len(useCase.Bundles) > 0 {
			entry["bundles"] = useCase.Bundles
		}
		if useCase.Docs != "" {
			entry["docs"] = useCase.Docs
		}
		// Deliverability comes from the same compatibility manifest that
		// AuthorGoals gates on, so the card cannot promise something the
		// authoring path will drop.
		if deliverabilityKnown {
			_, ok := deliverable[useCase.ID]
			entry["deliverable"] = ok
		}
		// The release's own status, per use case and per application, so the
		// Wizard labels beta and experimental choices instead of hiding them.
		if status := statuses[useCase.ID]; status != "" {
			entry["delivery_status"] = status
		}
		if maturity := useCase.Maturity(); maturity != "" {
			entry["maturity"] = maturity
		}
		if useCase.DefaultAlternative != "" {
			entry["default_alternative"] = useCase.DefaultAlternative
		}
		if len(useCase.Alternatives) > 0 {
			entry["alternatives"] = useCase.Alternatives
		}
		if len(useCase.AddOns) > 0 {
			entry["add_ons"] = useCase.AddOns
		}
		useCases = append(useCases, entry)
	}
	return map[string]any{
		"configured":           true,
		"deliverability_known": deliverabilityKnown,
		"release":              catalog.Release,
		"use_cases":            useCases,
	}
}
