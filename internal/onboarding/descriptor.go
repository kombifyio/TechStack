// Package onboarding is Techstack's half of ONBOARDING-JOURNEY-STANDARD: the
// journey descriptor it owns, the server-side state transitions, the derived
// completion signals and the availability resolution. The render model is
// NOT computed here — GET hands the client the raw descriptor, state and
// availability, and @kombiverselabs/onboarding-core resolves them, so there
// is exactly one implementation of what the user sees.
package onboarding

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed journeys/*.json
var journeyFS embed.FS

// Completion is a step's completion rule: derived from a server predicate, or
// reported by the client (never for a cost-bearing step).
type Completion struct {
	Kind        string `json:"kind"`
	PredicateID string `json:"predicate_id,omitempty"`
}

// Step mirrors onboarding-journey.v1 verbatim; the field names ARE the wire.
type Step struct {
	ID                 string     `json:"id"`
	MessageID          string     `json:"message_id"`
	BodyMessageID      string     `json:"body_message_id"`
	CTAMessageID       string     `json:"cta_message_id"`
	Route              string     `json:"route"`
	Surface            []string   `json:"surface"`
	Order              int        `json:"order"`
	Optional           bool       `json:"optional"`
	Completion         Completion `json:"completion"`
	RequiredCapability *string    `json:"required_capability"`
	CostBearing        bool       `json:"cost_bearing"`
	CoachAnchor        *string    `json:"coach_anchor"`
	AnalyticsID        string     `json:"analytics_id"`
}

// Journey is the static descriptor (onboarding-journey.v1).
type Journey struct {
	Version             string   `json:"version"`
	JourneyID           string   `json:"journey_id"`
	JourneyVersion      int      `json:"journey_version"`
	Stage               string   `json:"stage"`
	Product             string   `json:"product"`
	Modes               []string `json:"modes"`
	ReopenOnVersionBump bool     `json:"reopen_on_version_bump"`
	Steps               []Step   `json:"steps"`
	// Raw is the embedded document, served verbatim so the client resolves
	// exactly what the gate validated.
	Raw json.RawMessage `json:"-"`
}

// Step returns the step with the given id.
func (j Journey) Step(id string) (Step, bool) {
	for _, step := range j.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return Step{}, false
}

var journeys = loadJourneys()

func loadJourneys() map[string]Journey {
	out := map[string]Journey{}
	entries, err := fs.ReadDir(journeyFS, "journeys")
	if err != nil {
		panic(fmt.Sprintf("onboarding: read journeys: %v", err))
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := journeyFS.ReadFile("journeys/" + entry.Name())
		if err != nil {
			panic(fmt.Sprintf("onboarding: read %s: %v", entry.Name(), err))
		}
		var journey Journey
		if err := json.Unmarshal(raw, &journey); err != nil {
			panic(fmt.Sprintf("onboarding: decode %s: %v", entry.Name(), err))
		}
		journey.Raw = json.RawMessage(raw)
		if _, dup := out[journey.JourneyID]; dup {
			panic(fmt.Sprintf("onboarding: duplicate journey id %q", journey.JourneyID))
		}
		seen := map[string]bool{}
		for _, step := range journey.Steps {
			if seen[step.ID] {
				panic(fmt.Sprintf("onboarding: journey %q declares step %q twice", journey.JourneyID, step.ID))
			}
			seen[step.ID] = true
			if step.CostBearing && step.Completion.Kind == completionSourceReported {
				panic(fmt.Sprintf("onboarding: step %q is cost-bearing and reported (standard §3)", step.ID))
			}
		}
		sort.SliceStable(journey.Steps, func(a, b int) bool { return journey.Steps[a].Order < journey.Steps[b].Order })
		out[journey.JourneyID] = journey
	}
	return out
}

func getJourney(journeyID string) (Journey, bool) {
	journey, ok := journeys[strings.TrimSpace(journeyID)]
	return journey, ok
}
