package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// SealSnapshot normalizes the snapshot (stable order, version fields) and
// sets its semantic digest. Observation timestamps stay outside the digest:
// the same homelab observed twice yields the same snapshot digest.
func SealSnapshot(snapshot Snapshot) Snapshot {
	snapshot.APIVersion, snapshot.Kind = SnapshotAPIVersion, KindSnapshot
	snapshot.Nodes = append([]Node(nil), snapshot.Nodes...)
	sort.SliceStable(snapshot.Nodes, func(i, j int) bool { return snapshot.Nodes[i].ID < snapshot.Nodes[j].ID })
	snapshot.Components = append([]Component(nil), snapshot.Components...)
	sort.SliceStable(snapshot.Components, func(i, j int) bool { return snapshot.Components[i].ID < snapshot.Components[j].ID })
	snapshot.InFlight = sortedStrings(snapshot.InFlight)
	snapshot.MissingObservations = sortedStrings(snapshot.MissingObservations)
	semantic := snapshot
	semantic.Digest = ""
	semantic.Nodes = make([]Node, len(snapshot.Nodes))
	for i, node := range snapshot.Nodes {
		node.ObservedAt, node.Address = time.Time{}, ""
		node.Missing = sortedStrings(node.Missing)
		semantic.Nodes[i] = node
	}
	snapshot.Digest = digestJSON(semantic)
	return snapshot
}

// SealProblem normalizes the problem and sets its digest.
func SealProblem(problem Problem) Problem {
	problem.APIVersion, problem.Kind = ProblemAPIVersion, KindProblem
	problem.UseCases = sortedStrings(problem.UseCases)
	problem.Demands = append([]Demand(nil), problem.Demands...)
	sort.SliceStable(problem.Demands, func(i, j int) bool { return problem.Demands[i].Workload < problem.Demands[j].Workload })
	problem.Pins = append([]Pin(nil), problem.Pins...)
	sort.SliceStable(problem.Pins, func(i, j int) bool { return problem.Pins[i].Workload < problem.Pins[j].Workload })
	semantic := problem
	semantic.Digest = ""
	problem.Digest = digestJSON(semantic)
	return problem
}

// sealOption sets the proposal digest: the assignment of workloads to nodes
// with their modules and reservations, independent of explanation wording.
func sealOption(option Option, snapshotDigest, problemDigest string) Option {
	type proposalAssignment struct {
		Workload    string    `json:"workload"`
		Modules     []string  `json:"modules"`
		NodeID      string    `json:"nodeId"`
		Reservation Resources `json:"reservation"`
	}
	proposal := struct {
		Snapshot    string               `json:"snapshot"`
		Problem     string               `json:"problem"`
		Assignments []proposalAssignment `json:"assignments"`
	}{Snapshot: snapshotDigest, Problem: problemDigest}
	for _, assignment := range option.Assignments {
		proposal.Assignments = append(proposal.Assignments, proposalAssignment{
			Workload: assignment.Workload, Modules: assignment.Modules, NodeID: assignment.NodeID, Reservation: assignment.Reservation,
		})
	}
	sort.Slice(proposal.Assignments, func(i, j int) bool { return proposal.Assignments[i].Workload < proposal.Assignments[j].Workload })
	option.Digest = digestJSON(proposal)
	return option
}

// LayoutDigest digests only the workload-to-node mapping of an option. Two
// options with equal layout digests place every workload on the same node,
// whatever snapshot they were computed from; acceptance compares this.
func LayoutDigest(option Option) string {
	pairs := make([]string, 0, len(option.Assignments))
	for _, assignment := range option.Assignments {
		pairs = append(pairs, assignment.Workload+"\x00"+assignment.NodeID)
	}
	sort.Strings(pairs)
	return digestJSON(pairs)
}

func sealResult(result Result) Result {
	result.APIVersion, result.Kind = ResultAPIVersion, KindResult
	semantic := result
	semantic.Digest = ""
	result.Digest = digestJSON(semantic)
	return result
}

func digestJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
