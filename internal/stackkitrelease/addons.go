package stackkitrelease

import "sort"

// UseCaseAddOnView projects declared optional workloads through the existing
// add-on endpoint. Labels come from the setting that names the workload.
type UseCaseAddOnView struct {
	UseCaseAddOn
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

func (catalog *UseCaseCatalog) AddOnViews() []UseCaseAddOnView {
	result := make([]UseCaseAddOnView, 0)
	seen := make(map[string]bool)
	for _, useCase := range catalog.UseCases {
		for _, addon := range useCase.AddOns {
			if seen[addon.ID] {
				continue
			}
			seen[addon.ID] = true
			entry := UseCaseAddOnView{UseCaseAddOn: addon, Name: addon.ID}
			for _, setting := range useCase.ConfigurationSettings() {
				if setting.WorkloadRef == addon.ID {
					entry.DisplayName, entry.Description = setting.Name, setting.Help
					break
				}
			}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
