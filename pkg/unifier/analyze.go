// Package unifier provides the Unifier Engine implementation.
// This file contains the Analyze method and related helper functions
// for Phase 1 of the Two-Phase Unifier: requirements analysis.
package unifier

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

const serviceTypeMonitoring = "monitoring"

// =============================================================================
// Two-Phase Unifier: Analyze (Phase 1)
// =============================================================================

// Analyze examines an IntentSpec and produces RequirementsSpec.
// This is Phase 1 of the Two-Phase Unifier: determine what's needed before deployment.
// Catalog and declared-resource errors remain explicit; no installation facts
// are invented when their released authority cannot be read.
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
	resolution := NewStackKitResolver(append([]string{}, e.ListStackKits()...)).Resolve(spec)
	if !resolution.Valid {
		return nil, fmt.Errorf("StackKit selection could not be admitted: %v", resolution.Warnings)
	}
	req.StackKit = resolution.StackKit
	if spec.Kit == "" {
		req.AppliedDefaults["autoSelectedKit"] = req.StackKit
	}

	// 2. Detect Add-ons based on services and goals
	req.DetectedAddons = e.detectAddons(spec)

	// 3. Calculate worker requirements
	workers, err := e.calculateWorkerRequirements(spec)
	if err != nil {
		return nil, err
	}
	req.RequiredWorkers = workers

	// 4. Detect required credentials
	req.RequiredCredentials = e.detectRequiredCredentials(spec)

	// 5. Define pre-checks
	req.RequiredPreChecks = e.definePreChecks(spec)

	// 6. Generate human-readable description
	req.Description = e.generateDescription(req, spec)

	decisionContext := BuildDecisionContext(spec, nil)
	decisionTrace := BuildDecisionTrace(spec, decisionContext, resolution, req.DetectedAddons)
	ApplyDecisionArtifacts(req, spec, decisionContext, decisionTrace)

	return req, nil
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
func (e *Engine) calculateWorkerRequirements(spec *core.KombinationSpec) (core.WorkerRequirements, error) {
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

	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return req, err
	}
	profile := metadataValue(spec, "compute_tier", "standard")
	var settings map[string]map[string]any
	var capabilities map[string]map[string]string
	if raw := spec.Metadata["use_case_settings"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			return req, fmt.Errorf("unifier: invalid use-case settings: %w", err)
		}
	}
	if raw := spec.Metadata["use_case_capabilities"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
			return req, fmt.Errorf("unifier: invalid use-case capabilities: %w", err)
		}
	}
	goals := selectedUseCaseRefs(spec.Metadata, nil)
	footprints, err := selectedFootprints(catalog, goals, profile, settings, capabilities)
	if err != nil {
		return req, err
	}
	byWorkload := map[string][]stackkitrelease.WorkloadFootprint{}
	for _, footprint := range footprints {
		byWorkload[footprint.Workload] = append(byWorkload[footprint.Workload], footprint)
	}
	reservation := stackkitrelease.UseCaseResources{}
	// Only unrelated discovered services use Techstack's generic estimates.
	for _, svc := range spec.Services {
		serviceProfile := svc.ComputeProfile
		if serviceProfile == "" {
			serviceProfile = profile
		}
		facts, matched, err := catalog.ServiceFootprints(svc.Name, serviceProfile, true)
		if !matched {
			facts, matched, err = catalog.ServiceFootprints(svc.Type, serviceProfile, false)
		}
		if err != nil {
			return req, err
		}
		if matched {
			if len(facts) > 0 {
				// A declared service alternative overrides only the implicit
				// default, not an operator's installing backend selection.
				if _, explicit := settings[facts[0].Workload]["backend"]; !explicit {
					byWorkload[facts[0].Workload] = facts
				}
			}
			continue
		}
		ram, cpu, special := e.estimateServiceResources(serviceIdentifier(svc))
		reservation.RAMGB += float64(ram) / 1024
		reservation.CPUCores += float64(cpu)
		if special != "" {
			req.SpecialRequirements = appendUnique(req.SpecialRequirements, special)
		}
	}
	for _, facts := range byWorkload {
		for _, footprint := range facts {
			if footprint.CPUReservationUnspecified {
				req.SpecialRequirements = appendUnique(req.SpecialRequirements, footprint.Module+": the released profile does not declare a CPU reservation")
			}
			req.MinRAM = max(req.MinRAM, int(math.Ceil(footprint.HostFloor.MinRAMGB*1024)))
			req.MinCPU = max(req.MinCPU, int(math.Ceil(footprint.HostFloor.MinCPUCores)))
			reservation.CPUCores += footprint.Reservation.CPUCores
			reservation.RAMGB += footprint.Reservation.RAMGB
		}
	}
	req.MinRAM = max(req.MinRAM, int(math.Ceil(reservation.RAMGB*1024)))
	req.MinCPU = max(req.MinCPU, int(math.Ceil(reservation.CPUCores)))
	return req, nil
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
	case "plex":
		return 2048, 2, "Hardware-Transcoding empfohlen (Intel QSV/NVIDIA)"
	case "homeassistant":
		return 512, 1, ""
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
