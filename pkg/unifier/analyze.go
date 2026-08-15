// Package unifier provides the Unifier Engine implementation.
// This file contains the Analyze method and related helper functions
// for Phase 1 of the Two-Phase Unifier: requirements analysis.
package unifier

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/pkg/core"
)

const serviceTypeMonitoring = "monitoring"

// =============================================================================
// Two-Phase Unifier: Analyze (Phase 1)
// =============================================================================

// Analyze examines an IntentSpec and produces RequirementsSpec.
// This is Phase 1 of the Two-Phase Unifier: determine what's needed before deployment.
// Robustheit: Always returns valid RequirementsSpec, never blocks on errors.
func (e *Engine) Analyze(spec *core.KombinationSpec) (*core.RequirementsSpec, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Robustheit: nil-Spec wird mit leerer Config behandelt
	if spec == nil {
		spec = &core.KombinationSpec{
			Name: "default-stack",
		}
	}

	// Build RequirementsSpec
	req := &core.RequirementsSpec{
		IntentName:      spec.Name,
		AppliedDefaults: make(map[string]any),
	}

	// 1. Select StackKit (explicit or auto-detect)
	req.StackKit = e.selectStackKit(spec)
	if spec.Kit == "" {
		req.AppliedDefaults["autoSelectedKit"] = req.StackKit
	}

	// 2. Detect Add-ons based on services and goals
	req.DetectedAddons = e.detectAddons(spec)

	// 3. Calculate worker requirements
	req.RequiredWorkers = e.calculateWorkerRequirements(spec)

	// 4. Detect required credentials
	req.RequiredCredentials = e.detectRequiredCredentials(spec)

	// 5. Define pre-checks
	req.RequiredPreChecks = e.definePreChecks(spec)

	// 6. Generate human-readable description
	req.Description = e.generateDescription(req, spec)

	decisionContext := BuildDecisionContext(spec, nil)
	decisionTrace := BuildDecisionTrace(spec, decisionContext, &ResolveResult{
		StackKit:     req.StackKit,
		Reason:       "requirements analysis",
		AutoSelected: spec.Kit == "",
		Confidence:   0.75,
		Valid:        true,
	}, req.DetectedAddons)
	ApplyDecisionArtifacts(req, spec, decisionContext, decisionTrace)

	return req, nil
}

// selectStackKit chooses the appropriate StackKit based on services and complexity.
func (e *Engine) selectStackKit(spec *core.KombinationSpec) string {
	// Explicit kit takes precedence
	if spec.Kit != "" {
		if IsSupportedProductStackKit(spec.Kit) {
			return CanonicalStackKitName(spec.Kit)
		}
		return StackKitBase
	}

	result := NewStackKitResolver(DefaultKnownStackKits()).Resolve(spec)
	if result == nil || result.StackKit == "" {
		return StackKitBase
	}
	return result.StackKit
}

// detectAddons identifies required add-ons based on spec configuration.
func (e *Engine) detectAddons(spec *core.KombinationSpec) []string {
	addons := []string{}
	seen := make(map[string]bool)

	addAddon := func(name string) {
		if !seen[name] {
			seen[name] = true
			addons = append(addons, name)
		}
	}

	// Check network configuration
	if spec.Network.VPN != "" && spec.Network.VPN != "none" {
		// Use specific VPN type name (e.g., vpn-wireguard)
		addAddon("vpn-" + spec.Network.VPN)
	}

	// Check services for addon requirements
	for _, svc := range spec.Services {
		switch svc.Type {
		case serviceTypeMonitoring, "otel-collector", "otel-gateway", "victoriametrics", "prometheus", "grafana":
			addAddon(serviceTypeMonitoring)
		case "logging", "loki", "elasticsearch":
			addAddon("logging")
		case "backup", "restic", "borg":
			addAddon("backup")
		case "auth", "authentik", "keycloak":
			addAddon("authentication")
		case "cloudflare-tunnel", "cloudflared", "cloudflare":
			addAddon("cloudflare")
		case "reverse-proxy", "traefik", "nginx", "caddy":
			addAddon("reverse-proxy")
		}
	}

	// Check node providers for cloud addons
	for _, node := range spec.Nodes {
		provider := strings.ToLower(strings.TrimSpace(node.Provider))
		switch provider {
		case "hetzner":
			addAddon("hetzner-cloud")
		case providerCentron:
			addAddon("managed-vm-lease")
		case "aws":
			addAddon("aws-integration")
		case "gcp":
			addAddon("gcp-integration")
		}
		if managedMonthlyRuntimeProvider(provider) {
			addAddon("managed-vm-lease")
		}
	}

	return addons
}

// calculateWorkerRequirements estimates resources needed based on services.
func (e *Engine) calculateWorkerRequirements(spec *core.KombinationSpec) core.WorkerRequirements {
	req := core.WorkerRequirements{
		MinCloudServers: 0,
		MinLocalServers: 1, // Always need at least one
		MinRAM:          2048,
		MinCPU:          2,
	}

	// Count existing node types
	for _, node := range spec.Nodes {
		provider := strings.ToLower(strings.TrimSpace(node.Provider))
		switch provider {
		case providerLocal, providerBareMetal, "":
			// Already counted in MinLocalServers
		case providerHetzner, providerAWS, providerGCP, providerAzure, providerDigitalOcean, providerDigitalOceanManaged:
			req.MinCloudServers++
		default:
			if managedMonthlyRuntimeProvider(provider) {
				req.MinCloudServers++
			}
		}
	}
	if req.MinCloudServers > 0 && managedMonthlyRuntimeSpec(spec) {
		req.MinLocalServers = 0
	}

	// Estimate resources per service
	for _, svc := range spec.Services {
		ram, cpu, special := e.estimateServiceResources(serviceIdentifier(svc))
		req.MinRAM += ram
		req.MinCPU += cpu
		if special != "" {
			req.SpecialRequirements = appendUnique(req.SpecialRequirements, special)
		}
	}

	return req
}

func serviceIdentifier(svc core.ServiceSpec) string {
	name := strings.ToLower(strings.TrimSpace(svc.Name))
	name = strings.ReplaceAll(name, "_", "-")
	typ := strings.ToLower(strings.TrimSpace(svc.Type))
	typ = strings.ReplaceAll(typ, "_", "-")

	switch name {
	case "caprover", "coolify", "dokploy", "dokku", "portainer",
		"pocket-id", "pocketid", "tinyauth", "authelia", "vaultwarden",
		"whoami", "base", "dashboard", "homepage":
		return name
	}
	if typ != "" {
		return typ
	}
	return name
}

func managedMonthlyRuntimeSpec(spec *core.KombinationSpec) bool {
	if spec == nil {
		return false
	}
	if spec.Metadata != nil {
		switch strings.ToLower(strings.TrimSpace(spec.Metadata["server_mode"])) {
		case serverruntime.RuntimeLaneMonthly, "managed-cloud":
			return true
		}
		if strings.EqualFold(strings.TrimSpace(spec.Metadata["runtime_lane"]), serverruntime.RuntimeLaneMonthly) {
			return true
		}
	}
	for _, node := range spec.Nodes {
		if managedMonthlyRuntimeProvider(node.Provider) {
			return true
		}
	}
	return false
}

func managedMonthlyRuntimeProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == providerCentron || provider == providerIONOS
}

// estimateServiceResources returns RAM (MB), CPU cores, and special requirements for a service type.
func (e *Engine) estimateServiceResources(serviceType string) (ram int, cpu int, special string) {
	switch strings.ToLower(strings.TrimSpace(serviceType)) {
	case "database", "postgres", "mysql", "mariadb":
		return 1024, 2, ""
	case "pocketbase":
		return 256, 1, ""
	case "reverse-proxy", "traefik", "nginx", "caddy":
		return 256, 1, ""
	case serviceTypeMonitoring, "otel-collector", "otel-gateway", "victoriametrics", "prometheus":
		return 512, 1, ""
	case "grafana":
		return 256, 1, ""
	case "paas", "coolify", "dokploy", "dokku", "caprover", "portainer":
		return 1024, 1, ""
	case "auth", "pocket-id", "pocketid", "pocket_id", "tinyauth", "authelia":
		return 256, 0, ""
	case "whoami", "base", "dashboard", "homepage":
		return 64, 0, ""
	case "jellyfin", "plex":
		return 2048, 2, "Hardware-Transcoding empfohlen (Intel QSV/NVIDIA)"
	case "nextcloud":
		return 1024, 2, ""
	case "homeassistant":
		return 512, 1, ""
	case "vaultwarden":
		return 128, 1, ""
	default:
		return 256, 1, "" // Conservative default
	}
}

// detectRequiredCredentials identifies credentials needed based on providers and services.
func (e *Engine) detectRequiredCredentials(spec *core.KombinationSpec) []core.CredentialRequirement {
	creds := []core.CredentialRequirement{}
	seen := make(map[string]bool)

	addCred := func(c core.CredentialRequirement) {
		if !seen[c.Key] {
			seen[c.Key] = true
			creds = append(creds, c)
		}
	}

	// Check node providers
	for _, node := range spec.Nodes {
		switch node.Provider {
		case "hetzner":
			addCred(core.CredentialRequirement{
				Key:         "hetzner_api_token",
				Label:       "Hetzner API Token",
				Description: "API Token für Hetzner Cloud Zugriff",
				Required:    true,
				Type:        "api_key",
				HelpURL:     "https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/",
			})
		case "aws":
			addCred(core.CredentialRequirement{
				Key:         "aws_access_key",
				Label:       "AWS Access Key",
				Description: "AWS Access Key ID für Cloud Zugriff",
				Required:    true,
				Type:        "api_key",
			})
			addCred(core.CredentialRequirement{
				Key:         "aws_secret_key",
				Label:       "AWS Secret Key",
				Description: "AWS Secret Access Key",
				Required:    true,
				Type:        "secret",
			})
		case "digitalocean":
			addCred(core.CredentialRequirement{
				Key:         "digitalocean_token",
				Label:       "DigitalOcean Token",
				Description: "Personal Access Token für DigitalOcean",
				Required:    true,
				Type:        "api_key",
			})
		}
	}

	// Check services for credential requirements
	for _, svc := range spec.Services {
		switch svc.Type {
		case "cloudflare-tunnel", "cloudflared":
			addCred(core.CredentialRequirement{
				Key:         "cloudflare_api_token",
				Label:       "Cloudflare API Token",
				Description: "API Token für Cloudflare Tunnel",
				Required:    true,
				Type:        "api_key",
				HelpURL:     "https://developers.cloudflare.com/fundamentals/api/get-started/create-token/",
			})
		}
	}

	return creds
}

// definePreChecks creates pre-deployment check definitions based on requirements.
func (e *Engine) definePreChecks(spec *core.KombinationSpec) []core.PreCheckDefinition {
	checks := []core.PreCheckDefinition{}
	seen := make(map[string]bool)

	addCheck := func(c core.PreCheckDefinition) {
		if !seen[c.Type] {
			seen[c.Type] = true
			checks = append(checks, c)
		}
	}

	// Always check Docker
	addCheck(core.PreCheckDefinition{
		Type:        "docker_version",
		Description: "Docker muss installiert und lauffähig sein",
		MinVersion:  "24.0",
		Blocking:    true,
	})

	// Service-specific checks
	for _, svc := range spec.Services {
		switch svc.Type {
		case "jellyfin", "plex":
			addCheck(core.PreCheckDefinition{
				Type:        "hardware_transcoding",
				Description: "Hardware-Transcoding Verfügbarkeit prüfen",
				Blocking:    false,
			})
		}
	}

	// VPN-specific checks
	if spec.Network.VPN == "wireguard" {
		addCheck(core.PreCheckDefinition{
			Type:        "wireguard_kernel",
			Description: "WireGuard Kernel-Modul verfügbar",
			Blocking:    false,
		})
	}

	return checks
}

// generateDescription creates a human-readable summary.
func (e *Engine) generateDescription(req *core.RequirementsSpec, spec *core.KombinationSpec) string {
	cloudCount := req.RequiredWorkers.MinCloudServers
	localCount := req.RequiredWorkers.MinLocalServers

	var nodeDesc string
	if cloudCount > 0 && localCount > 0 {
		nodeDesc = fmt.Sprintf("%d Cloud-Server und %d lokale(s) Gerät(e)", cloudCount, localCount)
	} else if cloudCount > 0 {
		nodeDesc = fmt.Sprintf("%d Cloud-Server", cloudCount)
	} else {
		nodeDesc = fmt.Sprintf("%d lokale(s) Gerät(e)", localCount)
	}

	ramGB := req.RequiredWorkers.MinRAM / 1024
	if ramGB < 1 {
		ramGB = 1
	}

	desc := fmt.Sprintf(
		"Stack '%s' verwendet StackKit '%s'. Du brauchst %s mit mindestens %dGB RAM und %d CPU-Kernen.",
		spec.Name,
		req.StackKit,
		nodeDesc,
		ramGB,
		req.RequiredWorkers.MinCPU,
	)

	if len(req.RequiredWorkers.SpecialRequirements) > 0 {
		desc += " " + req.RequiredWorkers.SpecialRequirements[0]
	}

	return desc
}

// appendUnique adds a string to a slice if not already present.
func appendUnique(slice []string, s string) []string {
	for _, existing := range slice {
		if existing == s {
			return slice
		}
	}
	return append(slice, s)
}
