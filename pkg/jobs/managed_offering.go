package jobs

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// projectManagedOffering preserves the customer's size selection before the
// legacy metadata normalizer can supply its default. Multiple supported input
// shapes may agree, but must never silently select different billable packages.
func projectManagedOffering(config map[string]any, metadata map[string]string) error {
	selected := ""
	for _, input := range []any{config, config["metadata"], config["options"]} {
		section := mapFromInterface(input)
		if values, ok := input.(map[string]string); ok {
			section = make(map[string]any, len(values))
			for key, value := range values {
				section[key] = value
			}
		}
		raw, exists := section[metadataKeyRuntimeOfferingID]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("managed runtime offering must be a string")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, err := monthlyruntime.ResolveOffering(value); err != nil {
			return err
		}
		if selected != "" && selected != value {
			return fmt.Errorf("managed runtime offering selections conflict")
		}
		selected = value
	}
	if selected != "" {
		metadata[metadataKeyRuntimeOfferingID] = selected
	}
	return nil
}
