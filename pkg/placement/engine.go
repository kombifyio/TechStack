// Package placement provides the Kubernetes-inspired service-to-worker matching algorithm.
//
// Architecture: Pillar 1 — Unifier Engine, Phase 5 (UnifiedSpec generation)
//
// The placement engine uses a two-phase approach:
//   - Phase 1 (Filtering): Remove incompatible workers (CPU, RAM, GPU, arch, status)
//   - Phase 2 (Scoring): Rank viable candidates (resource balance, affinity, locality)
//
// Design: docs/concepts/Worker-Service-Matching-Algorithm.md
// ARCHITECTURE: See docs/architecture/ARCHITECTURE_V2.md Section 3.3
package placement

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/core"
)

// Service type constants for consistency.
const (
	ServiceTypeDatabase = "database"
	ServiceTypePostgres = "postgres"
	ServiceTypeMySQL    = "mysql"
	ServiceTypeMariaDB  = "mariadb"
)

// PlacementEngine handles service-to-worker matching using a two-phase algorithm:
// 1. Filtering: Remove workers that don't meet hard requirements
// 2. Scoring: Rank remaining workers by quality of fit
type PlacementEngine struct {
	// Scoring weights (customizable per deployment)
	weights ScoreWeights

	// containerRuntimeProvisioned records that the rollout installs the
	// container runtime, so its current absence must not filter workers out.
	containerRuntimeProvisioned bool
}

// ScoreWeights defines the importance of each scoring factor (0-100).
type ScoreWeights struct {
	ResourceBalance    int // Prefer workers at 40-70% utilization
	HardwareAffinity   int // GPU for AI, NVMe for DBs
	NodeTypePreference int // main vs worker vs storage
	DependencyLocality int // Co-locate dependent services
	LoadDistribution   int // Spread services evenly
	ProviderAffinity   int // Prefer same provider for related services
	UserPreference     int // Explicit user-specified affinities
}

// DefaultWeights returns sensible defaults for scoring.
func DefaultWeights() ScoreWeights {
	return ScoreWeights{
		ResourceBalance:    10, // Reduced to not penalize empty workers too much
		HardwareAffinity:   30,
		NodeTypePreference: 10,
		DependencyLocality: 15,
		LoadDistribution:   25, // Increased to prioritize load balancing
		ProviderAffinity:   5,
		UserPreference:     5,
	}
}

// NewPlacementEngine creates a PlacementEngine with default weights.
func NewPlacementEngine() *PlacementEngine {
	return &PlacementEngine{
		weights: DefaultWeights(),
	}
}

// WithProvisionedContainerRuntime declares that the rollout being planned
// installs the container runtime itself.
//
// A StackKit rollout provisions Docker as part of the kit, so on a freshly
// created managed VM the runtime is an OUTCOME of the rollout, not a
// precondition for planning it. Treating its absence as a hard filter made
// every service unplaceable on every newly provisioned server: the rollout that
// would install Docker could never be planned because Docker was not installed
// yet. Resource and architecture predicates still apply.
func (p *PlacementEngine) WithProvisionedContainerRuntime() *PlacementEngine {
	p.containerRuntimeProvisioned = true
	return p
}

// PlaceServices matches all services to workers based on requirements and capabilities.
// Returns placements and overall quality score (0-100).
func (p *PlacementEngine) PlaceServices(
	requirements *core.RequirementsSpec,
	workers []core.Worker,
	services []core.ServiceSpec,
) ([]core.ServicePlacement, int, error) {
	if len(workers) == 0 {
		return nil, 0, fmt.Errorf("no workers available for placement")
	}

	// Track resource allocation across all placements
	allocation := make(map[string]*core.WorkerUsage)
	for _, w := range workers {
		allocation[w.ID] = &core.WorkerUsage{
			// Converted once here; every comparison below is in millicores.
			CPU:          coresToMillicores(w.Usage.CPU),
			RAM:          w.Usage.RAM,
			Disk:         w.Usage.Disk,
			ServiceCount: w.Usage.ServiceCount,
		}
	}

	placements := make([]core.ServicePlacement, 0, len(services))
	var totalScore int

	// Process services in dependency order (topological sort)
	orderedServices := p.sortByDependencies(services)

	for _, svc := range orderedServices {
		// Phase 1: Filter workers that meet hard requirements
		candidates := p.filterWorkers(svc, workers, allocation)
		if len(candidates) == 0 {
			need := estimateServiceRequirements(svc)
			return nil, 0, fmt.Errorf(
				"no suitable worker found for service %s: needs %dm CPU / %dMB RAM / %dGB disk on top of already-placed reservations; no worker has that much free",
				svc.Name, need.CPU, need.RAM, need.Disk,
			)
		}

		// Phase 2: Score and rank candidates
		scored := p.scoreWorkers(svc, candidates, allocation, placements)
		if len(scored) == 0 {
			return nil, 0, fmt.Errorf("scoring failed for service %s", svc.Name)
		}

		// Select best worker
		best := scored[0]
		placement := core.ServicePlacement{
			Service:    svc,
			WorkerID:   best.worker.ID,
			WorkerName: best.worker.Name,
			Score:      best.score,
			Warnings:   best.warnings,
		}

		// Update allocation
		p.allocateResources(svc, best.worker.ID, allocation)

		placements = append(placements, placement)
		totalScore += best.score
	}

	// Calculate overall quality (average score)
	avgScore := 0
	if len(placements) > 0 {
		avgScore = totalScore / len(placements)
	}

	return placements, avgScore, nil
}

// =============================================================================
// Phase 1: Filtering
// =============================================================================

type workerWithScore struct {
	worker   core.Worker
	score    int
	warnings []string
}

// filterWorkers removes workers that don't meet hard requirements.
func (p *PlacementEngine) filterWorkers(
	svc core.ServiceSpec,
	workers []core.Worker,
	allocation map[string]*core.WorkerUsage,
) []core.Worker {
	svcReq := estimateServiceRequirements(svc)
	candidates := make([]core.Worker, 0, len(workers))

	for _, w := range workers {
		// Predicate 1: Worker must be online
		if w.Status != "online" {
			continue
		}

		// Predicate 2: Sufficient CPU available
		allocated := allocation[w.ID]
		availCPU := coresToMillicores(w.Capabilities.CPU) - allocated.CPU
		if availCPU < svcReq.CPU {
			continue
		}

		// Predicate 3: Sufficient RAM available
		availRAM := w.Capabilities.RAM - allocated.RAM
		if availRAM < svcReq.RAM {
			continue
		}

		// Predicate 4: Sufficient disk available
		availDisk := w.Capabilities.Disk - allocated.Disk
		if availDisk < svcReq.Disk {
			continue
		}

		// Predicate 5: GPU requirement (if needed)
		if svcReq.RequiresGPU && w.Capabilities.GPU == nil {
			continue
		}

		// Predicate 6: Architecture match (default: amd64)
		if svcReq.Arch != "" && w.Capabilities.Arch != svcReq.Arch {
			continue
		}

		// Predicate 7: Docker runtime (if needed).
		// Skipped when the rollout provisions the runtime itself -- see
		// WithProvisionedContainerRuntime.
		if svcReq.RequiresDocker && !p.containerRuntimeProvisioned && w.Capabilities.DockerVersion == "" {
			continue
		}

		// All predicates passed
		candidates = append(candidates, w)
	}

	return candidates
}

// =============================================================================
// Phase 2: Scoring
// =============================================================================

// scoreWorkers ranks workers by quality of fit (higher score = better).
func (p *PlacementEngine) scoreWorkers(
	svc core.ServiceSpec,
	candidates []core.Worker,
	allocation map[string]*core.WorkerUsage,
	previousPlacements []core.ServicePlacement,
) []workerWithScore {
	scored := make([]workerWithScore, 0, len(candidates))
	svcReq := estimateServiceRequirements(svc)

	for _, w := range candidates {
		allocated := allocation[w.ID]
		score := 0
		var warnings []string

		// Factor 1: Resource balance (prefer 40-70% utilization)
		score += p.scoreResourceBalance(w, allocated, svcReq) * p.weights.ResourceBalance / 100

		// Factor 2: Hardware affinity (GPU for AI, NVMe for DBs)
		affinityScore, affinityWarning := p.scoreHardwareAffinity(svc, w, svcReq)
		score += affinityScore * p.weights.HardwareAffinity / 100
		if affinityWarning != "" {
			warnings = append(warnings, affinityWarning)
		}

		// Factor 3: Node type preference
		score += p.scoreNodeTypePreference(svc, w) * p.weights.NodeTypePreference / 100

		// Factor 4: Dependency locality (co-locate with dependencies)
		score += p.scoreDependencyLocality(svc, w, previousPlacements) * p.weights.DependencyLocality / 100

		// Factor 5: Load distribution (spread services)
		score += p.scoreLoadDistribution(w, allocated) * p.weights.LoadDistribution / 100

		// Factor 6: Provider affinity (same cloud provider)
		score += p.scoreProviderAffinity(svc, w, previousPlacements) * p.weights.ProviderAffinity / 100

		scored = append(scored, workerWithScore{
			worker:   w,
			score:    score,
			warnings: warnings,
		})
	}

	// Sort by score (descending), then by service count (ascending) for load balancing
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// If scores are equal, prefer worker with fewer services
		return allocation[scored[i].worker.ID].ServiceCount < allocation[scored[j].worker.ID].ServiceCount
	})

	return scored
}

// scoreResourceBalance prefers workers at 40-70% utilization (avoid extremes).
func (p *PlacementEngine) scoreResourceBalance(w core.Worker, allocated *core.WorkerUsage, req serviceRequirement) int {
	cpuUtil := float64(allocated.CPU+req.CPU) / float64(coresToMillicores(w.Capabilities.CPU))
	ramUtil := float64(allocated.RAM+req.RAM) / float64(w.Capabilities.RAM)
	avgUtil := (cpuUtil + ramUtil) / 2.0

	// Ideal: 40-70% utilization
	if avgUtil >= 0.4 && avgUtil <= 0.7 {
		return 100
	}
	// Acceptable: 20-90%
	if avgUtil >= 0.2 && avgUtil <= 0.9 {
		return 70
	}
	// Avoid: <20% (underutilized) or >90% (overloaded)
	return 30
}

// scoreHardwareAffinity prefers workers with special hardware (GPU, NVMe).
func (p *PlacementEngine) scoreHardwareAffinity(svc core.ServiceSpec, w core.Worker, req serviceRequirement) (int, string) {
	score := 50 // Default neutral score
	warning := ""

	// Database services prefer NVMe storage
	if isDatabaseService(svc.Type) {
		if w.Capabilities.HasNVMe {
			score = 90 // Strong preference
		} else {
			score = 50 // Acceptable but not ideal
			warning = "Database on HDD/SATA will have slower I/O"
		}
	}

	// Media services prefer hardware transcoding
	if isMediaService(svc.Type) {
		if w.Capabilities.HasHWTranscode {
			score = 90
		} else {
			score = 40
			warning = "Media transcoding without hardware acceleration will use high CPU"
		}
	}

	return score, warning
}

// scoreNodeTypePreference prefers appropriate node types per service.
func (p *PlacementEngine) scoreNodeTypePreference(svc core.ServiceSpec, w core.Worker) int {
	// Control plane services (PocketBase, Headscale) → main node
	if isControlPlaneService(svc.Type) {
		if w.Type == "main" {
			return 100
		}
		return 40
	}

	// Storage-heavy services → storage nodes
	if isStorageService(svc.Type) {
		if w.Type == "storage" {
			return 100
		}
		return 60
	}

	// Compute-heavy services → worker nodes
	if isComputeService(svc.Type) {
		if w.Type == "worker" {
			return 100
		}
		return 70
	}

	// Default: any node type acceptable
	return 70
}

// scoreDependencyLocality prefers co-locating services with their dependencies.
func (p *PlacementEngine) scoreDependencyLocality(svc core.ServiceSpec, w core.Worker, placements []core.ServicePlacement) int {
	if len(svc.Needs) == 0 {
		return 50 // Neutral if no dependencies
	}

	// Check how many dependencies are on this worker
	depCount := 0
	for _, dep := range svc.Needs {
		for _, placement := range placements {
			if placement.Service.Name == dep && placement.WorkerID == w.ID {
				depCount++
			}
		}
	}

	// Prefer workers with more dependencies
	if depCount == len(svc.Needs) {
		return 100 // All deps co-located
	}
	if depCount > 0 {
		return 70 + (depCount * 30 / len(svc.Needs)) // Partial co-location
	}
	return 30 // No deps co-located
}

// scoreLoadDistribution prefers workers with fewer services (spread load).
func (p *PlacementEngine) scoreLoadDistribution(w core.Worker, allocated *core.WorkerUsage) int {
	serviceCount := allocated.ServiceCount

	// Linear scoring: fewer services = higher score
	// 0 services: 100 points
	// 1 service:  90 points
	// 2 services: 80 points
	// 3 services: 70 points
	// 4 services: 60 points
	// 5 services: 50 points
	// 6+ services: 40 points

	if serviceCount == 0 {
		return 100
	}
	if serviceCount <= 5 {
		return 100 - (serviceCount * 10)
	}
	// Many services (hotspot): low score
	return 40
}

// scoreProviderAffinity prefers same cloud provider for related services.
func (p *PlacementEngine) scoreProviderAffinity(svc core.ServiceSpec, w core.Worker, placements []core.ServicePlacement) int {
	if len(placements) == 0 {
		return 50 // Neutral for first service
	}

	// Count services on same provider
	sameProviderCount := 0
	for _, placement := range placements {
		// Find worker for this placement
		if placement.WorkerID == w.ID {
			sameProviderCount++
		}
	}

	// Prefer same provider (reduces data transfer costs)
	if sameProviderCount > 0 {
		return 80
	}
	return 40
}

// =============================================================================
// Helper Functions
// =============================================================================

// cpuMillicoresPerCore is the internal CPU unit for placement.
//
// core.WorkerCapabilities.CPU counts whole cores, and the requirement estimates
// used to be whole cores too. That made one core the smallest thing a service
// could ask for, so eight containers needed eight cores -- on a 4-core host the
// eighth service was unplaceable no matter how idle the others were. Container
// workloads are fractional: whoami and homepage sit at a few percent of a core.
// Placement therefore does its arithmetic in millicores and converts the
// worker's whole-core capacity once, on the way in. The public types are
// unchanged.
const cpuMillicoresPerCore = 1000

// ServiceTypeImmichML is the immich machine-learning sidecar. Its inference
// load is episodic and time-shared, so it reserves a burstable share rather
// than a dedicated core.
const ServiceTypeImmichML = "immich-ml"

// serviceTypeWhoami is the debug echo container; the smallest thing a kit ships.
const serviceTypeWhoami = "whoami"

func coresToMillicores(cores int) int { return cores * cpuMillicoresPerCore }

type serviceRequirement struct {
	CPU            int
	RAM            int
	Disk           int
	RequiresGPU    bool
	RequiresDocker bool
	Arch           string
}

// estimateServiceRequirements returns resource needs for a service, in
// millicores for CPU.
//
// These are steady-state container reservations, not peak capability: homelab
// hardware time-shares burst load (transcoding, ML inference), so reserving
// peaks would price every default kit off the 2-core machines the product
// targets. The invariant is pinned by TestFullDefaultKitFitsA2Core4GBHost.
//
// Wizard specs carry generic types (media/auth/cache) with specific names, so
// a well-known service name is matched before falling back to the type.
func estimateServiceRequirements(svc core.ServiceSpec) serviceRequirement {
	req := serviceRequirement{
		RequiresDocker: true,    // Most services need Docker
		Arch:           "amd64", // Default architecture
	}
	estimate, ok := serviceResourceEstimate(svc.Name)
	if !ok {
		estimate, ok = serviceResourceEstimate(svc.Type)
	}
	if !ok {
		// Conservative default for a small containerised service.
		estimate = resourceEstimate{CPU: 200, RAM: 256, Disk: 2}
	}
	req.CPU, req.RAM, req.Disk = estimate.CPU, estimate.RAM, estimate.Disk
	return req
}

type resourceEstimate struct {
	CPU  int
	RAM  int
	Disk int
}

func serviceResourceEstimate(key string) (resourceEstimate, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case ServiceTypeDatabase, ServiceTypePostgres, ServiceTypeMySQL, ServiceTypeMariaDB, "immich-postgres":
		return resourceEstimate{CPU: 300, RAM: 512, Disk: 8}, true

	case "jellyfin", "plex", "emby":
		// Transcoding saturates cores while it runs, but it time-shares with
		// everything else; a dedicated-two-core reservation would ban these
		// from every small host.
		return resourceEstimate{CPU: 750, RAM: 1024, Disk: 50}, true

	case ServiceTypeImmichML, "immich-machine-learning":
		// Model inference is episodic: it wants a core while classifying, not
		// a dedicated one around the clock.
		return resourceEstimate{CPU: 400, RAM: 1024, Disk: 8}, true

	case "immich", "immich-server", "media":
		return resourceEstimate{CPU: 400, RAM: 768, Disk: 15}, true

	case "nextcloud", "photoprism":
		return resourceEstimate{CPU: 400, RAM: 768, Disk: 20}, true

	case "reverse-proxy", "traefik", "nginx", "caddy":
		return resourceEstimate{CPU: 100, RAM: 128, Disk: 2}, true

	case "auth", "pocket-id", "vaultwarden", "authelia", "pocketbase", "headscale":
		return resourceEstimate{CPU: 100, RAM: 128, Disk: 2}, true

	case "monitoring", "otel-collector", "otel-gateway", "victoriametrics", "prometheus", "grafana", "uptime-kuma":
		return resourceEstimate{CPU: 200, RAM: 256, Disk: 4}, true

	case "redis", "valkey", "memcached", "cache", "immich-redis":
		return resourceEstimate{CPU: 100, RAM: 256, Disk: 2}, true

	case "homepage":
		return resourceEstimate{CPU: 100, RAM: 128, Disk: 1}, true

	case serviceTypeWhoami:
		return resourceEstimate{CPU: 50, RAM: 64, Disk: 1}, true

	default:
		return resourceEstimate{}, false
	}
}

// Service type classification helpers

func isDatabaseService(svcType string) bool {
	switch svcType {
	case ServiceTypeDatabase, ServiceTypePostgres, ServiceTypeMySQL, ServiceTypeMariaDB, "mongodb", "redis":
		return true
	}
	return false
}

func isMediaService(svcType string) bool {
	switch svcType {
	case "jellyfin", "plex", "emby", "kodi":
		return true
	}
	return false
}

func isControlPlaneService(svcType string) bool {
	switch svcType {
	case "pocketbase", "headscale", "traefik", "nginx", "caddy":
		return true
	}
	return false
}

func isStorageService(svcType string) bool {
	switch svcType {
	case "nextcloud", "photoprism", "immich", "seafile":
		return true
	}
	return false
}

func isComputeService(svcType string) bool {
	return isDatabaseService(svcType) || isMediaService(svcType)
}

// sortByDependencies returns services ordered by dependencies (topological sort).
func (p *PlacementEngine) sortByDependencies(services []core.ServiceSpec) []core.ServiceSpec {
	// Simple implementation: services with no dependencies first
	ordered := make([]core.ServiceSpec, 0, len(services))
	remaining := make([]core.ServiceSpec, len(services))
	copy(remaining, services)

	for len(remaining) > 0 {
		progress := false
		for i := 0; i < len(remaining); i++ {
			svc := remaining[i]
			// Check if all dependencies are already placed
			allDepsMet := true
			for _, dep := range svc.Needs {
				found := false
				for _, placed := range ordered {
					if placed.Name == dep {
						found = true
						break
					}
				}
				if !found {
					allDepsMet = false
					break
				}
			}

			if allDepsMet {
				ordered = append(ordered, svc)
				remaining = append(remaining[:i], remaining[i+1:]...)
				progress = true
				break
			}
		}

		// Prevent infinite loop if circular dependencies exist
		if !progress {
			// Add remaining services anyway (circular dep detected)
			ordered = append(ordered, remaining...)
			break
		}
	}

	return ordered
}

// allocateResources updates worker usage after placing a service.
func (p *PlacementEngine) allocateResources(svc core.ServiceSpec, workerID string, allocation map[string]*core.WorkerUsage) {
	req := estimateServiceRequirements(svc)
	usage := allocation[workerID]
	usage.CPU += req.CPU
	usage.RAM += req.RAM
	usage.Disk += req.Disk
	usage.ServiceCount++
}
