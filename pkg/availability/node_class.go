package availability

import "time"

// The planning view of a node's availability. The Layout Planner needs one
// honest word per node: a node that is on all the time can host an important
// workload, one that is switched off most evenings cannot. The word is derived
// from the same transition fold as the uptime report and says "unknown" in
// every case the evidence does not settle, never a guess.
const (
	// NodeAlwaysOn is a node whose observed uptime met NodeAlwaysOnMinUptime.
	NodeAlwaysOn = "always-on"
	// NodePartTime is a node whose observed uptime stayed under
	// NodePartTimeMaxUptime.
	NodePartTime = "part-time"
	// NodeUnknown means too little observed time, or an uptime between the two
	// thresholds that neither class describes.
	NodeUnknown = "unknown"

	// NodeEvidenceWindowDays is the window the planning class is folded over.
	NodeEvidenceWindowDays = 14
	// NodeMinObservedDays is how much observed time (connected or down, not
	// pending) must lie inside the window before a class is stated.
	NodeMinObservedDays = 3
	// NodeAlwaysOnMinUptime and NodePartTimeMaxUptime bound the two classes.
	// The band between them stays unknown on purpose: a node at 93 % uptime
	// may be a flaky always-on host or a part-time one, and only the owner
	// knows which.
	NodeAlwaysOnMinUptime = 0.97
	NodePartTimeMaxUptime = 0.90
)

// NodeEvidenceWindow is the window ending at now that the planning class reads.
func NodeEvidenceWindow(now time.Time) Window {
	end := now.UTC()
	return Window{Start: end.Add(-NodeEvidenceWindowDays * 24 * time.Hour), End: end}
}

// ClassifyNode maps one server's availability report onto the planning class.
// The report must have been built over NodeEvidenceWindow.
func ClassifyNode(report ServerReport) string {
	if report.UptimeRatio == nil ||
		report.ObservedSeconds < int64(NodeMinObservedDays*24*time.Hour/time.Second) {
		return NodeUnknown
	}
	switch ratio := *report.UptimeRatio; {
	case ratio >= NodeAlwaysOnMinUptime:
		return NodeAlwaysOn
	case ratio < NodePartTimeMaxUptime:
		return NodePartTime
	default:
		return NodeUnknown
	}
}
