package availability

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func at(day, hour int) time.Time {
	return time.Date(2026, 8, day, hour, 0, 0, 0, time.UTC)
}

func window() Window {
	return Window{Start: at(1, 0), End: at(11, 0)}
}

func transition(dimension, from, to, reason string, when time.Time) serverregistry.Transition {
	return serverregistry.Transition{
		Dimension: dimension, FromState: from, ToState: to,
		ReasonCode: reason, Source: "sweeper", ObservedAt: when,
	}
}

// TestNoTransitionsMeansTheCurrentStateHeldAllWindow pins the rule that makes
// the whole projection work: transitions record changes, so silence is not
// absence of evidence - it means nothing changed.
func TestNoTransitionsMeansTheCurrentStateHeldAllWindow(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
	})

	if report.UptimeRatio == nil || *report.UptimeRatio != 1 {
		t.Fatalf("uptime = %v, want 1", report.UptimeRatio)
	}
	if report.DownSeconds != 0 || len(report.Episodes) != 0 {
		t.Fatalf("a quiet connected node reported downtime: %#v", report)
	}
	if got := int64((10 * 24 * time.Hour).Seconds()); report.ObservedSeconds != got {
		t.Fatalf("observed = %d, want %d", report.ObservedSeconds, got)
	}
}

// TestStateBeforeTheWindowComesFromTheFirstTransition proves the projection
// does not assume the current state applied retroactively: a node that is
// connected now but was offline for the first half of the window must report
// that outage.
func TestStateBeforeTheWindowComesFromTheFirstTransition(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		Transitions: []serverregistry.Transition{
			transition("connection", "offline", "connected", "agent_reconnected", at(6, 0)),
		},
	})

	if len(report.Episodes) != 1 {
		t.Fatalf("episodes = %#v, want the pre-window outage", report.Episodes)
	}
	episode := report.Episodes[0]
	if !episode.StartedAt.Equal(at(1, 0)) || episode.EndedAt == nil || !episode.EndedAt.Equal(at(6, 0)) {
		t.Fatalf("episode window = %v..%v", episode.StartedAt, episode.EndedAt)
	}
	if episode.RecoveryReason != "agent_reconnected" {
		t.Fatalf("recovery reason = %q", episode.RecoveryReason)
	}
	if want := int64((5 * 24 * time.Hour).Seconds()); episode.Seconds != want {
		t.Fatalf("episode seconds = %d, want %d", episode.Seconds, want)
	}
}

// TestDeepeningOutageStaysOneEpisode is the behaviour an operator expects: an
// agent going stale and then offline is one outage with one cause, not two.
func TestDeepeningOutageStaysOneEpisode(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		Transitions: []serverregistry.Transition{
			transition("connection", "connected", "stale", "heartbeat_window_exceeded", at(3, 0)),
			transition("connection", "stale", "offline", "sweeper_demoted", at(3, 2)),
			transition("connection", "offline", "connected", "agent_reconnected", at(4, 0)),
		},
	})

	if len(report.Episodes) != 1 {
		t.Fatalf("episodes = %#v, want one", report.Episodes)
	}
	episode := report.Episodes[0]
	if episode.TriggerReason != "heartbeat_window_exceeded" {
		t.Fatalf("trigger = %q, want the reason the outage opened with", episode.TriggerReason)
	}
	if episode.State != "offline" {
		t.Fatalf("state = %q, want the worst state reached", episode.State)
	}
	if !episode.StartedAt.Equal(at(3, 0)) {
		t.Fatalf("started = %v, want the stale transition", episode.StartedAt)
	}
}

// TestOngoingOutageHasNoEndAndNoRecovery keeps an unresolved outage honest: it
// must not borrow the window end as a recovery time.
func TestOngoingOutageHasNoEndAndNoRecovery(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "offline",
		Transitions: []serverregistry.Transition{
			transition("connection", "connected", "offline", "agent_channel_closed", at(9, 0)),
		},
	})

	if len(report.Episodes) != 1 {
		t.Fatalf("episodes = %#v", report.Episodes)
	}
	episode := report.Episodes[0]
	if !episode.Ongoing || episode.EndedAt != nil || episode.RecoveryReason != "" {
		t.Fatalf("ongoing outage claimed a recovery: %#v", episode)
	}
}

// TestEnrolmentTrimsTheDenominator stops a freshly enrolled node from being
// scored on time it did not exist for.
func TestEnrolmentTrimsTheDenominator(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		EnrolledAt: at(9, 0),
	})

	if want := int64((2 * 24 * time.Hour).Seconds()); report.ObservedSeconds != want {
		t.Fatalf("observed = %d, want only the enrolled span %d", report.ObservedSeconds, want)
	}
	if report.UptimeRatio == nil || *report.UptimeRatio != 1 {
		t.Fatalf("uptime = %v, want 1", report.UptimeRatio)
	}
	// Days before enrolment carry no observation and must stay empty rather
	// than defaulting to a healthy-looking state.
	if report.Days[0].State != "" {
		t.Fatalf("pre-enrolment day = %q, want no data", report.Days[0].State)
	}
}

// TestPendingIsNotDowntime separates "never connected" from "outage" - the
// distinction that keeps a provisioning node out of the downtime budget.
func TestPendingIsNotDowntime(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		Transitions: []serverregistry.Transition{
			transition("connection", "pending", "connected", "agent_enrolled", at(5, 0)),
		},
	})

	if report.DownSeconds != 0 || len(report.Episodes) != 0 {
		t.Fatalf("pending time was charged as downtime: %#v", report)
	}
	if want := int64((6 * 24 * time.Hour).Seconds()); report.ObservedSeconds != want {
		t.Fatalf("observed = %d, want %d - pending must leave the denominator", report.ObservedSeconds, want)
	}
}

// TestOnlyTheConnectionDimensionCounts keeps the axes from being conflated: a
// health change is not an availability event.
func TestOnlyTheConnectionDimensionCounts(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		Transitions: []serverregistry.Transition{
			transition("health", "healthy", "unhealthy", "probe_failed", at(4, 0)),
			transition("lifecycle", "active", "decommissioning", "owner_requested", at(5, 0)),
		},
	})

	if report.UptimeRatio == nil || *report.UptimeRatio != 1 {
		t.Fatalf("uptime = %v - a health change became downtime", report.UptimeRatio)
	}
}

// TestWorstStateWinsTheDay is what makes the ribbon trustworthy: a brief
// outage must not be hidden by the healthy hours around it.
func TestWorstStateWinsTheDay(t *testing.T) {
	report := BuildServerReport(window(), Input{
		ServerID: "server-1", CurrentState: "connected",
		Transitions: []serverregistry.Transition{
			transition("connection", "connected", "offline", "agent_channel_closed", at(4, 10)),
			transition("connection", "offline", "connected", "agent_reconnected", at(4, 11)),
		},
	})

	var day string
	for _, bucket := range report.Days {
		if bucket.Day == "2026-08-04" {
			day = bucket.State
		}
	}
	if day != "offline" {
		t.Fatalf("2026-08-04 = %q, want offline - a one-hour outage was hidden", day)
	}
}

// TestFleetRatioIsRecomputedNotAveraged stops a two-day-old node from weighing
// the same as a thirty-day-old one.
func TestFleetRatioIsRecomputedNotAveraged(t *testing.T) {
	report := BuildFleetReport(window(), []Input{
		{ServerID: "long-lived", CurrentState: "connected"},
		{
			ServerID: "fresh", CurrentState: "offline", EnrolledAt: at(10, 0),
			Transitions: []serverregistry.Transition{
				transition("connection", "connected", "offline", "power_loss", at(10, 12)),
			},
		},
	})

	// Averaging the two ratios would give ~75 %; weighting by observed time
	// gives ~95 %, which is the true fleet figure.
	if report.UptimeRatio == nil {
		t.Fatal("fleet ratio missing")
	}
	if *report.UptimeRatio < 0.94 || *report.UptimeRatio > 0.96 {
		t.Fatalf("fleet uptime = %.4f, want the time-weighted ~0.95", *report.UptimeRatio)
	}
}

// TestMTTRIgnoresOngoingOutages keeps the recovery mean from being flattered by
// an outage that has not recovered.
func TestMTTRIgnoresOngoingOutages(t *testing.T) {
	report := BuildFleetReport(window(), []Input{
		{
			ServerID: "server-1", CurrentState: "offline",
			Transitions: []serverregistry.Transition{
				transition("connection", "connected", "offline", "power_loss", at(2, 0)),
				transition("connection", "offline", "connected", "agent_reconnected", at(2, 2)),
				transition("connection", "connected", "offline", "agent_channel_closed", at(9, 0)),
			},
		},
	})

	if report.ClosedEpisodes != 1 {
		t.Fatalf("closed episodes = %d, want 1", report.ClosedEpisodes)
	}
	if report.MTTRSeconds == nil || *report.MTTRSeconds != int64((2 * time.Hour).Seconds()) {
		t.Fatalf("mttr = %v, want the closed episode only", report.MTTRSeconds)
	}
}

// TestNoObservationYieldsNoRatio keeps "we have no evidence" distinguishable
// from "zero percent".
func TestNoObservationYieldsNoRatio(t *testing.T) {
	report := BuildServerReport(window(), Input{ServerID: "server-1", CurrentState: "pending"})
	if report.UptimeRatio != nil {
		t.Fatalf("uptime = %v, want nil for a never-observed node", *report.UptimeRatio)
	}
	if report.ObservedSeconds != 0 {
		t.Fatalf("observed = %d, want 0", report.ObservedSeconds)
	}
}

// TestUnknownStateIsNotCountedAsUp is the fail-visible guard: a vocabulary the
// projection does not recognise must never improve the number.
func TestUnknownStateIsNotCountedAsUp(t *testing.T) {
	if Classify("quiescing") != ClassUnobserved {
		t.Fatal("an unknown connection state was classified as up or down")
	}
	report := BuildServerReport(window(), Input{ServerID: "server-1", CurrentState: "quiescing"})
	if report.UptimeRatio != nil {
		t.Fatalf("uptime = %v, want nil", *report.UptimeRatio)
	}
}

// TestDowntimeGroupsByRecordedCause proves the cause breakdown reports what
// the aggregate wrote, and names unrecorded causes instead of dropping them.
func TestDowntimeGroupsByRecordedCause(t *testing.T) {
	report := BuildFleetReport(window(), []Input{
		{
			ServerID: "server-1", CurrentState: "connected",
			Transitions: []serverregistry.Transition{
				transition("connection", "connected", "offline", "power_loss", at(2, 0)),
				transition("connection", "offline", "connected", "agent_reconnected", at(2, 4)),
				transition("connection", "connected", "offline", "", at(5, 0)),
				transition("connection", "offline", "connected", "agent_reconnected", at(5, 1)),
			},
		},
	})

	if len(report.ByCause) != 2 {
		t.Fatalf("causes = %#v", report.ByCause)
	}
	if report.ByCause[0].ReasonCode != "power_loss" {
		t.Fatalf("largest cause = %q, want power_loss", report.ByCause[0].ReasonCode)
	}
	if report.ByCause[1].ReasonCode != "unrecorded" {
		t.Fatalf("missing reason = %q, want it named rather than dropped", report.ByCause[1].ReasonCode)
	}
}
