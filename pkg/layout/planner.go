package layout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Planner is the layout search port. An implementation is deterministic for
// equal inputs and bounds its work by Problem.WorkBudget, never by the clock.
type Planner interface {
	Identity() PlannerIdentity
	Plan(ctx context.Context, snapshot Snapshot, problem Problem) (Result, error)
}

// DefaultWorkBudget bounds the repack search in node visits.
const DefaultWorkBudget = 20000

// ErrStaleProblem is returned when a problem was built for another snapshot.
var ErrStaleProblem = errors.New("layout: problem was built for a different snapshot")

// BestFit is the first-slice baseline: best-fit-decreasing inside household
// priority bands. Important workloads go to the most available node, standard
// workloads prefer the most available node and then a node without important
// workloads, tolerant workloads prefer any node without important workloads;
// inside those preferences the tightest fit wins so larger nodes stay free.
// When the greedy pass fails, a bounded depth-first repack either finds a
// layout, proves that none exists, or reports budget-exhausted.
type BestFit struct{}

// Identity names the baseline.
func (BestFit) Identity() PlannerIdentity {
	return PlannerIdentity{Name: "best-fit-bands", Version: "1"}
}

type nodeState struct {
	node      Node
	load      Resources
	important int
	workloads []string
	// The base is the node before the new selection: existing components
	// and observed usage. Search restarts from it.
	baseLoad      Resources
	baseImportant int
	baseWorkloads []string
}

func (state *nodeState) reset() {
	state.load, state.important = state.baseLoad, state.baseImportant
	state.workloads = append([]string(nil), state.baseWorkloads...)
}

type candidate struct {
	index   int
	keys    []float64
	labels  []string
	unknown []string
}

type placement struct {
	demand   Demand
	node     int
	decisive string
	rivals   []string
	unknown  []string
	repacked bool
	pinned   bool
}

// Plan computes at most one recommended option.
func (p BestFit) Plan(ctx context.Context, snapshot Snapshot, problem Problem) (Result, error) {
	if problem.SnapshotDigest != snapshot.Digest {
		return Result{}, ErrStaleProblem
	}
	result := Result{SnapshotDigest: snapshot.Digest, ProblemDigest: problem.Digest, Planner: p.Identity(), Options: []Option{}}
	budget := problem.WorkBudget
	if budget <= 0 {
		budget = DefaultWorkBudget
	}

	states, notes, excludedUnknown := eligibleNodes(snapshot)
	result.Notes = append(result.Notes, notes...)
	if len(states) == 0 {
		result.Outcome = OutcomeUnknown
		result.Notes = append(result.Notes, reason("no_eligible_node", nil))
		return sealResult(result), nil
	}
	pins := map[string]string{}
	for _, pin := range problem.Pins {
		pins[pin.Workload] = pin.NodeID
	}
	demands := orderedDemands(problem.Demands, pins)

	placements, failed := p.greedy(states, demands, pins)
	if len(failed) > 0 {
		if conflicts := provenConflicts(states, demands, failed, pins); len(conflicts) > 0 {
			result.Conflicts = conflicts
			result.Outcome = OutcomeProvenInfeasible
			if excludedUnknown {
				// A node without capacity evidence might still host it.
				result.Outcome = OutcomeUnknown
				result.Notes = append(result.Notes, reason("infeasibility_unproven_unknown_node", nil))
			}
			return sealResult(result), nil
		}
		found, exhausted, work, err := repack(ctx, states, demands, pins, budget)
		result.Work = work
		if err != nil {
			return Result{}, err
		}
		switch {
		case found != nil:
			placements = found
		case exhausted:
			result.Outcome = OutcomeProvenInfeasible
			for _, demand := range failed {
				result.Conflicts = append(result.Conflicts, Conflict{Workload: demand.Workload, Reasons: []Reason{reason("no_combination_fits", map[string]string{"workload": demand.Workload, "title": demandTitle(demand)})}})
			}
			if excludedUnknown {
				result.Outcome = OutcomeUnknown
				result.Notes = append(result.Notes, reason("infeasibility_unproven_unknown_node", nil))
			}
			return sealResult(result), nil
		default:
			result.Outcome = OutcomeBudgetExhausted
			return sealResult(result), nil
		}
	}

	option := buildOption(states, placements)
	result.Options = append(result.Options, sealOption(option, snapshot.Digest, problem.Digest))
	result.Outcome = OutcomeFeasible
	return sealResult(result), nil
}

func eligibleNodes(snapshot Snapshot) ([]*nodeState, []Reason, bool) {
	var states []*nodeState
	var notes []Reason
	excludedUnknown := false
	for _, node := range snapshot.Nodes {
		params := map[string]string{"node": node.Name}
		switch {
		case node.TrustClass != TrustOwnerControlled:
			notes = append(notes, reason("node_not_owner_controlled", params))
		case node.State != NodeConnected && node.State != NodePlanned:
			notes = append(notes, reason("node_not_connected", params))
			// It might host the selection once it is back; nothing is proven.
			excludedUnknown = true
		case node.Capacity.RAMGB <= 0 || node.Capacity.CPUCores <= 0:
			notes = append(notes, reason("node_capacity_unknown", params))
			excludedUnknown = true
		default:
			states = append(states, &nodeState{node: node})
		}
	}
	index := map[string]*nodeState{}
	for _, state := range states {
		index[state.node.ID] = state
	}
	managed := map[string]Resources{}
	for _, component := range snapshot.Components {
		state := index[component.NodeID]
		if state == nil {
			continue
		}
		if component.Management == ManagementManaged {
			managed[component.NodeID] = managed[component.NodeID].Add(component.Reservation)
		}
		if component.Band == BandImportant {
			state.important++
		}
		state.workloads = append(state.workloads, component.Workload)
	}
	for _, state := range states {
		// Observed usage already contains what managed components consume;
		// count the larger of the two, never both.
		base := managed[state.node.ID]
		if state.node.Usage != nil {
			base = base.Max(*state.node.Usage)
		}
		state.baseLoad, state.baseImportant = base, state.important
		state.baseWorkloads = append([]string(nil), state.workloads...)
		state.reset()
	}
	return states, notes, excludedUnknown
}

func orderedDemands(demands []Demand, pins map[string]string) []Demand {
	ordered := append([]Demand(nil), demands...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if ra, rb := bandRank(a.Band), bandRank(b.Band); ra != rb {
			return ra < rb
		}
		_, pa := pins[a.Workload]
		_, pb := pins[b.Workload]
		if pa != pb {
			return pa
		}
		if a.Reservation.RAMGB != b.Reservation.RAMGB {
			return a.Reservation.RAMGB > b.Reservation.RAMGB
		}
		if a.Reservation.CPUCores != b.Reservation.CPUCores {
			return a.Reservation.CPUCores > b.Reservation.CPUCores
		}
		if a.Reservation.StorageGB != b.Reservation.StorageGB {
			return a.Reservation.StorageGB > b.Reservation.StorageGB
		}
		return a.Workload < b.Workload
	})
	return ordered
}

func (BestFit) greedy(states []*nodeState, demands []Demand, pins map[string]string) ([]placement, []Demand) {
	var placements []placement
	var failed []Demand
	for _, demand := range demands {
		candidates := rankCandidates(states, demand, pins)
		if len(candidates) == 0 {
			failed = append(failed, demand)
			continue
		}
		best := candidates[0]
		chosen := placement{demand: demand, node: best.index, unknown: best.unknown, pinned: pins[demand.Workload] != ""}
		if len(candidates) == 1 {
			chosen.decisive = "only"
		} else {
			chosen.decisive = decisiveKey(best, candidates[1])
			chosen.rivals = []string{states[candidates[1].index].node.Name}
		}
		commit(states[best.index], demand)
		placements = append(placements, chosen)
	}
	return placements, failed
}

func commit(state *nodeState, demand Demand) {
	state.load = state.load.Add(demand.Reservation)
	if demand.Band == BandImportant {
		state.important++
	}
	state.workloads = append(state.workloads, demand.Workload)
}

func uncommit(state *nodeState, demand Demand) {
	state.load = Resources{
		CPUCores:  state.load.CPUCores - demand.Reservation.CPUCores,
		RAMGB:     state.load.RAMGB - demand.Reservation.RAMGB,
		StorageGB: state.load.StorageGB - demand.Reservation.StorageGB,
	}
	if demand.Band == BandImportant {
		state.important--
	}
	state.workloads = state.workloads[:len(state.workloads)-1]
}

// rankCandidates orders the nodes that can host the demand now.
func rankCandidates(states []*nodeState, demand Demand, pins map[string]string) []candidate {
	var candidates []candidate
	pinned := pins[demand.Workload]
	for index, state := range states {
		if pinned != "" && state.node.ID != pinned {
			continue
		}
		shortfall, unknown := fits(demand, state)
		if len(shortfall) > 0 {
			continue
		}
		candidates = append(candidates, rankedCandidate(index, state, demand, unknown))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		for k := range candidates[i].keys {
			if candidates[i].keys[k] != candidates[j].keys[k] {
				return candidates[i].keys[k] < candidates[j].keys[k]
			}
		}
		return states[candidates[i].index].node.ID < states[candidates[j].index].node.ID
	})
	return candidates
}

func rankedCandidate(index int, state *nodeState, demand Demand, unknown []string) candidate {
	availability := float64(availabilityRank(state.node.Availability.Class))
	isolation := 0.0
	if state.important > 0 {
		isolation = 1
	}
	verified := 0.0
	if len(unknown) > 0 {
		verified = 1
	}
	left := state.node.Capacity.RAMGB - state.load.RAMGB - demand.Reservation.RAMGB
	cpuLeft := state.node.Capacity.CPUCores - state.load.CPUCores - demand.Reservation.CPUCores
	// Quantize the fit so float noise never decides a tie.
	left, cpuLeft = math.Round(left*1000)/1000, math.Round(cpuLeft*1000)/1000
	switch demand.Band {
	case BandImportant:
		return candidate{index: index, unknown: unknown, keys: []float64{availability, verified, left, cpuLeft}, labels: []string{"availability", "verified", "fit", "fit"}}
	case BandStandard:
		return candidate{index: index, unknown: unknown, keys: []float64{availability, isolation, verified, left, cpuLeft}, labels: []string{"availability", "isolation", "verified", "fit", "fit"}}
	default:
		return candidate{index: index, unknown: unknown, keys: []float64{isolation, verified, left, cpuLeft}, labels: []string{"isolation", "verified", "fit", "fit"}}
	}
}

func decisiveKey(best, runnerUp candidate) string {
	for k := range best.keys {
		if best.keys[k] != runnerUp.keys[k] {
			return best.labels[k]
		}
	}
	return "order"
}

// fits checks floors against the whole host and the reservation against what
// is left. Missing evidence is reported as unknown, never as a shortfall.
func fits(demand Demand, state *nodeState) (shortfall, unknown []string) {
	capacity, floor := state.node.Capacity, demand.HostFloor
	check := func(axis string, total, used, minimum, need float64) {
		if minimum == 0 && need == 0 {
			return
		}
		if total <= 0 {
			unknown = append(unknown, axis)
			return
		}
		if total < minimum || total-used < need-1e-9 {
			shortfall = append(shortfall, axis)
		}
	}
	check("cpu", capacity.CPUCores, state.load.CPUCores, floor.CPUCores, demand.Reservation.CPUCores)
	check("ram", capacity.RAMGB, state.load.RAMGB, floor.RAMGB, demand.Reservation.RAMGB)
	check("storage", capacity.StorageGB, state.load.StorageGB, floor.StorageGB, demand.Reservation.StorageGB)
	if demand.CPUUnspecified {
		unknown = append(unknown, "cpu_reservation")
	}
	for _, role := range demand.RequiresRoles {
		if len(state.node.Roles) == 0 {
			unknown = append(unknown, "roles")
			break
		}
		if !containsRole(state.node.Roles, role) {
			shortfall = append(shortfall, "roles")
			break
		}
	}
	if floor.AMD64Level > 0 && strings.EqualFold(state.node.Arch, "amd64") {
		switch {
		case state.node.AMD64Level == 0:
			unknown = append(unknown, "cpu_microarchitecture")
		case state.node.AMD64Level < floor.AMD64Level:
			shortfall = append(shortfall, "cpu_microarchitecture")
		}
	} else if floor.AMD64Level > 0 && state.node.Arch == "" {
		unknown = append(unknown, "cpu_architecture")
	}
	if gpu := demand.Accelerator; gpu != nil {
		observed := state.node.Accelerator
		switch {
		case observed == nil:
			shortfall = append(shortfall, "accelerator")
		case observed.Vendor == "":
			unknown = append(unknown, "accelerator_vendor")
		case !strings.EqualFold(observed.Vendor, gpu.Vendor):
			shortfall = append(shortfall, "accelerator")
		case gpu.MinDriverMajor > 0 && observed.DriverMajor == 0:
			unknown = append(unknown, "accelerator_driver")
		case gpu.MinDriverMajor > 0 && observed.DriverMajor < gpu.MinDriverMajor:
			shortfall = append(shortfall, "accelerator_driver")
		}
	}
	return shortfall, unknown
}

// provenConflicts returns sufficient reasons when a failed demand cannot fit
// on any node even before the new selection is placed, or when the selection
// as a whole needs more than all nodes have left.
func provenConflicts(states []*nodeState, demands, failed []Demand, pins map[string]string) []Conflict {
	// Re-evaluate against the base only: the new placements are undone.
	base := make([]*nodeState, len(states))
	for i, state := range states {
		clone := *state
		clone.reset()
		base[i] = &clone
	}
	var conflicts []Conflict
	for _, demand := range failed {
		var reasons []Reason
		pinned := pins[demand.Workload]
		hostable := false
		for _, state := range base {
			if pinned != "" && state.node.ID != pinned {
				continue
			}
			shortfall, _ := fits(demand, state)
			if len(shortfall) == 0 {
				hostable = true
				break
			}
		}
		if hostable {
			continue
		}
		reasons = append(reasons, shortfallReasons(demand, base, pinned)...)
		conflicts = append(conflicts, Conflict{Workload: demand.Workload, Reasons: reasons})
	}
	if len(conflicts) > 0 {
		return conflicts
	}
	// Aggregate bound: the new reservations exceed every node's free RAM.
	need, free := 0.0, 0.0
	for _, demand := range demands {
		need += demand.Reservation.RAMGB
	}
	for _, state := range base {
		free += math.Max(0, state.node.Capacity.RAMGB-state.load.RAMGB)
	}
	if need > free+1e-9 {
		for _, demand := range failed {
			conflicts = append(conflicts, Conflict{Workload: demand.Workload, Reasons: []Reason{reason("selection_exceeds_free_ram", map[string]string{
				"need": formatGB(need), "free": formatGB(free),
			})}})
		}
	}
	return conflicts
}

func shortfallReasons(demand Demand, states []*nodeState, pinned string) []Reason {
	largest := Resources{}
	vendors := []string{}
	for _, state := range states {
		if pinned != "" && state.node.ID != pinned {
			continue
		}
		largest = largest.Max(Resources{
			CPUCores:  state.node.Capacity.CPUCores,
			RAMGB:     state.node.Capacity.RAMGB - state.load.RAMGB,
			StorageGB: state.node.Capacity.StorageGB - state.load.StorageGB,
		})
		if state.node.Accelerator != nil && state.node.Accelerator.Vendor != "" {
			vendors = append(vendors, state.node.Accelerator.Vendor)
		}
	}
	params := map[string]string{"workload": demand.Workload, "title": demandTitle(demand)}
	var reasons []Reason
	needRAM := math.Max(demand.HostFloor.RAMGB, demand.Reservation.RAMGB)
	if needRAM > largest.RAMGB+1e-9 {
		reasons = append(reasons, reason("needs_more_ram", merge(params, map[string]string{"need": formatGB(needRAM), "largest": formatGB(largest.RAMGB)})))
	}
	if needCPU := math.Max(demand.HostFloor.CPUCores, demand.Reservation.CPUCores); needCPU > largest.CPUCores+1e-9 {
		reasons = append(reasons, reason("needs_more_cpu", merge(params, map[string]string{"need": formatCores(needCPU), "largest": formatCores(largest.CPUCores)})))
	}
	if needStorage := math.Max(demand.HostFloor.StorageGB, demand.Reservation.StorageGB); needStorage > largest.StorageGB+1e-9 {
		reasons = append(reasons, reason("needs_more_storage", merge(params, map[string]string{"need": formatGB(needStorage), "largest": formatGB(largest.StorageGB)})))
	}
	if demand.Accelerator != nil {
		has := false
		for _, vendor := range vendors {
			if strings.EqualFold(vendor, demand.Accelerator.Vendor) {
				has = true
			}
		}
		if !has {
			reasons = append(reasons, reason("missing_accelerator", merge(params, map[string]string{"vendor": demand.Accelerator.Vendor})))
		}
	}
	if pinned != "" {
		reasons = append(reasons, reason("pinned_node_lacks_room", params))
	}
	if len(reasons) == 0 {
		reasons = append(reasons, reason("no_node_meets_requirements", params))
	}
	return reasons
}

// repack is a bounded depth-first search over the same ranking. It undoes
// the greedy placements and tries again from the base load.
func repack(ctx context.Context, states []*nodeState, demands []Demand, pins map[string]string, budget int) ([]placement, bool, int, error) {
	for _, state := range states {
		state.reset()
	}
	work := 0
	chosen := make([]placement, 0, len(demands))
	var walk func(depth int) (bool, error)
	walk = func(depth int) (bool, error) {
		if depth == len(demands) {
			return true, nil
		}
		if work >= budget {
			return false, nil
		}
		if work%256 == 0 {
			if err := ctx.Err(); err != nil {
				return false, fmt.Errorf("layout: repack cancelled: %w", err)
			}
		}
		demand := demands[depth]
		for _, candidate := range rankCandidates(states, demand, pins) {
			work++
			commit(states[candidate.index], demand)
			chosen = append(chosen, placement{demand: demand, node: candidate.index, decisive: "repack", unknown: candidate.unknown, repacked: true, pinned: pins[demand.Workload] != ""})
			ok, err := walk(depth + 1)
			if err != nil || ok {
				return ok, err
			}
			chosen = chosen[:len(chosen)-1]
			uncommit(states[candidate.index], demand)
			if work >= budget {
				return false, nil
			}
		}
		return false, nil
	}
	ok, err := walk(0)
	if err != nil {
		return nil, false, work, err
	}
	if ok {
		return chosen, false, work, nil
	}
	return nil, work < budget, work, nil
}

func buildOption(states []*nodeState, placements []placement) Option {
	option := Option{ID: "recommended"}
	for _, placed := range placements {
		state := states[placed.node]
		option.Assignments = append(option.Assignments, Assignment{
			Workload: placed.demand.Workload, Goal: placed.demand.Goal, Modules: placed.demand.Modules,
			NodeID: state.node.ID, SpecNodeRef: state.node.SpecNodeRef, Band: placed.demand.Band,
			Provenance: placed.demand.Provenance, Reservation: placed.demand.Reservation,
			Reasons: explainPlacement(placed, state, states),
		})
	}
	sort.SliceStable(option.Assignments, func(i, j int) bool { return option.Assignments[i].Workload < option.Assignments[j].Workload })
	for _, state := range states {
		workloads := append([]string(nil), state.workloads...)
		sort.Strings(workloads)
		option.Loads = append(option.Loads, NodeLoad{NodeID: state.node.ID, Capacity: state.node.Capacity, Reserved: roundResources(state.load), Workloads: workloads})
	}
	return option
}

func roundResources(r Resources) Resources {
	round := func(v float64) float64 { return math.Round(v*1000) / 1000 }
	return Resources{CPUCores: round(r.CPUCores), RAMGB: round(r.RAMGB), StorageGB: round(r.StorageGB)}
}

func merge(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func containsRole(roles []string, role string) bool {
	for _, candidate := range roles {
		if strings.EqualFold(candidate, role) {
			return true
		}
	}
	return false
}
