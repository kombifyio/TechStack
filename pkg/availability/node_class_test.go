package availability

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// nodeReport folds a server whose connection was down for the given hours of an
// otherwise connected, long-enrolled window.
func nodeReport(now time.Time, enrolledDaysAgo int, downHours ...float64) ServerReport {
	window := NodeEvidenceWindow(now)
	var transitions []serverregistry.Transition
	cursor := window.Start
	for _, hours := range downHours {
		down := cursor.Add(6 * time.Hour)
		up := down.Add(time.Duration(hours * float64(time.Hour)))
		transitions = append(transitions,
			transition("connection", "connected", "offline", "agent_silent", down),
			transition("connection", "offline", "connected", "agent_reconnected", up))
		cursor = up
	}
	return BuildServerReport(window, Input{
		ServerID: "s1", CurrentState: "connected",
		EnrolledAt:  now.Add(-time.Duration(enrolledDaysAgo) * 24 * time.Hour),
		Transitions: transitions,
	})
}

// The thresholds are the whole contract of the planning class: the Layout
// Planner never puts an important workload on a node that is not always-on, so
// a class stated on thin or ambiguous evidence would be an outage waiting.
func TestClassifyNodeThresholds(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		report ServerReport
		want   string
	}{
		"fully up over the window":                 {nodeReport(now, 30), NodeAlwaysOn},
		"two short outages stay above 97 percent":  {nodeReport(now, 30, 2, 2), NodeAlwaysOn},
		"switched off every night is part-time":    {nodeReport(now, 30, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8), NodePartTime},
		"ninety-three percent is neither class":    {nodeReport(now, 30, 12, 12), NodeUnknown},
		"enrolled two days ago has too little":     {nodeReport(now, 2), NodeUnknown},
		"exactly three observed days is enough":    {nodeReport(now, 3), NodeAlwaysOn},
		"no observation at all":                    {ServerReport{}, NodeUnknown},
		"part-time evidence is also gated by time": {nodeReport(now, 2, 10), NodeUnknown},
	} {
		if got := ClassifyNode(tc.report); got != tc.want {
			ratio := -1.0
			if tc.report.UptimeRatio != nil {
				ratio = *tc.report.UptimeRatio
			}
			t.Errorf("%s: class = %q, want %q (uptime %.3f over %d s)", name, got, tc.want, ratio, tc.report.ObservedSeconds)
		}
	}
}

// A node that never connected (pending) is not having an outage, so the
// planner must hear "unknown" and not "part-time".
func TestClassifyNodeIgnoresPendingTime(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	report := BuildServerReport(NodeEvidenceWindow(now), Input{
		ServerID: "s1", CurrentState: "pending", EnrolledAt: now.Add(-20 * 24 * time.Hour),
	})
	if got := ClassifyNode(report); got != NodeUnknown {
		t.Fatalf("class = %q, want unknown for a never-connected node", got)
	}
}
