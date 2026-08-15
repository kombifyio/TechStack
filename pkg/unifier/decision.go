package unifier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

const (
	decisionAPIVersion = "techstack/decision-context/v1"
	decisionKind       = "DecisionContext"

	decisionChannelManual  = "manual"
	decisionChannelWizard  = "wizard"
	decisionSourceMetadata = "metadata"
	decisionOwnerCloud     = "cloud"
	metadataValueTrue      = "true"
)

// BuildDecisionContext derives the decision-plane input bundle without
// modifying the user-owned spec. Provided fields win; missing fields are
// inferred from the normalized StackSpec metadata and node hints.
func BuildDecisionContext(spec *core.KombinationSpec, provided *core.DecisionContext) *core.DecisionContext {
	ctx := cloneDecisionContext(provided)
	if ctx.APIVersion == "" {
		ctx.APIVersion = decisionAPIVersion
	}
	if ctx.Kind == "" {
		ctx.Kind = decisionKind
	}
	if spec != nil && ctx.IntentRef.Name == "" {
		ctx.IntentRef.Name = spec.Name
	}
	if ctx.Channel == "" {
		ctx.Channel = inferDecisionChannel(spec)
	}
	if ctx.Source == "" {
		ctx.Source = inferDecisionSource(spec)
	}
	if ctx.Environment == nil {
		ctx.Environment = inferEnvironmentInventory(spec)
	}
	if ctx.Operator == nil || ctx.Operator.Score == 0 {
		ctx.Operator = inferOperatorCapability(spec, ctx.Channel)
	} else {
		normalizeOperatorCapability(ctx.Operator)
	}
	return ctx
}

func BuildDecisionTrace(spec *core.KombinationSpec, ctx *core.DecisionContext, resolve *ResolveResult, addons []string) *core.DecisionTrace {
	stackKit := ""
	if resolve != nil {
		stackKit = resolve.StackKit
	}
	if stackKit == "" && spec != nil {
		stackKit = spec.Kit
	}
	if stackKit == "" {
		stackKit = StackKitBase
	}

	trace := &core.DecisionTrace{
		SelectedStackKit:    stackKit,
		Foundation:          metadataValue(spec, "stackkit_foundation", stackKit),
		Addons:              append([]string(nil), addons...),
		DeploymentMode:      resolveDeploymentMode(spec, stackKit),
		ComputeTier:         resolveComputeTier(spec, ctx),
		PAAS:                metadataValue(spec, "paas", "coolify"),
		DecisionContextHash: HashDecisionContext(ctx),
	}
	trace.StackKitProfile = resolveStackKitProfile(spec, ctx, trace)
	trace.BlockingGaps = blockingEnvironmentGaps(evaluateEnvironmentGaps(spec, ctx, stackKit))
	trace.Guidance = buildGuidance(ctx)
	trace.Reasons = buildDecisionReasons(spec, ctx, resolve, trace)
	return trace
}

func ApplyDecisionArtifacts(req *core.RequirementsSpec, spec *core.KombinationSpec, ctx *core.DecisionContext, trace *core.DecisionTrace) {
	if req == nil {
		return
	}
	req.DecisionContext = ctx
	req.DecisionContextHash = HashDecisionContext(ctx)
	req.DecisionTrace = trace
	req.DecisionTraceHash = HashDecisionTrace(trace)
	req.EnvironmentGaps = evaluateEnvironmentGaps(spec, ctx, req.StackKit)
	req.Guidance = buildGuidance(ctx)
}

func ApplyUnifiedDecisionArtifacts(unified *core.UnifiedSpec, ctx *core.DecisionContext, trace *core.DecisionTrace) {
	if unified == nil {
		return
	}
	unified.DecisionContext = ctx
	unified.DecisionContextHash = HashDecisionContext(ctx)
	unified.DecisionTrace = trace
	unified.DecisionTraceHash = HashDecisionTrace(trace)
}

func HashDecisionContext(ctx *core.DecisionContext) string {
	return hashJSON(ctx)
}

func HashDecisionTrace(trace *core.DecisionTrace) string {
	return hashJSON(trace)
}

func hashJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func cloneDecisionContext(provided *core.DecisionContext) *core.DecisionContext {
	if provided == nil {
		return &core.DecisionContext{}
	}
	data, err := json.Marshal(provided)
	if err != nil {
		copy := *provided
		return &copy
	}
	var cloned core.DecisionContext
	if err := json.Unmarshal(data, &cloned); err != nil {
		copy := *provided
		return &copy
	}
	return &cloned
}

func inferDecisionChannel(spec *core.KombinationSpec) string {
	wizardType := metadataValue(spec, "wizard_type", "")
	if wizardType != "" {
		return "wizard:" + wizardType
	}
	createdBy := metadataValue(spec, "created_by", "")
	switch createdBy {
	case "import":
		return "import"
	case decisionChannelManual:
		return decisionChannelManual
	case "cli":
		return "cli"
	case decisionChannelWizard:
		return "wizard:easy"
	default:
		return "unknown"
	}
}

func inferDecisionSource(spec *core.KombinationSpec) string {
	if source := metadataValue(spec, "decision_context_source", ""); source != "" {
		return source
	}
	if createdBy := metadataValue(spec, "created_by", ""); createdBy != "" {
		return createdBy
	}
	return "unifier-inferred"
}

func inferEnvironmentInventory(spec *core.KombinationSpec) *core.EnvironmentInventory {
	now := time.Now().UTC()
	inventory := &core.EnvironmentInventory{
		SnapshotID:  environmentSnapshotID(spec),
		GeneratedAt: now,
		ObservedAt:  now,
		Source:      "unifier-inferred",
	}
	if spec == nil {
		return inventory
	}
	for _, node := range spec.Nodes {
		status := "planned"
		source := "intent-node"
		if metadataValue(spec, "server_provisioning_mode", "") == "install-command" && node.SSH == nil {
			status = "pending-registration"
			source = "server-registry-pending"
		}
		inventory.ComputeNodes = append(inventory.ComputeNodes, core.EnvironmentComputeNode{
			ID:       node.Name,
			Name:     node.Name,
			Role:     node.Type,
			Provider: node.Provider,
			Status:   status,
			Arch:     node.Tags["arch"],
			Source:   source,
			Tags:     cloneStringMap(node.Tags),
		})
	}
	if spec.Network.Domain != "" {
		inventory.AccessEndpoints = append(inventory.AccessEndpoints, core.AccessEndpoint{
			ID:       "domain:" + spec.Network.Domain,
			Kind:     "domain",
			Value:    spec.Network.Domain,
			Source:   "intent-network",
			Verified: metadataBool(spec, "domain_verified") || metadataBool(spec, "dns_verified"),
		})
		if metadataBool(spec, "domain_verified") || metadataBool(spec, "dns_verified") {
			inventory.Assets = append(inventory.Assets, core.EnvironmentAsset{
				ID:     "domain:" + spec.Network.Domain,
				Kind:   "domain",
				Name:   spec.Network.Domain,
				Value:  spec.Network.Domain,
				Source: decisionSourceMetadata,
				Ready:  true,
			})
		}
	}
	if mailAddress := metadataValue(spec, "mail_address", ""); mailAddress != "" {
		verified := metadataBool(spec, "mail_verified")
		inventory.AccessEndpoints = append(inventory.AccessEndpoints, core.AccessEndpoint{
			ID:       "mail:" + mailAddress,
			Kind:     "mail",
			Value:    mailAddress,
			Source:   "stack-spec",
			Verified: verified,
		})
		inventory.Assets = append(inventory.Assets, core.EnvironmentAsset{
			ID:     "mail:" + mailAddress,
			Kind:   "mail-identity",
			Name:   "operator mail address",
			Value:  mailAddress,
			Source: "stack-spec",
			Ready:  verified,
		})
	}
	if runtimeLane := metadataValue(spec, "runtime_lane", ""); runtimeLane != "" {
		inventory.Assets = append(inventory.Assets, core.EnvironmentAsset{
			ID:     "runtime:" + runtimeLane,
			Kind:   "managed-runtime-lease",
			Name:   runtimeLane,
			Value:  metadataValue(spec, "runtime_offering_id", runtimeLane),
			Source: decisionSourceMetadata,
			Ready:  true,
		})
	}
	if identityProvider := metadataValue(spec, "identity_provider", ""); identityProvider != "" {
		inventory.Assets = append(inventory.Assets, core.EnvironmentAsset{
			ID:     "auth:" + identityProvider,
			Kind:   "auth",
			Name:   identityProvider,
			Value:  identityProvider,
			Source: decisionSourceMetadata,
			Ready:  true,
		})
	}
	if ownerSource := metadataValue(spec, "owner_source", ""); ownerSource == decisionOwnerCloud {
		inventory.Assets = append(inventory.Assets, core.EnvironmentAsset{
			ID:     "auth:kombify-cloud",
			Kind:   "identity",
			Name:   "kombify Cloud profile",
			Value:  metadataValue(spec, "identity_provider", "kombify-cloud"),
			Source: decisionSourceMetadata,
			Ready:  true,
		})
	}
	if context := metadataValue(spec, "context", ""); context != "" {
		inventory.NetworkSignals = append(inventory.NetworkSignals, core.EnvironmentSignal{
			Key:        "context",
			Value:      context,
			Confidence: "0.70",
			Source:     "stack-spec",
		})
	}
	return inventory
}

func environmentSnapshotID(spec *core.KombinationSpec) string {
	name := "unknown"
	if spec != nil && spec.Name != "" {
		name = spec.Name
	}
	return "env:" + name
}

func inferOperatorCapability(spec *core.KombinationSpec, channel string) *core.OperatorCapabilityProfile {
	score := 2
	source := "unifier-baseline"
	evidence := []core.OperatorCapabilityEvidence{{Signal: "default-oneclick-baseline", Weight: 2, Source: source}}

	switch {
	case strings.HasPrefix(channel, "wizard:techie"):
		score = 7
		evidence = []core.OperatorCapabilityEvidence{{Signal: "techie-wizard-selected", Weight: 7, Source: decisionChannelWizard}}
	case channel == "cli":
		score = 8
		evidence = []core.OperatorCapabilityEvidence{{Signal: "cli-submission", Weight: 8, Source: "cli"}}
	case channel == decisionChannelManual:
		score = 9
		evidence = []core.OperatorCapabilityEvidence{{Signal: "manual-stack-spec", Weight: 9, Source: decisionChannelManual}}
	case channel == "import":
		score = 8
		evidence = []core.OperatorCapabilityEvidence{{Signal: "imported-stack-spec", Weight: 8, Source: "import"}}
	case strings.HasPrefix(channel, "wizard:easy"):
		score = 2
		evidence = []core.OperatorCapabilityEvidence{{Signal: "easy-wizard-selected", Weight: 2, Source: decisionChannelWizard}}
	}

	if metaScore := parseMetadataInt(spec, "operator_capability_score"); metaScore > 0 {
		score = metaScore
		source = metadataValue(spec, "operator_capability_source", decisionSourceMetadata)
		evidence = append(evidence, core.OperatorCapabilityEvidence{Signal: "operator-capability-metadata", Weight: metaScore, Source: source})
	}
	if advancedCount := parseMetadataInt(spec, "advanced_interaction_count"); advancedCount > 0 && score < 10 {
		score += minInt(advancedCount, 2)
		evidence = append(evidence, core.OperatorCapabilityEvidence{Signal: "advanced-interactions", Weight: advancedCount, Source: decisionChannelWizard})
	}

	profile := &core.OperatorCapabilityProfile{
		Score:      score,
		Confidence: metadataFloat(spec, "operator_capability_confidence", 0.55),
		Source:     source,
		Evidence:   evidence,
		UpdatedAt:  time.Now().UTC(),
	}
	normalizeOperatorCapability(profile)
	return profile
}

func normalizeOperatorCapability(profile *core.OperatorCapabilityProfile) {
	if profile.Score < 1 {
		profile.Score = 1
	}
	if profile.Score > 10 {
		profile.Score = 10
	}
	profile.Band = operatorCapabilityBand(profile.Score)
	if profile.Confidence <= 0 {
		profile.Confidence = 0.55
	}
	if profile.Source == "" {
		profile.Source = "provided"
	}
	if profile.UpdatedAt.IsZero() {
		profile.UpdatedAt = time.Now().UTC()
	}
}

func operatorCapabilityBand(score int) string {
	switch {
	case score <= 2:
		return "oneclick"
	case score <= 4:
		return "guided"
	case score <= 6:
		return "curious-admin"
	case score <= 8:
		return "techie"
	default:
		return "expert"
	}
}

func evaluateEnvironmentGaps(spec *core.KombinationSpec, ctx *core.DecisionContext, stackKit string) []core.EnvironmentGap {
	inventory := (*core.EnvironmentInventory)(nil)
	if ctx != nil {
		inventory = ctx.Environment
	}
	nodeCount := 0
	if inventory != nil {
		nodeCount = eligibleComputeNodeCount(inventory)
	}
	hasManagedRuntime := inventoryHasReadyAsset(inventory, "managed-runtime-lease")
	gaps := make([]core.EnvironmentGap, 0)
	if nodeCount == 0 && !hasManagedRuntime {
		gaps = append(gaps, core.EnvironmentGap{
			Code:     "foundation_node_or_managed_runtime_required",
			Message:  "Foundation Node oder Managed Runtime nötig",
			Severity: "requirement",
			Blocking: false,
			Source:   "environment-inventory",
		})
	}
	if isHAStackKit(stackKit) && nodeCount < 3 {
		gaps = append(gaps, core.EnvironmentGap{
			Code:     "ha_requires_three_nodes",
			Message:  "HA Kit requires at least three eligible compute nodes; operator capability cannot bypass this StackKit constraint.",
			Severity: "blocking",
			Blocking: true,
			Source:   "stackkit-policy",
		})
	}
	if spec != nil && isCustomPublicDomain(spec.Network.Domain) && !inventoryHasReadyDomainAsset(inventory, spec.Network.Domain) {
		gaps = append(gaps, core.EnvironmentGap{
			Code:     "domain_dns_asset_required",
			Message:  "Custom public domains require a verified DNS/domain asset before public TLS can be selected.",
			Severity: "requirement",
			Blocking: false,
			Source:   "environment-inventory",
		})
	}
	return gaps
}

func eligibleComputeNodeCount(inventory *core.EnvironmentInventory) int {
	if inventory == nil {
		return 0
	}
	count := 0
	for _, node := range inventory.ComputeNodes {
		switch node.Status {
		case "pending-registration", "missing", "offline", precheckStatusFailed:
			continue
		default:
			count++
		}
	}
	return count
}

func blockingEnvironmentGaps(gaps []core.EnvironmentGap) []core.EnvironmentGap {
	blocking := make([]core.EnvironmentGap, 0)
	for _, gap := range gaps {
		if gap.Blocking {
			blocking = append(blocking, gap)
		}
	}
	return blocking
}

func buildGuidance(ctx *core.DecisionContext) []core.DecisionGuidance {
	if ctx == nil || ctx.Operator == nil {
		return nil
	}
	switch ctx.Operator.Band {
	case "oneclick":
		return []core.DecisionGuidance{{
			Code:    "prefer_defaults",
			Level:   "low-jargon",
			Message: "Prefer safe defaults, short explanations, and a single primary action.",
			Surface: "easy",
		}}
	case "guided":
		return []core.DecisionGuidance{{
			Code:    "guided_choices",
			Level:   "guided",
			Message: "Show only decisions that materially change rollout safety or capability.",
			Surface: "easy",
		}}
	case "curious-admin":
		return []core.DecisionGuidance{{
			Code:    "explain_alternatives",
			Level:   "moderate",
			Message: "Show recommended defaults with concise alternative explanations.",
			Surface: "advanced-toggle",
		}}
	case "techie":
		return []core.DecisionGuidance{{
			Code:    "show_explicit_controls",
			Level:   "technical",
			Message: "Expose StackKit, service, placement, and network controls within validated contracts.",
			Surface: "techie",
		}}
	default:
		return []core.DecisionGuidance{{
			Code:    "allow_validated_overrides",
			Level:   "expert",
			Message: "Allow full validated overrides and preserve decision trace details.",
			Surface: "cli-techie",
		}}
	}
}

func buildDecisionReasons(spec *core.KombinationSpec, ctx *core.DecisionContext, resolve *ResolveResult, trace *core.DecisionTrace) []core.DecisionReason {
	reasons := make([]core.DecisionReason, 0)
	if resolve != nil && resolve.Reason != "" {
		reasons = append(reasons, core.DecisionReason{Code: "stackkit_resolution", Message: resolve.Reason, Source: "resolver"})
	}
	if ctx != nil && ctx.Operator != nil {
		reasons = append(reasons, core.DecisionReason{
			Code:    "operator_capability_band",
			Message: fmt.Sprintf("Operator capability band %s from score %d", ctx.Operator.Band, ctx.Operator.Score),
			Source:  ctx.Operator.Source,
		})
	}
	if trace != nil && trace.ComputeTier == resolverMemoryLow {
		reasons = append(reasons, core.DecisionReason{Code: "low_compute_profile", Message: "Low compute or Pi context selected constrained defaults.", Source: "environment-inventory"})
	}
	if spec != nil && metadataValue(spec, "owner_source", "") == decisionOwnerCloud {
		reasons = append(reasons, core.DecisionReason{Code: "cloud_owner_asset", Message: "kombify Cloud identity asset is available for automatic owner bootstrap.", Source: "environment-inventory"})
	}
	return reasons
}

func resolveStackKitProfile(spec *core.KombinationSpec, ctx *core.DecisionContext, trace *core.DecisionTrace) string {
	if trace != nil {
		switch {
		case isHAStackKit(trace.SelectedStackKit):
			return "ha"
		case trace.ComputeTier == resolverMemoryLow:
			return "constrained-pi"
		case trace.Foundation != "" && strings.Contains(trace.Foundation, "managed"):
			return "managed-cloud"
		}
	}
	if metadataValue(spec, "runtime_lane", "") == "monthly-runtime" {
		return "managed-cloud"
	}
	if ctx != nil && ctx.Operator != nil {
		switch ctx.Operator.Band {
		case "oneclick", "guided":
			return "oneclick-default"
		case "techie":
			return "techie-standard"
		case "expert":
			return "expert-explicit"
		}
	}
	return "guided-default"
}

func resolveDeploymentMode(spec *core.KombinationSpec, stackKit string) string {
	if mode := metadataValue(spec, "stackkit_mode", ""); mode != "" {
		return mode
	}
	if isHAStackKit(stackKit) {
		return "advanced"
	}
	return iacModeSimple
}

func resolveComputeTier(spec *core.KombinationSpec, ctx *core.DecisionContext) string {
	if tier := metadataValue(spec, "compute_tier", ""); tier != "" {
		return tier
	}
	if isPiOrLowResource(spec, ctx) {
		return resolverMemoryLow
	}
	return iacDefaultComputeTier
}

func isPiOrLowResource(spec *core.KombinationSpec, ctx *core.DecisionContext) bool {
	if metadataValue(spec, "context", "") == "pi" {
		return true
	}
	if environmentHasPiOrLowResource(ctx) {
		return true
	}
	if spec != nil {
		for _, node := range spec.Nodes {
			if hasResolverARMIndicator(node.Tags) || hasResolverLowMemoryIndicator(node.Tags) {
				return true
			}
		}
	}
	return false
}

func environmentHasPiOrLowResource(ctx *core.DecisionContext) bool {
	if ctx == nil || ctx.Environment == nil {
		return false
	}
	for _, signal := range ctx.Environment.NetworkSignals {
		if signal.Key == "context" && signal.Value == "pi" {
			return true
		}
	}
	for _, node := range ctx.Environment.ComputeNodes {
		if environmentComputeNodeIsPiOrLowResource(node) {
			return true
		}
	}
	return false
}

func environmentComputeNodeIsPiOrLowResource(node core.EnvironmentComputeNode) bool {
	if node.Arch == resolverArchARM64 || node.Tags["type"] == resolverTypeRPI || node.Tags["device"] == resolverDeviceRPI {
		return true
	}
	return node.RAMMB > 0 && node.RAMMB < 4096
}

func isHAStackKit(stackKit string) bool {
	switch stackKit {
	case StackKitHA, "ha", "high-availability":
		return true
	default:
		return false
	}
}

func isCustomPublicDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || domain == providerLocal || domain == "localhost" || strings.HasSuffix(domain, ".localhost") {
		return false
	}
	if domain == "kombify.me" || domain == "stack.home" || strings.HasSuffix(domain, ".home") || strings.HasSuffix(domain, ".lan") || strings.HasSuffix(domain, ".local") || strings.HasSuffix(domain, ".internal") {
		return false
	}
	return strings.Contains(domain, ".")
}

func inventoryHasReadyAsset(inventory *core.EnvironmentInventory, kind string) bool {
	if inventory == nil {
		return false
	}
	for _, asset := range inventory.Assets {
		if asset.Kind == kind && asset.Ready {
			return true
		}
	}
	return false
}

func inventoryHasReadyDomainAsset(inventory *core.EnvironmentInventory, domain string) bool {
	if inventory == nil {
		return false
	}
	for _, asset := range inventory.Assets {
		if (asset.Kind == "domain" || asset.Kind == "dns") && asset.Ready && (asset.Value == "" || strings.EqualFold(asset.Value, domain)) {
			return true
		}
	}
	for _, endpoint := range inventory.AccessEndpoints {
		if endpoint.Kind == "domain" && endpoint.Verified && strings.EqualFold(endpoint.Value, domain) {
			return true
		}
	}
	return false
}

func metadataValue(spec *core.KombinationSpec, key, fallback string) string {
	if spec == nil || spec.Metadata == nil {
		return fallback
	}
	value := strings.TrimSpace(spec.Metadata[key])
	if value == "" {
		return fallback
	}
	return value
}

func metadataBool(spec *core.KombinationSpec, key string) bool {
	switch strings.ToLower(metadataValue(spec, key, "")) {
	case "1", metadataValueTrue, "yes", "verified", "ready":
		return true
	default:
		return false
	}
}

func metadataFloat(spec *core.KombinationSpec, key string, fallback float64) float64 {
	value := metadataValue(spec, key, "")
	if value == "" {
		return fallback
	}
	var parsed float64
	if _, err := fmt.Sscanf(value, "%f", &parsed); err != nil {
		return fallback
	}
	return parsed
}

func parseMetadataInt(spec *core.KombinationSpec, key string) int {
	value := metadataValue(spec, key, "")
	if value == "" {
		return 0
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return 0
	}
	return parsed
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for k, v := range values {
		cloned[k] = v
	}
	return cloned
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
