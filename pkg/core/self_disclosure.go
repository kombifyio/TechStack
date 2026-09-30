package core

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// OperatorSelfDisclosure is the voluntary self-description an operator gives
// before or during creation (CREATION-EXPERIENCE-STANDARD §4, trust order 1).
// It carries raw answers only. The capability score and band are derived by
// the Unifier from these answers; no client supplies a score.
//
// Every field is optional: an operator may answer one question, all of them,
// or none. Unknown answer values are rejected so the stored document stays a
// closed vocabulary that later derivations can rely on.
type OperatorSelfDisclosure struct {
	Experience  string   `json:"experience,omitempty"`
	Goals       []string `json:"goals,omitempty"`
	Placement   string   `json:"placement,omitempty"`
	Hardware    []string `json:"hardware,omitempty"`
	Household   string   `json:"household,omitempty"`
	Motivations []string `json:"motivations,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

// OperatorSelfDisclosureRecord is the persisted self-disclosure of one
// authenticated subject together with the profile the Unifier derives from it.
type OperatorSelfDisclosureRecord struct {
	Disclosure OperatorSelfDisclosure     `json:"disclosure"`
	Profile    *OperatorCapabilityProfile `json:"profile,omitempty"`
	Source     string                     `json:"source,omitempty"`
	UpdatedAt  time.Time                  `json:"updated_at,omitempty"`
}

// Self-disclosure experience answers. There are five answers on purpose: the
// operator picks the sentence that fits, and the Unifier places it on the
// ten-level capability scale.
const (
	SelfDisclosureExperienceHandsOff = "hands-off"
	SelfDisclosureExperienceGuided   = "guided"
	SelfDisclosureExperienceCurious  = "curious"
	SelfDisclosureExperienceTechie   = "techie"
	SelfDisclosureExperienceExpert   = "expert"
)

// SelfDisclosureNotesMaxRunes bounds the free-text answer.
const SelfDisclosureNotesMaxRunes = 2000

var (
	selfDisclosureExperiences = []string{
		SelfDisclosureExperienceHandsOff,
		SelfDisclosureExperienceGuided,
		SelfDisclosureExperienceCurious,
		SelfDisclosureExperienceTechie,
		SelfDisclosureExperienceExpert,
	}
	// Goals follow the canonical use-case goal vocabulary of the creation
	// wizard (app/src/lib/wizard/standardBundle.ts CANONICAL_USE_CASE_GOALS).
	selfDisclosureGoals       = []string{"photos", "media", "vault", "files", "smart-home", "ai", "dev", "mail", "game"}
	selfDisclosurePlacements  = []string{"home", "rented", "both", "unsure"}
	selfDisclosureHardware    = []string{"none-yet", "old-pc", "mini-pc", "nas", "raspberry-pi", "rented-server"}
	selfDisclosureHouseholds  = []string{"just-me", "household", "friends-family", "small-team"}
	selfDisclosureMotivations = []string{"privacy", "replace-subscriptions", "learning", "family", "projects"}
)

// Normalize trims, lower-cases and de-duplicates every answer and returns the
// first vocabulary violation. The receiver is modified in place.
func (d *OperatorSelfDisclosure) Normalize() error {
	var err error
	if d.Experience, err = normalizeSelfDisclosureChoice("experience", d.Experience, selfDisclosureExperiences); err != nil {
		return err
	}
	if d.Goals, err = normalizeSelfDisclosureChoices("goals", d.Goals, selfDisclosureGoals); err != nil {
		return err
	}
	if d.Placement, err = normalizeSelfDisclosureChoice("placement", d.Placement, selfDisclosurePlacements); err != nil {
		return err
	}
	if d.Hardware, err = normalizeSelfDisclosureChoices("hardware", d.Hardware, selfDisclosureHardware); err != nil {
		return err
	}
	if d.Household, err = normalizeSelfDisclosureChoice("household", d.Household, selfDisclosureHouseholds); err != nil {
		return err
	}
	if d.Motivations, err = normalizeSelfDisclosureChoices("motivations", d.Motivations, selfDisclosureMotivations); err != nil {
		return err
	}
	d.Notes = strings.TrimSpace(d.Notes)
	if utf8.RuneCountInString(d.Notes) > SelfDisclosureNotesMaxRunes {
		return fmt.Errorf("notes must not exceed %d characters", SelfDisclosureNotesMaxRunes)
	}
	return nil
}

// IsEmpty reports whether the operator disclosed nothing at all.
func (d OperatorSelfDisclosure) IsEmpty() bool {
	return d.Experience == "" && len(d.Goals) == 0 && d.Placement == "" &&
		len(d.Hardware) == 0 && d.Household == "" && len(d.Motivations) == 0 &&
		strings.TrimSpace(d.Notes) == ""
}

func normalizeSelfDisclosureChoice(field, value string, allowed []string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
}

func normalizeSelfDisclosureChoices(field string, values []string, allowed []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := normalizeSelfDisclosureChoice(field, value, allowed)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
