// Package availability folds the append-only server transition timeline into
// uptime, downtime episodes and daily state buckets.
//
// It is deliberately a projection over recorded transitions rather than a
// sampler: a transition row is a fact the aggregate boundary committed, so an
// episode derived from two of them is evidence, not an estimate. Nothing here
// touches a database or a clock beyond the window it is handed.
package availability

import (
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// Class is the availability accounting class of one connection state.
//
// The split is conservative on purpose. `stale` means the agent stopped
// reporting: we do not know that the node is down, we only know we can no
// longer say it is up. For an availability figure the honest reading of
// "cannot confirm service" is downtime - and the surface still renders stale
// as its own state, so the distinction is never lost.
//
// `pending` and `connecting` are neither: a node that has not connected yet is
// not having an outage. Time in those states leaves the denominator entirely,
// so a node enrolled two days ago does not report 6 % availability over a
// 30-day window.
type Class int

const (
	// ClassUnobserved carries no availability signal and is excluded from both
	// the numerator and the denominator.
	ClassUnobserved Class = iota
	// ClassUp is confirmed service.
	ClassUp
	// ClassDown is absent or unconfirmable service.
	ClassDown
)

// Classify maps a connection state onto its availability class. An unknown
// state is unobserved rather than up: a vocabulary we do not recognise must
// never silently improve the number.
func Classify(state string) Class {
	switch serverregistry.ConnectionState(strings.ToLower(strings.TrimSpace(state))) {
	case serverregistry.ConnectionConnected, serverregistry.ConnectionDegraded:
		return ClassUp
	case serverregistry.ConnectionStale, serverregistry.ConnectionOffline, serverregistry.ConnectionRevoked:
		return ClassDown
	default:
		return ClassUnobserved
	}
}

// severity orders states for the daily bucket, where the worst state observed
// in a day is the one the day shows. A day that was briefly offline is an
// offline day; anything else lets an outage hide behind the hours around it.
func severity(state string) int {
	switch serverregistry.ConnectionState(strings.ToLower(strings.TrimSpace(state))) {
	case serverregistry.ConnectionConnected:
		return 1
	case serverregistry.ConnectionDegraded:
		return 2
	case serverregistry.ConnectionStale:
		return 3
	case serverregistry.ConnectionOffline, serverregistry.ConnectionRevoked:
		return 4
	default:
		return 0
	}
}

// Window is the interval a report covers.
type Window struct {
	Start time.Time
	End   time.Time
}

// Segment is one contiguous run of a single connection state.
type Segment struct {
	State string    `json:"state"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

// Duration of the segment.
func (s Segment) Duration() time.Duration { return s.To.Sub(s.From) }

// Episode is one maximal run of downtime, bounded by the transitions that
// entered and left it. Trigger and recovery carry the recorded reason and the
// authority that wrote it - never an inferred cause.
type Episode struct {
	ServerID       string     `json:"server_id"`
	ServerName     string     `json:"server_name,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	Seconds        int64      `json:"seconds"`
	Ongoing        bool       `json:"ongoing"`
	State          string     `json:"state"`
	TriggerReason  string     `json:"trigger_reason,omitempty"`
	TriggerSource  string     `json:"trigger_source,omitempty"`
	RecoveryReason string     `json:"recovery_reason,omitempty"`
	RecoverySource string     `json:"recovery_source,omitempty"`
	EvidenceRef    string     `json:"evidence_ref,omitempty"`
}

// DayBucket is the worst state a server was observed in on one UTC day. An
// empty state means the day carries no observation at all.
type DayBucket struct {
	Day   string `json:"day"`
	State string `json:"state"`
}

// ServerReport is one server's availability over the window.
type ServerReport struct {
	ServerID string `json:"server_id"`
	Name     string `json:"name,omitempty"`
	// UptimeRatio is up / (up + down) over observed time only. It is nil when
	// the window contains no observed time, because 0 % and "no evidence" are
	// different statements and a surface must be able to tell them apart.
	UptimeRatio     *float64    `json:"uptime_ratio"`
	ObservedSeconds int64       `json:"observed_seconds"`
	DownSeconds     int64       `json:"down_seconds"`
	Episodes        []Episode   `json:"episodes"`
	Days            []DayBucket `json:"days"`
}

// CauseTotal groups downtime by the reason code that opened each episode.
type CauseTotal struct {
	ReasonCode string `json:"reason_code"`
	Seconds    int64  `json:"seconds"`
	Episodes   int    `json:"episodes"`
}

// FleetReport is the whole tenant over the window.
type FleetReport struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	UptimeRatio     *float64  `json:"uptime_ratio"`
	ObservedSeconds int64     `json:"observed_seconds"`
	DownSeconds     int64     `json:"down_seconds"`
	// MTTRSeconds is the mean duration of episodes that actually ended. An
	// ongoing outage has no recovery time yet and must not shorten the mean.
	MTTRSeconds    *int64         `json:"mttr_seconds"`
	ClosedEpisodes int            `json:"closed_episodes"`
	ByCause        []CauseTotal   `json:"by_cause"`
	Servers        []ServerReport `json:"servers"`
}

// Input is one server's raw material: its identity, when it was enrolled, the
// state it is in now, and its transitions inside the window.
type Input struct {
	ServerID     string
	Name         string
	EnrolledAt   time.Time
	CurrentState string
	Transitions  []serverregistry.Transition
}

// BuildServerReport folds one server's transitions into segments, episodes and
// daily buckets.
//
// Transitions record only changes, so the state at the window start is not in
// the window: it is the from_state of the first transition inside it, and when
// there is no transition at all the server has been in its current state for
// the whole window.
func BuildServerReport(window Window, in Input) ServerReport {
	report := ServerReport{
		ServerID: in.ServerID, Name: in.Name,
		Episodes: []Episode{}, Days: []DayBucket{},
	}
	if !window.End.After(window.Start) {
		return report
	}

	transitions := connectionTransitions(window, in.Transitions)
	segments := buildSegments(window, in, transitions)

	var up, down time.Duration
	for _, segment := range segments {
		switch Classify(segment.State) {
		case ClassUp:
			up += segment.Duration()
		case ClassDown:
			down += segment.Duration()
		case ClassUnobserved:
		}
	}
	observed := up + down
	report.ObservedSeconds = int64(observed.Seconds())
	report.DownSeconds = int64(down.Seconds())
	if observed > 0 {
		ratio := up.Seconds() / observed.Seconds()
		report.UptimeRatio = &ratio
	}

	report.Episodes = buildEpisodes(in, segments, transitions, window)
	report.Days = buildDays(window, segments)
	return report
}

// connectionTransitions keeps only the connection dimension inside the window,
// oldest first. The other dimensions answer different questions and must not
// enter an availability fold.
func connectionTransitions(window Window, all []serverregistry.Transition) []serverregistry.Transition {
	out := make([]serverregistry.Transition, 0, len(all))
	for _, transition := range all {
		if !strings.EqualFold(strings.TrimSpace(transition.Dimension), "connection") {
			continue
		}
		at := transition.ObservedAt.UTC()
		if at.Before(window.Start) || at.After(window.End) {
			continue
		}
		out = append(out, transition)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	return out
}

func buildSegments(window Window, in Input, transitions []serverregistry.Transition) []Segment {
	// A server that did not exist for part of the window contributes no
	// evidence for that part, so its first segment starts at enrolment.
	start := window.Start
	if enrolled := in.EnrolledAt.UTC(); !enrolled.IsZero() && enrolled.After(start) {
		start = enrolled
	}
	if !window.End.After(start) {
		return nil
	}

	state := strings.TrimSpace(in.CurrentState)
	if len(transitions) > 0 {
		state = strings.TrimSpace(transitions[0].FromState)
	}

	segments := make([]Segment, 0, len(transitions)+1)
	cursor := start
	for _, transition := range transitions {
		at := transition.ObservedAt.UTC()
		if at.After(cursor) {
			segments = append(segments, Segment{State: state, From: cursor, To: at})
			cursor = at
		}
		state = strings.TrimSpace(transition.ToState)
	}
	if window.End.After(cursor) {
		segments = append(segments, Segment{State: state, From: cursor, To: window.End})
	}
	return segments
}

func buildEpisodes(
	in Input,
	segments []Segment,
	transitions []serverregistry.Transition,
	window Window,
) []Episode {
	episodes := make([]Episode, 0, 4)
	var current *Episode

	// Index transitions by the instant they occurred so an episode can carry
	// the reason the aggregate actually recorded rather than a guess.
	entering := map[time.Time]serverregistry.Transition{}
	for _, transition := range transitions {
		entering[transition.ObservedAt.UTC()] = transition
	}

	for _, segment := range segments {
		if Classify(segment.State) == ClassDown {
			if current == nil {
				episode := Episode{
					ServerID: in.ServerID, ServerName: in.Name,
					StartedAt: segment.From, State: segment.State,
				}
				if transition, ok := entering[segment.From]; ok {
					episode.TriggerReason = transition.ReasonCode
					episode.TriggerSource = transition.Source
					episode.EvidenceRef = evidenceRef(transition)
				}
				current = &episode
			}
			// A run that deepens (stale, then offline) keeps the reason it
			// opened with and reports the worst state it reached.
			if severity(segment.State) > severity(current.State) {
				current.State = segment.State
			}
			continue
		}
		if current != nil {
			episodes = append(episodes, closeEpisode(*current, segment.From, entering))
			current = nil
		}
	}

	if current != nil {
		episode := *current
		episode.Ongoing = true
		episode.Seconds = int64(window.End.Sub(episode.StartedAt).Seconds())
		episodes = append(episodes, episode)
	}
	return episodes
}

func closeEpisode(
	episode Episode,
	endedAt time.Time,
	entering map[time.Time]serverregistry.Transition,
) Episode {
	ended := endedAt
	episode.EndedAt = &ended
	episode.Seconds = int64(ended.Sub(episode.StartedAt).Seconds())
	if transition, ok := entering[endedAt]; ok {
		episode.RecoveryReason = transition.ReasonCode
		episode.RecoverySource = transition.Source
	}
	return episode
}

func evidenceRef(transition serverregistry.Transition) string {
	if transition.Evidence == nil {
		return ""
	}
	ref, _ := transition.Evidence["evidence_ref"].(string)
	return ref
}

// buildDays reports the worst state observed on each UTC day of the window. A
// day with no segment stays empty rather than defaulting to healthy.
func buildDays(window Window, segments []Segment) []DayBucket {
	worst := map[string]string{}
	for _, segment := range segments {
		day := segment.From.UTC().Truncate(24 * time.Hour)
		for ; day.Before(segment.To.UTC()); day = day.Add(24 * time.Hour) {
			key := day.Format("2006-01-02")
			if severity(segment.State) > severity(worst[key]) {
				worst[key] = segment.State
			}
		}
	}

	days := make([]DayBucket, 0, 32)
	day := window.Start.UTC().Truncate(24 * time.Hour)
	for ; !day.After(window.End.UTC()); day = day.Add(24 * time.Hour) {
		key := day.Format("2006-01-02")
		days = append(days, DayBucket{Day: key, State: worst[key]})
	}
	return days
}

// BuildFleetReport aggregates per-server reports. Ratios are recomputed from
// the totals rather than averaged: averaging percentages would let a node with
// two days of history weigh as much as one with thirty.
func BuildFleetReport(window Window, inputs []Input) FleetReport {
	report := FleetReport{
		Start: window.Start.UTC(), End: window.End.UTC(),
		ByCause: []CauseTotal{}, Servers: []ServerReport{},
	}

	causes := map[string]*CauseTotal{}
	var observed, down, closedTotal int64
	closedCount := 0

	for _, in := range inputs {
		server := BuildServerReport(window, in)
		report.Servers = append(report.Servers, server)
		observed += server.ObservedSeconds
		down += server.DownSeconds

		for _, episode := range server.Episodes {
			reason := strings.TrimSpace(episode.TriggerReason)
			if reason == "" {
				reason = "unrecorded"
			}
			total, ok := causes[reason]
			if !ok {
				total = &CauseTotal{ReasonCode: reason}
				causes[reason] = total
			}
			total.Seconds += episode.Seconds
			total.Episodes++

			if !episode.Ongoing {
				closedTotal += episode.Seconds
				closedCount++
			}
		}
	}

	report.ObservedSeconds = observed
	report.DownSeconds = down
	if observed > 0 {
		ratio := float64(observed-down) / float64(observed)
		report.UptimeRatio = &ratio
	}
	if closedCount > 0 {
		mttr := closedTotal / int64(closedCount)
		report.MTTRSeconds = &mttr
		report.ClosedEpisodes = closedCount
	}

	for _, total := range causes {
		report.ByCause = append(report.ByCause, *total)
	}
	sort.SliceStable(report.ByCause, func(i, j int) bool {
		if report.ByCause[i].Seconds == report.ByCause[j].Seconds {
			return report.ByCause[i].ReasonCode < report.ByCause[j].ReasonCode
		}
		return report.ByCause[i].Seconds > report.ByCause[j].Seconds
	})
	return report
}
