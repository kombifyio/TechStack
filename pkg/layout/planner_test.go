package layout

import (
	"context"
	"testing"
	"time"
)

func node(id, name string, cpu, ram float64, availability string) Node {
	return Node{
		ID: id, Name: name, Operator: "owner", TrustClass: TrustOwnerControlled, State: NodeConnected,
		Capacity: Resources{CPUCores: cpu, RAMGB: ram, StorageGB: 200}, CapacitySource: "inventory", Arch: "amd64", AMD64Level: 3,
		Availability: Availability{Class: availability, Source: "server-transitions"},
	}
}

func demand(workload string, ram float64) Demand {
	return Demand{
		Workload: workload, Goal: workload, Modules: []string{"stackkits-" + workload + "-runtime"}, ComputeProfile: "standard",
		HostFloor: Floor{CPUCores: 2, RAMGB: 4, StorageGB: 20}, Reservation: Resources{CPUCores: 0.05, RAMGB: ram},
		Provenance: "measured", Band: DefaultBand(workload),
	}
}

func plan(t *testing.T, nodes []Node, demands []Demand, components []Component) Result {
	t.Helper()
	snapshot := SealSnapshot(Snapshot{Scope: "stack:test", Nodes: nodes, Components: components, Source: "test"})
	problem := SealProblem(Problem{SnapshotDigest: snapshot.Digest, CatalogRelease: "v0.53.16", ComputeProfile: "standard", Demands: demands, PolicyProfile: DefaultPolicyProfile})
	result, err := BestFit{}.Plan(context.Background(), snapshot, problem)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return result
}

func assignedNode(result Result, workload string) string {
	for _, option := range result.Options {
		for _, assignment := range option.Assignments {
			if assignment.Workload == workload {
				return assignment.NodeID
			}
		}
	}
	return ""
}

// The household bands decide before packing: important apps share the most
// available node, standard apps stay away from them, tolerant apps go where
// nothing important runs.
func TestBestFitBandsOnTwoOwnerNodes(t *testing.T) {
	nodes := []Node{node("srv-mini", "mini-pc", 4, 16, AvailabilityAlwaysOn), node("srv-gaming", "gaming-pc", 8, 32, AvailabilityPartTime)}
	demands := []Demand{demand("vault", 0.0625), demand("smart-home", 0.4375), demand("photos", 2.375), demand("ai", 1.5)}
	result := plan(t, nodes, demands, nil)
	if result.Outcome != OutcomeFeasible || len(result.Options) != 1 {
		t.Fatalf("outcome %s with %d options", result.Outcome, len(result.Options))
	}
	for workload, want := range map[string]string{"vault": "srv-mini", "smart-home": "srv-mini", "photos": "srv-mini", "ai": "srv-gaming"} {
		if got := assignedNode(result, workload); got != want {
			t.Errorf("%s on %q, want %q", workload, got, want)
		}
	}
	for _, assignment := range result.Options[0].Assignments {
		if assignment.Workload == "vault" && assignment.Reasons[0].Code != "runs_on_always_on" {
			t.Errorf("vault reason %s, want runs_on_always_on", assignment.Reasons[0].Code)
		}
		if assignment.Workload == "ai" && assignment.Reasons[0].Code != "kept_apart" {
			t.Errorf("ai reason %s, want kept_apart", assignment.Reasons[0].Code)
		}
	}
}

// With equal availability the important group packs onto the tighter node
// and heavier standard apps keep apart from it.
func TestBestFitKeepsStandardAppsApartFromImportantOnes(t *testing.T) {
	nodes := []Node{node("srv-nas", "nas", 4, 8, AvailabilityUnknown), node("srv-mini", "mini-pc", 4, 16, AvailabilityUnknown)}
	result := plan(t, nodes, []Demand{demand("vault", 0.0625), demand("photos", 2.375), demand("media", 0.3125)}, nil)
	if got := assignedNode(result, "vault"); got != "srv-nas" {
		t.Fatalf("vault on %q, want the tighter nas", got)
	}
	if assignedNode(result, "photos") != "srv-mini" || assignedNode(result, "media") != "srv-mini" {
		t.Fatalf("standard apps share the node of an important app: %+v", result.Options[0].Assignments)
	}
}

// Equal semantic input gives equal digests, whatever the observation time or
// the order the inventory arrived in.
func TestLayoutIsReproducible(t *testing.T) {
	a := []Node{node("srv-a", "a", 4, 16, AvailabilityAlwaysOn), node("srv-b", "b", 4, 8, AvailabilityAlwaysOn)}
	b := []Node{a[1], a[0]}
	b[0].ObservedAt = time.Now()
	demands := []Demand{demand("vault", 0.0625), demand("photos", 2.375)}
	first, second := plan(t, a, demands, nil), plan(t, b, []Demand{demands[1], demands[0]}, nil)
	if first.Digest != second.Digest || first.Options[0].Digest != second.Options[0].Digest {
		t.Fatalf("digests differ: %s/%s vs %s/%s", first.Digest, first.Options[0].Digest, second.Digest, second.Options[0].Digest)
	}
}

// Only a sufficient reason makes a result proven-infeasible; a node without
// reported hardware keeps the verdict unknown.
func TestHonestOutcomes(t *testing.T) {
	small := []Node{node("srv-a", "a", 4, 4, AvailabilityAlwaysOn)}
	big := demand("photos", 2.375)
	big.HostFloor.RAMGB = 6
	result := plan(t, small, []Demand{big}, nil)
	if result.Outcome != OutcomeProvenInfeasible || len(result.Conflicts) != 1 || result.Conflicts[0].Reasons[0].Code != "needs_more_ram" {
		t.Fatalf("got %s %+v", result.Outcome, result.Conflicts)
	}
	unreported := node("srv-b", "b", 0, 0, AvailabilityUnknown)
	result = plan(t, append(small, unreported), []Demand{big}, nil)
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("an unreported node must leave the verdict unknown, got %s", result.Outcome)
	}
}

// The repack finds a layout the greedy pass misses.
func TestRepackFindsLayoutGreedyMisses(t *testing.T) {
	nodes := []Node{node("srv-a", "a", 8, 10, AvailabilityAlwaysOn), node("srv-b", "b", 8, 10, AvailabilityAlwaysOn)}
	var demands []Demand
	for _, spec := range []struct {
		name string
		ram  float64
	}{{"files", 5}, {"media", 4}, {"photos", 4}, {"remote", 3}, {"documents", 2}, {"dev", 2}} {
		d := demand(spec.name, spec.ram)
		d.HostFloor = Floor{}
		demands = append(demands, d)
	}
	result := plan(t, nodes, demands, nil)
	if result.Outcome != OutcomeFeasible {
		t.Fatalf("outcome %s, want feasible after repack (5+3+2 / 4+4+2)", result.Outcome)
	}
}

// Existing managed components count with their reservation and are never
// moved.
func TestExistingComponentsStayAndCount(t *testing.T) {
	nodes := []Node{node("srv-a", "a", 4, 6, AvailabilityAlwaysOn), node("srv-b", "b", 4, 8, AvailabilityAlwaysOn)}
	existing := []Component{{ID: "files", Workload: "files", NodeID: "srv-a", Management: ManagementManaged, Reservation: Resources{RAMGB: 5}}}
	result := plan(t, nodes, []Demand{demand("photos", 2.375)}, existing)
	if got := assignedNode(result, "photos"); got != "srv-b" {
		t.Fatalf("photos on %q, want srv-b (srv-a has 1 GB left)", got)
	}
}
