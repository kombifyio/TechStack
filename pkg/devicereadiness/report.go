package devicereadiness

import "time"

// Check is one question asked of the device, answered from measured facts.
type Check struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	// Summary states what was measured, in the operator's words. Nothing
	// asserts on its text.
	Summary string `json:"summary"`
	// Detail carries the measurement itself when it helps: the interface that
	// has no driver, the name that did not resolve.
	Detail string `json:"detail,omitempty"`
}

// Readiness is the report's single verdict about what can happen next.
type Readiness string

const (
	// ReadinessReady means enrollment can proceed without touching the device.
	ReadinessReady Readiness = "ready"
	// ReadinessRepairable means enrollment cannot proceed yet, and the catalog
	// offers at least one fix that this executor can carry out.
	ReadinessRepairable Readiness = "repairable"
	// ReadinessNeedsOwner means what is wrong is outside the executor's
	// authority: a credential, a reboot, a decision, or a physical act.
	ReadinessNeedsOwner Readiness = "needs_owner"
	// ReadinessSubstrate means the device is a hypervisor node. It is not
	// repaired into a host; it is registered as a substrate.
	ReadinessSubstrate Readiness = "substrate"
)

// Report is the machine-readable outcome of one readiness run. It is the shape
// the HTTP route returns, the CLI prints, and the creation flow renders.
type Report struct {
	SchemaVersion string    `json:"schemaVersion"`
	EvaluatedAt   time.Time `json:"evaluatedAt"`
	Readiness     Readiness `json:"readiness"`
	Status        Status    `json:"status"`
	Checks        []Check   `json:"checks"`
	// Resolutions are the catalog entries that answer what this report found,
	// in the order they should be offered.
	Resolutions []Resolution `json:"resolutions,omitempty"`
	Facts       Facts        `json:"facts"`
}

// Blocking returns the checks that stop enrollment.
func (r Report) Blocking() []Check {
	blocking := make([]Check, 0, len(r.Checks))
	for _, check := range r.Checks {
		if check.Status == StatusBlocked {
			blocking = append(blocking, check)
		}
	}
	return blocking
}

// statusRank orders outcomes from most to least severe so a report takes the
// worst measured check.
func statusRank(status Status) int {
	switch status {
	case StatusBlocked:
		return 4
	case StatusUnknown:
		return 3
	case StatusWarning:
		return 2
	case StatusPass:
		return 1
	default:
		return 0
	}
}

func worstStatus(checks []Check) Status {
	worst := StatusPass
	for _, check := range checks {
		if statusRank(check.Status) > statusRank(worst) {
			worst = check.Status
		}
	}
	return worst
}
