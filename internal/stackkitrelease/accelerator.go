package stackkitrelease

// RequiresAccelerator reports whether the selected use cases cannot run
// without a GPU on the pinned release: a selected capability module declares
// requiresAccelerator, or a chosen setting option declares needsAccelerator.
// An unset setting takes its default and an unset capability its default when
// it is required or on by default, the same selection release authoring makes.
// A goal the release does not declare asks nothing of the placement here; the
// catalog admission that owns goals refuses it.
func (catalog UseCaseCatalog) RequiresAccelerator(goals []string, settings map[string]map[string]any, capabilities map[string]map[string]string) bool {
	for _, goal := range goals {
		if useCase, found := catalog.FindUseCase(goal); found && useCase.requiresAccelerator(settings[goal], capabilities[goal]) {
			return true
		}
	}
	return false
}

func (useCase UseCase) requiresAccelerator(settings map[string]any, capabilities map[string]string) bool {
	for _, capability := range useCase.Capabilities {
		if !capability.RequiresAccelerator {
			continue
		}
		value, explicit := capabilities[capability.ID]
		if !explicit && (capability.Required || capability.EnabledByDefault) {
			value = capability.Default.ID
		}
		if value != "" && value != "off" {
			return true
		}
	}
	for _, setting := range useCase.ConfigurationSettings() {
		if setting.Kind != "choice" {
			continue
		}
		value, set := settings[setting.ID]
		if !set {
			value = setting.Default
		}
		for _, option := range setting.Options {
			if option.NeedsAccelerator && value == option.ID {
				return true
			}
		}
	}
	return false
}
