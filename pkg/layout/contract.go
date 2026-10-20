// Package layout is the Unifier's Layout Planner: the versioned contracts for
// the homelab view a layout is computed from (Snapshot), the question asked
// (Problem) and the answer (Result), the planner port, and the deterministic
// best-fit baseline with household priority bands.
//
// StackKits owns the technical facts (host floors, reservations, accelerators,
// measured provenance) and verifies a shown option against exact inventory.
// Household policy (importance, bands) is Techstack's and lives here. Nothing
// in this package executes anything: a Result is advice until the user
// accepts a Recommendation (owner decision D2, 2026-10-19).
package layout

import "time"

// Contract versions. A change to the meaning of a field is a new version.
const (
	SnapshotAPIVersion = "techstack.kombify.io/layout-snapshot/v1"
	ProblemAPIVersion  = "techstack.kombify.io/layout-problem/v1"
	ResultAPIVersion   = "techstack.kombify.io/layout-result/v1"

	KindSnapshot = "LayoutSnapshot"
	KindProblem  = "LayoutProblem"
	KindResult   = "LayoutResult"
)

// Node states.
const (
	NodeConnected = "connected"
	NodePlanned   = "planned"
	NodeOffline   = "offline"
)

// Trust classes (owner decision D1): only owner-controlled nodes exist today.
// Member-controlled nodes are modeled so a contribution policy can admit them
// later without a contract change; the baseline never places on them.
const (
	TrustOwnerControlled  = "owner-controlled"
	TrustMemberControlled = "member-controlled"
)

// Availability classes, observed from the server transition history and
// optionally confirmed by the owner.
const (
	AvailabilityAlwaysOn = "always-on"
	AvailabilityPartTime = "part-time"
	AvailabilityUnknown  = "unknown"
)

// Management states of an existing component (the registry separates
// managed from observed services).
const (
	ManagementManaged  = "managed"
	ManagementObserved = "observed"
)

// Resources is a CPU, RAM and storage vector in cores and GB.
type Resources struct {
	CPUCores  float64 `json:"cpuCores"`
	RAMGB     float64 `json:"ramGB"`
	StorageGB float64 `json:"storageGB"`
}

// Add returns r + other.
func (r Resources) Add(other Resources) Resources {
	return Resources{CPUCores: r.CPUCores + other.CPUCores, RAMGB: r.RAMGB + other.RAMGB, StorageGB: r.StorageGB + other.StorageGB}
}

// Max returns the per-axis maximum.
func (r Resources) Max(other Resources) Resources {
	return Resources{CPUCores: maxFloat(r.CPUCores, other.CPUCores), RAMGB: maxFloat(r.RAMGB, other.RAMGB), StorageGB: maxFloat(r.StorageGB, other.StorageGB)}
}

// Fact is a hardware class with its provenance.
type Fact struct {
	Class  string `json:"class"`
	Source string `json:"source"`
}

// Accelerator is an observed GPU.
type Accelerator struct {
	Vendor      string `json:"vendor"`
	DriverMajor int    `json:"driverMajor,omitempty"`
}

// Availability is a node availability fact with provenance.
type Availability struct {
	Class       string   `json:"class"`
	Source      string   `json:"source"`
	UptimeRatio *float64 `json:"uptimeRatio,omitempty"`
	Confirmed   bool     `json:"confirmed,omitempty"`
}

// Node is one owner node in the snapshot. ID is the canonical runtime
// inventory server id, stable across re-enrollment; EnrollmentGeneration
// changes when the node is re-enrolled.
type Node struct {
	ID                   string `json:"id"`
	SpecNodeRef          string `json:"specNodeRef,omitempty"`
	Name                 string `json:"name"`
	EnrollmentGeneration int64  `json:"enrollmentGeneration"`
	// Operator is the custody subject (the owner). Contributor is reserved
	// for member-contributed nodes and stays empty under D1.
	Operator    string `json:"operator"`
	Contributor string `json:"contributor,omitempty"`
	TrustClass  string `json:"trustClass"`
	State       string `json:"state"`
	// Primary marks the kit deployment's controller node.
	Primary bool `json:"primary,omitempty"`
	// Roles are the StackSpec node roles (controller, worker, storage, edge)
	// when the node belongs to a kit deployment.
	Roles []string `json:"roles,omitempty"`
	// Capacity is the whole host; CapacitySource says where it came from
	// ("inventory" for a Guard report, "offering" for a planned size).
	Capacity       Resources `json:"capacity"`
	CapacitySource string    `json:"capacitySource"`
	// Usage is the observed total consumption; nil when never observed.
	Usage        *Resources   `json:"usage,omitempty"`
	Arch         string       `json:"arch,omitempty"`
	AMD64Level   int          `json:"amd64Level,omitempty"`
	Accelerator  *Accelerator `json:"accelerator,omitempty"`
	Media        *Fact        `json:"media,omitempty"`
	DiskClass    *Fact        `json:"diskClass,omitempty"`
	PowerClass   *Fact        `json:"powerClass,omitempty"`
	Availability Availability `json:"availability"`
	// Missing names observations the planner had to do without.
	Missing []string `json:"missingObservations,omitempty"`
	// ObservedAt is freshness evidence and stays outside the digest.
	ObservedAt time.Time `json:"observedAt,omitempty"`
}

// Component is an existing workload on a node. Managed components count with
// their reservation; observed consumption is already part of Node.Usage, so
// the two are never added for one component.
type Component struct {
	ID          string    `json:"id"`
	Workload    string    `json:"workload"`
	Modules     []string  `json:"modules,omitempty"`
	NodeID      string    `json:"nodeId"`
	Management  string    `json:"management"`
	Reservation Resources `json:"reservation"`
	Band        string    `json:"band,omitempty"`
	// Assumed is true when the node binding was inferred (for example the
	// primary node of a single-node deployment) rather than declared.
	Assumed bool `json:"assumed,omitempty"`
}

// Snapshot is the canonical, digest-addressed view of the homelab a layout is
// computed from.
type Snapshot struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Scope binds the snapshot to a household context, for example
	// "stack:<id>" for an existing deployment or "household" for a first run.
	Scope      string      `json:"scope"`
	StackID    string      `json:"stackId,omitempty"`
	Nodes      []Node      `json:"nodes"`
	Components []Component `json:"components,omitempty"`
	// InFlight lists operations that may change the inventory while the
	// layout is computed (a running rollout, a pending pairing).
	InFlight            []string `json:"inFlight,omitempty"`
	MissingObservations []string `json:"missingObservations,omitempty"`
	Source              string   `json:"source"`
	Digest              string   `json:"digest"`
}

// Floor is a whole-host requirement.
type Floor struct {
	CPUCores   float64 `json:"cpuCores,omitempty"`
	RAMGB      float64 `json:"ramGB,omitempty"`
	StorageGB  float64 `json:"storageGB,omitempty"`
	AMD64Level int     `json:"amd64Level,omitempty"`
}

// AcceleratorDemand is a required GPU.
type AcceleratorDemand struct {
	Vendor         string `json:"vendor"`
	MinDriverMajor int    `json:"minDriverMajor,omitempty"`
}

// Demand is one selected workload to place. Its modules land together in L0;
// component splits with typed edges arrive in L1.
type Demand struct {
	Workload       string             `json:"workload"`
	Title          string             `json:"title,omitempty"`
	Goal           string             `json:"goal"`
	Alternative    string             `json:"alternative,omitempty"`
	Modules        []string           `json:"modules"`
	ComputeProfile string             `json:"computeProfile"`
	HostFloor      Floor              `json:"hostFloor"`
	Reservation    Resources          `json:"reservation"`
	Accelerator    *AcceleratorDemand `json:"accelerator,omitempty"`
	// Provenance is "measured" only when every module reservation is measured.
	Provenance string `json:"provenance"`
	// CPUUnspecified marks a release profile without a CPU reservation.
	CPUUnspecified bool `json:"cpuReservationUnspecified,omitempty"`
	// Band is the household priority band from the policy profile.
	Band string `json:"band"`
	// RequiresRoles are the node roles the release-authored placement asks for.
	RequiresRoles []string `json:"requiresRoles,omitempty"`
}

// Pin binds a workload to a node.
type Pin struct {
	Workload string `json:"workload"`
	NodeID   string `json:"nodeId"`
	Reason   string `json:"reason"`
}

// Problem is the question a planner answers.
type Problem struct {
	APIVersion     string   `json:"apiVersion"`
	Kind           string   `json:"kind"`
	SnapshotDigest string   `json:"snapshotDigest"`
	CatalogRelease string   `json:"catalogRelease"`
	CatalogDigest  string   `json:"catalogDigest,omitempty"`
	UseCases       []string `json:"useCases"`
	ComputeProfile string   `json:"computeProfile"`
	Demands        []Demand `json:"demands"`
	Pins           []Pin    `json:"pins,omitempty"`
	PolicyProfile  string   `json:"policyProfile"`
	// WorkBudget bounds the search in node visits, never in wall-clock time.
	WorkBudget int    `json:"workBudget"`
	Digest     string `json:"digest"`
}

// Outcome is the honest planner verdict. Only proven-infeasible carries
// conflicts; unknown and budget-exhausted never read as "does not fit".
type Outcome string

const (
	OutcomeFeasible         Outcome = "feasible"
	OutcomeProvenInfeasible Outcome = "proven-infeasible"
	OutcomeUnknown          Outcome = "unknown"
	OutcomeBudgetExhausted  Outcome = "budget-exhausted"
)

// Reason is one trace fact. Code and Params are the contract; Text is the
// deterministic English rendering of the same fact.
type Reason struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// Assignment places one workload on one node.
type Assignment struct {
	Workload    string    `json:"workload"`
	Goal        string    `json:"goal"`
	Modules     []string  `json:"modules"`
	NodeID      string    `json:"nodeId"`
	SpecNodeRef string    `json:"specNodeRef,omitempty"`
	Band        string    `json:"band"`
	Provenance  string    `json:"provenance"`
	Reservation Resources `json:"reservation"`
	Reasons     []Reason  `json:"reasons"`
}

// NodeLoad is the reserved load of one node in an option.
type NodeLoad struct {
	NodeID    string    `json:"nodeId"`
	Capacity  Resources `json:"capacity"`
	Reserved  Resources `json:"reserved"`
	Workloads []string  `json:"workloads"`
}

// Option is one complete layout. Digest is the proposal digest a
// recommendation is accepted under.
type Option struct {
	ID          string       `json:"id"`
	Assignments []Assignment `json:"assignments"`
	Loads       []NodeLoad   `json:"loads"`
	Digest      string       `json:"digest"`
}

// Conflict is a sufficient (not claimed minimal) reason why a workload cannot
// be placed.
type Conflict struct {
	Workload string   `json:"workload"`
	Reasons  []Reason `json:"reasons"`
}

// PlannerIdentity names the engine and its version.
type PlannerIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Result is a planner answer.
type Result struct {
	APIVersion     string          `json:"apiVersion"`
	Kind           string          `json:"kind"`
	SnapshotDigest string          `json:"snapshotDigest"`
	ProblemDigest  string          `json:"problemDigest"`
	Planner        PlannerIdentity `json:"planner"`
	Outcome        Outcome         `json:"outcome"`
	// Options holds at most one recommendation in L0; alternatives arrive
	// in L1.
	Options   []Option   `json:"options"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
	// Notes are facts about evidence the result depends on (an excluded
	// node, unverified capacity).
	Notes  []Reason `json:"notes,omitempty"`
	Work   int      `json:"work"`
	Digest string   `json:"digest"`
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
