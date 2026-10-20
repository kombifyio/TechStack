package unifier

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/layout"
)

// LayoutInput is the server-side input of one layout computation. Routes fill
// the inventory from the tenant registry after the owner scope check; the
// client contributes only its selection.
type LayoutInput struct {
	Scope   string
	StackID string
	OwnerID string
	Workers []core.Worker
	// PrimaryServerID is the kit deployment's controller server.
	PrimaryServerID string
	// SpecNodeRefs maps a server id to its StackSpec node id; NodeRoles to
	// that node's StackSpec roles.
	SpecNodeRefs map[string]string
	NodeRoles    map[string][]string
	// RequiresRoles are the release-authored placement roles per workload.
	RequiresRoles map[string][]string
	// Existing are the deployment's workloads that stay where they are.
	Existing []layout.Component
	// Planned are nodes the run adds with a known size.
	Planned        []layout.Node
	InFlight       []string
	Goals          []string
	ComputeProfile string
	Settings       map[string]map[string]any
	Capabilities   map[string]map[string]string
	WorkBudget     int
}

// LayoutComputation is the complete, digest-bound answer for one input.
type LayoutComputation struct {
	Snapshot layout.Snapshot `json:"snapshot"`
	Problem  layout.Problem  `json:"problem"`
	Result   layout.Result   `json:"result"`
}

// ComputeLayout builds the snapshot and problem and runs the planner. The
// result is advice only; nothing is executed here (owner decision D2).
func ComputeLayout(ctx context.Context, catalog stackkitrelease.UseCaseCatalog, input LayoutInput, planner layout.Planner) (LayoutComputation, error) {
	if planner == nil {
		planner = layout.BestFit{}
	}
	snapshot := layout.SealSnapshot(layoutSnapshot(input))
	footprints, err := selectedFootprints(catalog, input.Goals, input.ComputeProfile, input.Settings, input.Capabilities)
	if err != nil {
		return LayoutComputation{}, err
	}
	existingNode := map[string]string{}
	for _, component := range input.Existing {
		existingNode[component.Workload] = component.NodeID
	}
	// Deployed workloads stay where they are (no migration in L0); only the
	// new ones are planned, grouped per use case.
	fresh := make([]stackkitrelease.WorkloadFootprint, 0, len(footprints))
	for _, footprint := range footprints {
		if _, deployed := existingNode[footprint.Workload]; !deployed {
			fresh = append(fresh, footprint)
		}
	}
	demands := footprintDemands(catalog, fresh)
	var planned []layout.Demand
	var pins []layout.Pin
	primary := primaryNodeID(snapshot)
	for _, demand := range demands {
		for _, workload := range demand.Workloads {
			demand.RequiresRoles = appendMissing(demand.RequiresRoles, input.RequiresRoles[workload]...)
		}
		switch node := existingNode[demand.Goal]; {
		case node != "":
			// An add-on joins its use case on the node that already runs it.
			pins = append(pins, layout.Pin{Workload: demand.Workload, NodeID: node, Reason: "with_use_case"})
		case demand.Provenance != "measured" && primary != "":
			pins = append(pins, layout.Pin{Workload: demand.Workload, NodeID: primary, Reason: "profile_not_measured"})
		}
		planned = append(planned, demand)
	}
	profile := input.ComputeProfile
	if profile == "" {
		profile = "standard"
	}
	problem := layout.SealProblem(layout.Problem{
		SnapshotDigest: snapshot.Digest,
		CatalogRelease: catalog.Release.Tag,
		CatalogDigest:  catalog.Release.SourceSHA,
		UseCases:       append([]string(nil), input.Goals...),
		ComputeProfile: profile,
		Demands:        planned,
		Pins:           pins,
		PolicyProfile:  layout.DefaultPolicyProfile,
		WorkBudget:     input.WorkBudget,
	})
	result, err := planner.Plan(ctx, snapshot, problem)
	if err != nil {
		return LayoutComputation{}, err
	}
	return LayoutComputation{Snapshot: snapshot, Problem: problem, Result: result}, nil
}

func primaryNodeID(snapshot layout.Snapshot) string {
	for _, node := range snapshot.Nodes {
		if node.Primary {
			return node.ID
		}
	}
	return ""
}

func layoutSnapshot(input LayoutInput) layout.Snapshot {
	snapshot := layout.Snapshot{Scope: input.Scope, StackID: input.StackID, Components: input.Existing, InFlight: input.InFlight, Source: "worker-registry"}
	for index, worker := range input.Workers {
		node := layoutNodeFromWorker(worker, index, input.OwnerID)
		node.Primary = input.PrimaryServerID != "" && node.ID == input.PrimaryServerID
		node.SpecNodeRef = input.SpecNodeRefs[node.ID]
		node.Roles = append([]string(nil), input.NodeRoles[node.ID]...)
		node.Address = strings.TrimSpace(worker.Tags["ip"])
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	snapshot.Nodes = append(snapshot.Nodes, input.Planned...)
	return snapshot
}

func layoutNodeKey(worker core.Worker, index int) string {
	if id := strings.TrimSpace(worker.ServerID); id != "" {
		return id
	}
	if id := strings.TrimSpace(worker.ID); id != "" {
		return id
	}
	return fmt.Sprint(index)
}

// layoutNodeFromWorker projects a registry worker. Every enrolled node is the
// owner's own (D1); usage counts only when it was observed.
func layoutNodeFromWorker(worker core.Worker, index int, ownerID string) layout.Node {
	node := layout.Node{
		ID: layoutNodeKey(worker, index), Name: firstNonEmptyRecommendation(worker.Name, worker.ID),
		Operator: ownerID, TrustClass: layout.TrustOwnerControlled, State: layout.NodeConnected,
		Capacity: layout.Resources{
			CPUCores: float64(worker.Capabilities.CPU), RAMGB: float64(worker.Capabilities.RAM) / 1024, StorageGB: float64(worker.Capabilities.Disk),
		},
		CapacitySource: "inventory",
		Arch:           strings.ToLower(strings.TrimSpace(worker.Capabilities.Arch)),
		AMD64Level:     worker.Capabilities.AMD64MicroarchitectureLevel,
		Availability:   layout.Availability{Class: layout.AvailabilityUnknown, Source: "none"},
	}
	switch strings.ToLower(strings.TrimSpace(worker.Status)) {
	case "offline", "error", "disabled", "stale", "revoked":
		node.State = layout.NodeOffline
	}
	if worker.Capabilities.RAM <= 0 || worker.Capabilities.CPU <= 0 {
		node.Missing = append(node.Missing, "capacity")
	}
	if worker.UsageObserved || worker.Usage.RAM > 0 || worker.Usage.CPU > 0 || worker.Usage.Disk > 0 {
		// Quantized so heartbeat noise does not change the snapshot digest.
		node.Usage = &layout.Resources{
			CPUCores:  quantize(float64(worker.Usage.CPU), 0.25),
			RAMGB:     quantize(float64(worker.Usage.RAM)/1024, 0.25),
			StorageGB: quantize(float64(worker.Usage.Disk), 1),
		}
	} else {
		node.Missing = append(node.Missing, "usage")
	}
	if node.Arch == "amd64" && node.AMD64Level == 0 {
		node.Missing = append(node.Missing, "amd64_level")
	}
	if gpu := worker.Capabilities.GPU; gpu != nil && (gpu.Vendor != "" || gpu.Model != "") {
		node.Accelerator = &layout.Accelerator{Vendor: strings.ToLower(gpu.Vendor), DriverMajor: gpu.DriverMajor}
	}
	node.Media = layoutFact(worker.Capabilities.Media)
	node.DiskClass = layoutFact(worker.Capabilities.DiskClass)
	node.PowerClass = layoutFact(worker.Capabilities.PowerClass)
	if availability := worker.Availability; availability != nil && availability.Class != "" {
		node.Availability = layout.Availability{Class: availability.Class, Source: availability.Source, Confirmed: availability.Confirmed}
		if ratio := availability.UptimeRatio; ratio != nil {
			// Quantized so a few minutes of history do not change the digest.
			rounded := math.Round(*ratio*20) / 20
			node.Availability.UptimeRatio = &rounded
		}
	}
	return node
}

func layoutFact(fact *core.HardwareFact) *layout.Fact {
	if fact == nil || fact.Class == "" {
		return nil
	}
	return &layout.Fact{Class: fact.Class, Source: fact.Source}
}

func quantize(value, step float64) float64 {
	return math.Ceil(value/step) * step
}

// footprintDemands groups release footprints into one demand per use case:
// in L0 a use case's workloads (its own and its add-ons) and their modules
// land on one node together, so every add-on runs beside its primary.
func footprintDemands(catalog stackkitrelease.UseCaseCatalog, footprints []stackkitrelease.WorkloadFootprint) []layout.Demand {
	byGoal := map[string]*layout.Demand{}
	var order []string
	for _, footprint := range footprints {
		goal, title := workloadGoal(catalog, footprint.Workload)
		demand := byGoal[goal]
		if demand == nil {
			_, goalTitle := workloadGoal(catalog, goal)
			demand = &layout.Demand{
				Workload: goal, Title: firstNonEmptyRecommendation(goalTitle, title), Goal: goal,
				ComputeProfile: footprint.ComputeProfile, Provenance: "measured", Band: layout.DefaultBand(goal),
			}
			byGoal[goal] = demand
			order = append(order, goal)
		}
		if footprint.Workload == goal {
			demand.Alternative = footprint.Alternative
		}
		demand.Workloads = appendMissing(demand.Workloads, footprint.Workload)
		demand.Modules = append(demand.Modules, footprint.Module)
		demand.HostFloor = layout.Floor{
			CPUCores:   math.Max(demand.HostFloor.CPUCores, footprint.HostFloor.MinCPUCores),
			RAMGB:      math.Max(demand.HostFloor.RAMGB, footprint.HostFloor.MinRAMGB),
			StorageGB:  math.Max(demand.HostFloor.StorageGB, footprint.HostFloor.MinStorageGB),
			AMD64Level: maxInt(demand.HostFloor.AMD64Level, footprint.HostFloor.MinAMD64MicroarchitectureLevel),
		}
		demand.Reservation = demand.Reservation.Add(layout.Resources{
			CPUCores: footprint.Reservation.CPUCores, RAMGB: footprint.Reservation.RAMGB, StorageGB: footprint.Reservation.StorageGB,
		})
		demand.CPUUnspecified = demand.CPUUnspecified || footprint.CPUReservationUnspecified
		if footprint.ReservationProvenance != "measured" {
			demand.Provenance = firstNonEmptyRecommendation(footprint.ReservationProvenance, "policy")
		}
		if footprint.Accelerator != nil {
			demand.Accelerator = &layout.AcceleratorDemand{Vendor: strings.ToLower(footprint.Accelerator.Vendor), MinDriverMajor: footprint.Accelerator.MinDriverMajor}
		}
	}
	sort.Strings(order)
	demands := make([]layout.Demand, 0, len(order))
	for _, goal := range order {
		sort.Strings(byGoal[goal].Modules)
		sort.Strings(byGoal[goal].Workloads)
		demands = append(demands, *byGoal[goal])
	}
	return demands
}

func appendMissing(values []string, candidates ...string) []string {
	for _, candidate := range candidates {
		if candidate != "" && !containsString(values, candidate) {
			values = append(values, candidate)
		}
	}
	return values
}

// workloadGoal returns the use case a workload belongs to and its title.
func workloadGoal(catalog stackkitrelease.UseCaseCatalog, workload string) (string, string) {
	for _, useCase := range catalog.UseCases {
		if useCase.ID == workload {
			return useCase.ID, firstNonEmptyRecommendation(useCase.Title, useCase.ID)
		}
		for _, addOn := range useCase.AddOns {
			if addOn.ID == workload {
				return useCase.ID, workload
			}
		}
	}
	return workload, workload
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// reserveFootprints reports whether the footprints fit next to the given
// allocations and adds their reservations where the planner placed them.
// It is the wizard's capacity check behind the Layout Planner port; the
// allocations are planned, not yet deployed, so they add to observed usage.
func reserveFootprints(catalog stackkitrelease.UseCaseCatalog, footprints []stackkitrelease.WorkloadFootprint, workers []core.Worker, allocations map[string]stackkitrelease.UseCaseResources) bool {
	if len(footprints) == 0 {
		return true
	}
	snapshot := layout.Snapshot{Scope: "selection-guidance", Source: "worker-registry"}
	for index, worker := range workers {
		node := layoutNodeFromWorker(worker, index, "")
		node.ID = reservationKey(worker, index)
		node.Availability = layout.Availability{Class: layout.AvailabilityUnknown, Source: "none"}
		if allocated, ok := allocations[node.ID]; ok {
			usage := layout.Resources{}
			if node.Usage != nil {
				usage = *node.Usage
			}
			usage = usage.Add(layout.Resources{CPUCores: allocated.CPUCores, RAMGB: allocated.RAMGB, StorageGB: allocated.StorageGB})
			node.Usage = &usage
		}
		snapshot.Nodes = append(snapshot.Nodes, node)
	}
	snapshot = layout.SealSnapshot(snapshot)
	demands := footprintDemands(catalog, footprints)
	problem := layout.SealProblem(layout.Problem{SnapshotDigest: snapshot.Digest, Demands: demands, PolicyProfile: layout.DefaultPolicyProfile})
	result, err := layout.BestFit{}.Plan(context.Background(), snapshot, problem)
	if err != nil || result.Outcome != layout.OutcomeFeasible || len(result.Options) == 0 {
		return false
	}
	for _, assignment := range result.Options[0].Assignments {
		resources := allocations[assignment.NodeID]
		resources.CPUCores += assignment.Reservation.CPUCores
		resources.RAMGB += assignment.Reservation.RAMGB
		resources.StorageGB += assignment.Reservation.StorageGB
		allocations[assignment.NodeID] = resources
	}
	return true
}

func reservationKey(worker core.Worker, index int) string {
	if worker.ID != "" {
		return worker.ID
	}
	return fmt.Sprint(index)
}
