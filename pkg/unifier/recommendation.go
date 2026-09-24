package unifier

import (
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

const (
	wizardRecommendationDeploymentSaaS       = "saas"
	wizardRecommendationDeploymentSelfHosted = "self-hosted"
	wizardRecommendationSurfaceEasy          = "easy"
	wizardRecommendationSurfaceTechie        = "techie"
	wizardRecommendationInventoryFresh       = "fresh"
	wizardRecommendationInventoryStale       = "stale"
	wizardRecommendationInventoryUnavailable = "unavailable"
)

// WizardRecommendationInventory is the server-owned environment evidence
// used by the recommendation authority. The route populates it from the
// canonical tenant worker registry after applying the authenticated owner
// scope; callers cannot provide this structure through the public JSON API.
type WizardRecommendationInventory struct {
	Workers     []core.Worker
	State       string
	Source      string
	SnapshotID  string
	ObservedAt  time.Time
	StaleInputs []string
}

// WizardRecommendationEvaluation is the complete server-side input bundle
// for one Wizard recommendation. Tenant and owner are included here so the
// resulting DecisionContext is bound to the authenticated scope.
type WizardRecommendationEvaluation struct {
	Request   core.WizardRecommendationRequest
	TenantID  string
	OwnerID   string
	Inventory WizardRecommendationInventory
	Now       time.Time
}

// StackKitCatalog is the existing StackKit catalog boundary. Engine satisfies
// this interface, so recommendations reuse the configured/pinned StackKits
// catalog and StackKitResolver instead of introducing another CUE authority.
type StackKitCatalog interface {
	ListStackKits() []string
}

// WizardRecommendationAuthority evaluates Wizard answers against the shared
// StackKitResolver and catalog. It produces explainable ranked choices even
// when the environment evidence is stale, while making that state explicit.
type WizardRecommendationAuthority struct {
	resolver      *StackKitResolver
	catalogSource string
}

// NewWizardRecommendationAuthority creates the recommendation authority from
// the existing StackKit catalog. A nil catalog keeps the resolver's embedded
// supported-product defaults for self-hosted development.
func NewWizardRecommendationAuthority(catalog StackKitCatalog) *WizardRecommendationAuthority {
	available := []string(nil)
	source := "embedded-known-stackkits"
	if catalog != nil {
		available = catalog.ListStackKits()
		source = "stackkits-catalog"
	}
	return &WizardRecommendationAuthority{
		resolver:      NewStackKitResolver(available),
		catalogSource: source,
	}
}

// Recommend evaluates one server-scoped Wizard request. The response is
// intentionally usable for incomplete or stale states: its status and input
// arrays tell the caller whether it is safe to act on the ranked result.
func (a *WizardRecommendationAuthority) Recommend(input WizardRecommendationEvaluation) core.WizardRecommendationResponse {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	req := normalizeWizardRecommendationRequest(input.Request)
	response := core.WizardRecommendationResponse{
		Status:          core.WizardRecommendationReady,
		GeneratedAt:     now,
		CatalogSource:   a.catalogSource,
		MissingInputs:   []string{},
		StaleInputs:     append([]string{}, input.Inventory.StaleInputs...),
		Recommendations: []core.WizardRecommendation{},
	}
	if response.CatalogSource == "" {
		response.CatalogSource = "embedded-known-stackkits"
	}

	spec := wizardRecommendationSpec(req)
	decisionContext := buildWizardRecommendationDecisionContext(req, input, spec)
	response.DecisionContext = decisionContext
	response.DecisionContextHash = HashDecisionContext(decisionContext)

	if len(req.Goals) == 0 && len(req.Services) == 0 {
		response.Status = core.WizardRecommendationIncomplete
		response.MissingInputs = []string{"goals", "services"}
		return response
	}

	resolver := a.resolver
	if resolver == nil {
		response.Status = core.WizardRecommendationDegraded
		response.StaleInputs = appendUniqueString(response.StaleInputs, "stackkit_catalog")
		return response
	}
	resolved := resolver.Resolve(spec)
	trace := BuildDecisionTrace(spec, decisionContext, resolved, nil)
	response.Recommendations = wizardRecommendations(req, resolved, trace, response.CatalogSource)

	switch {
	case strings.EqualFold(strings.TrimSpace(input.Inventory.State), wizardRecommendationInventoryUnavailable):
		response.Status = core.WizardRecommendationDegraded
		response.StaleInputs = appendUniqueString(response.StaleInputs, "worker_inventory")
	case wizardRecommendationInventoryIsStale(input.Inventory):
		response.Status = core.WizardRecommendationStale
		response.StaleInputs = appendUniqueString(response.StaleInputs, "worker_inventory")
	case resolved == nil || !resolved.Valid:
		response.Status = core.WizardRecommendationDegraded
	}
	return response
}

func normalizeWizardRecommendationRequest(request core.WizardRecommendationRequest) core.WizardRecommendationRequest {
	request.Goals = normalizeWizardRecommendationValues(request.Goals)
	request.Services = normalizeWizardRecommendationValues(request.Services)
	request.DeploymentLane = strings.ToLower(strings.TrimSpace(request.DeploymentLane))
	if request.DeploymentLane != wizardRecommendationDeploymentSaaS && request.DeploymentLane != wizardRecommendationDeploymentSelfHosted {
		request.DeploymentLane = wizardRecommendationDeploymentSelfHosted
	}
	request.ProviderID = strings.TrimSpace(request.ProviderID)
	if request.DeploymentLane != wizardRecommendationDeploymentSaaS ||
		(request.ProviderID != providerCentron && request.ProviderID != providerIONOS) {
		request.ProviderID = ""
	}
	request.Surface = strings.ToLower(strings.TrimSpace(request.Surface))
	if request.Surface != wizardRecommendationSurfaceTechie {
		request.Surface = wizardRecommendationSurfaceEasy
	}
	return request
}

func normalizeWizardRecommendationValues(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func wizardRecommendationSpec(request core.WizardRecommendationRequest) *core.KombinationSpec {
	provider := request.ProviderID
	if provider == "" {
		if request.DeploymentLane == wizardRecommendationDeploymentSaaS {
			provider = "centron"
		} else {
			provider = "local"
		}
	}
	metadata := map[string]string{
		"created_by":      "wizard",
		"wizard_type":     request.Surface,
		"deployment_lane": request.DeploymentLane,
		"provider_id":     request.ProviderID,
		"use_cases":       strings.Join(request.Goals, ","),
	}
	services := make([]core.ServiceSpec, 0, len(request.Services))
	for _, service := range request.Services {
		services = append(services, core.ServiceSpec{Name: service, Type: service})
	}
	return &core.KombinationSpec{
		Name:     "wizard-recommendation",
		Nodes:    []core.NodeSpec{{Name: "foundation", Type: "main", Provider: provider}},
		Services: services,
		Metadata: metadata,
	}
}

func buildWizardRecommendationDecisionContext(request core.WizardRecommendationRequest, input WizardRecommendationEvaluation, spec *core.KombinationSpec) *core.DecisionContext {
	observedAt := input.Inventory.ObservedAt.UTC()
	context := &core.DecisionContext{
		Channel: "wizard:" + request.Surface,
		Source:  "wizard-recommendation",
		Constraints: []core.DecisionConstraint{
			{Key: "deployment_lane", Value: request.DeploymentLane, Source: "wizard", Required: true},
			{Key: "surface", Value: request.Surface, Source: "wizard", Required: true},
		},
		Environment: &core.EnvironmentInventory{
			SnapshotID: input.Inventory.SnapshotID,
			ObservedAt: observedAt,
			Source:     firstNonEmptyRecommendation(input.Inventory.Source, "worker-registry"),
		},
	}
	if context.Environment.SnapshotID == "" {
		context.Environment.SnapshotID = "worker-registry"
	}
	if request.ProviderID != "" {
		context.Constraints = append(context.Constraints, core.DecisionConstraint{Key: "provider_id", Value: request.ProviderID, Source: "wizard"})
	}
	for _, goal := range request.Goals {
		context.Constraints = append(context.Constraints, core.DecisionConstraint{Key: "goal", Value: goal, Source: "wizard", Required: true})
	}
	for _, service := range request.Services {
		context.Constraints = append(context.Constraints, core.DecisionConstraint{Key: "service", Value: service, Source: "wizard", Required: true})
	}
	if strings.TrimSpace(input.TenantID) != "" {
		context.Constraints = append(context.Constraints, core.DecisionConstraint{Key: "tenant_scope", Value: strings.TrimSpace(input.TenantID), Source: "tenantguard", Required: true})
	}
	if strings.TrimSpace(input.OwnerID) != "" {
		context.Constraints = append(context.Constraints, core.DecisionConstraint{Key: "owner_scope", Value: strings.TrimSpace(input.OwnerID), Source: "authenticated-subject", Required: true})
	}
	context.Environment.NetworkSignals = append(context.Environment.NetworkSignals, core.EnvironmentSignal{
		Key: "inventory_state", Value: firstNonEmptyRecommendation(input.Inventory.State, wizardRecommendationInventoryStale), Source: "worker-registry",
	})
	workers := append([]core.Worker(nil), input.Inventory.Workers...)
	sort.SliceStable(workers, func(i, j int) bool { return workers[i].ID < workers[j].ID })
	for _, worker := range workers {
		status := strings.TrimSpace(worker.Status)
		if status == "" {
			status = "unknown"
		}
		context.Environment.ComputeNodes = append(context.Environment.ComputeNodes, core.EnvironmentComputeNode{
			ID: worker.ID, Name: worker.Name, Role: worker.Type, Provider: worker.Provider,
			Status: status, CPU: worker.Capabilities.CPU, RAMMB: worker.Capabilities.RAM,
			DiskGB: worker.Capabilities.Disk, Arch: worker.Capabilities.Arch, OS: worker.Capabilities.OS,
			Source: "worker-registry", Tags: cloneRecommendationTags(worker.Tags),
		})
	}
	return BuildDecisionContext(spec, context)
}

func wizardRecommendations(request core.WizardRecommendationRequest, resolved *ResolveResult, trace *core.DecisionTrace, catalogSource string) []core.WizardRecommendation {
	if resolved == nil || strings.TrimSpace(resolved.StackKit) == "" {
		return []core.WizardRecommendation{}
	}
	selected := CanonicalStackKitName(resolved.StackKit)
	selectedScore := resolved.Confidence * 100
	if selectedScore < 0 {
		selectedScore = 0
	}
	if selectedScore > 100 {
		selectedScore = 100
	}
	reasons := make([]core.WizardRecommendationReason, 0)
	if trace != nil {
		for _, reason := range trace.Reasons {
			reasons = appendUniqueRecommendationReason(reasons, core.WizardRecommendationReason(reason))
		}
	}
	if len(request.Goals) > 0 {
		reasons = appendUniqueRecommendationReason(reasons, core.WizardRecommendationReason{Code: "goal_alignment", Message: "The recommendation evaluates the selected goals against the StackKit decision rules.", Source: "wizard"})
	}
	if len(request.Services) > 0 {
		reasons = appendUniqueRecommendationReason(reasons, core.WizardRecommendationReason{Code: "service_alignment", Message: "The recommendation evaluates the selected services against the StackKit decision rules.", Source: "wizard"})
	}
	if request.ProviderID != "" {
		reasons = appendUniqueRecommendationReason(reasons, core.WizardRecommendationReason{Code: "provider_alignment", Message: "The selected provider is included in the StackKit evaluation.", Source: "wizard"})
	}
	if request.DeploymentLane == wizardRecommendationDeploymentSaaS && request.ProviderID == "" {
		reasons = appendUniqueRecommendationReason(reasons, core.WizardRecommendationReason{
			Code: "safe_standard", Message: "The supported managed-cloud standard is used until a provider preference is available.", Source: "standard-bundle",
		})
	}
	alternatives := make([]string, 0, len(resolved.AlternativeKits))
	for _, kit := range resolved.AlternativeKits {
		kit = CanonicalStackKitName(kit)
		if kit != "" && kit != selected && !containsString(alternatives, kit) {
			alternatives = append(alternatives, kit)
		}
	}
	recommendations := []core.WizardRecommendation{{
		ID: "stackkit:" + selected, StackKit: selected, Rank: 1, Score: selectedScore, Recommended: true,
		Reasons: reasons, Alternatives: alternatives,
		DeepLink: core.WizardRecommendationDeepLink{Step: "node", Section: "stackkit"},
	}}
	for index, kit := range alternatives {
		score := selectedScore - float64(index+1)*10
		if score < 0 {
			score = 0
		}
		recommendations = append(recommendations, core.WizardRecommendation{
			ID: "stackkit:" + kit, StackKit: kit, Rank: index + 2, Score: score,
			Recommended:  false,
			Reasons:      []core.WizardRecommendationReason{{Code: "resolver_alternative", Message: "The shared StackKit resolver identified this as a compatible alternative.", Source: catalogSource}},
			Alternatives: []string{selected},
			DeepLink:     core.WizardRecommendationDeepLink{Step: "node", Section: "stackkit"},
		})
	}
	return recommendations
}

func wizardRecommendationInventoryIsStale(inventory WizardRecommendationInventory) bool {
	state := strings.ToLower(strings.TrimSpace(inventory.State))
	if state == wizardRecommendationInventoryStale || state == wizardRecommendationInventoryUnavailable {
		return true
	}
	if len(inventory.StaleInputs) > 0 {
		return true
	}
	if state == wizardRecommendationInventoryFresh {
		return false
	}
	return len(inventory.Workers) == 0
}

func appendUniqueRecommendationReason(reasons []core.WizardRecommendationReason, candidate core.WizardRecommendationReason) []core.WizardRecommendationReason {
	for _, reason := range reasons {
		if reason.Code == candidate.Code {
			return reasons
		}
	}
	return append(reasons, candidate)
}

func appendUniqueString(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func firstNonEmptyRecommendation(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cloneRecommendationTags(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
