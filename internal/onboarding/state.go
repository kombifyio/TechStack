package onboarding

import "strings"

// Record is onboarding-state.v1, the per-principal document. Field names are
// the wire; the client's core reads exactly this shape.
type Record struct {
	SchemaVersion  int               `json:"schema_version"`
	JourneyID      string            `json:"journey_id"`
	JourneyVersion int               `json:"journey_version"`
	Completed      []CompletionEntry `json:"completed"`
	SeenSteps      []string          `json:"seen_steps"`
	Status         string            `json:"status"`
	DismissedAt    *string           `json:"dismissed_at"`
	ReopenedAt     *string           `json:"reopened_at"`
	Revision       int               `json:"revision"`
	UpdatedAt      string            `json:"updated_at"`
}

// CompletionEntry records one completed step and who asserted it.
type CompletionEntry struct {
	StepID string `json:"step_id"`
	At     string `json:"at"`
	Source string `json:"source"`
}

// Action is a user-driven state transition.
type Action string

const (
	ActionComplete Action = "complete"
	ActionSkip     Action = "skip"
	ActionDismiss  Action = "dismiss"
	ActionResume   Action = "resume"
	ActionReset    Action = "reset"
	// ActionCoachSeen is carried on the same route but is not an Action in the
	// core's sense; the handler maps it to markCoachSeen.
	ActionCoachSeen Action = "coach_seen"

	completionSourceDerived  = "derived"
	completionSourceReported = "reported"
	completionSourceManual   = "manual"
	completionSourceImport   = "import"
	recordStatusActive       = "active"
	recordStatusCompleted    = "completed"
	recordStatusDismissed    = "dismissed"
)

func validCompletionSource(source string) bool {
	return source == completionSourceDerived || source == completionSourceReported || source == completionSourceManual || source == completionSourceImport
}

func (r Record) hasCompleted(stepID string) bool {
	for _, entry := range r.Completed {
		if entry.StepID == stepID {
			return true
		}
	}
	return false
}

func (r Record) hasSeen(stepID string) bool {
	for _, id := range r.SeenSteps {
		if id == stepID {
			return true
		}
	}
	return false
}

func (r Record) clone() Record {
	out := r
	out.Completed = append([]CompletionEntry(nil), r.Completed...)
	out.SeenSteps = append([]string(nil), r.SeenSteps...)
	return out
}

func emptyRecord(journey Journey, now string) Record {
	return Record{
		SchemaVersion:  1,
		JourneyID:      journey.JourneyID,
		JourneyVersion: journey.JourneyVersion,
		Completed:      []CompletionEntry{},
		SeenSteps:      []string{},
		Status:         recordStatusActive,
		Revision:       0,
		UpdatedAt:      now,
	}
}

// migrateRecord normalizes a persisted (or imported) document against the current
// descriptor. Unknown step ids are dropped rather than rejected: a descriptor
// may retire a step, and a stale entry must never make the whole record
// unreadable. Semantics match the core's `migrate` line for line.
func migrateRecord(raw map[string]any, journey Journey, now string) Record {
	if raw == nil {
		return emptyRecord(journey, now)
	}
	known := journeyStepSet(journey)

	out := emptyRecord(journey, now)
	out.Completed = migrateCompletions(raw["completed"], known)
	out.SeenSteps = migrateSeenSteps(raw["seen_steps"], known)
	if status, ok := raw["status"].(string); ok && (status == recordStatusCompleted || status == recordStatusDismissed) {
		out.Status = status
	}
	if v, ok := raw["journey_version"].(float64); ok && v >= 1 && v == float64(int(v)) {
		out.JourneyVersion = int(v)
	} else {
		out.JourneyVersion = 1
	}
	if out.Status == recordStatusDismissed {
		// A dismissal with no timestamp cannot be cooled down, so treat it as
		// dismissed NOW rather than silently reopening the journey.
		if at, ok := raw["dismissed_at"].(string); ok {
			out.DismissedAt = &at
		} else {
			at := now
			out.DismissedAt = &at
		}
	}
	if at, ok := raw["reopened_at"].(string); ok {
		out.ReopenedAt = &at
	}
	if v, ok := raw["revision"].(float64); ok && v >= 0 && v == float64(int(v)) {
		out.Revision = int(v)
	}
	if at, ok := raw["updated_at"].(string); ok {
		out.UpdatedAt = at
	}
	return out
}

func journeyStepSet(journey Journey) map[string]bool {
	known := make(map[string]bool, len(journey.Steps))
	for _, step := range journey.Steps {
		known[step.ID] = true
	}
	return known
}

func migrateCompletions(raw any, known map[string]bool) []CompletionEntry {
	completed := []CompletionEntry{}
	seen := map[string]bool{}
	list, ok := raw.([]any)
	if !ok {
		return completed
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stepID, _ := entry["step_id"].(string)
		at, atOK := entry["at"].(string)
		source, _ := entry["source"].(string)
		if !known[stepID] || seen[stepID] || !atOK || !validCompletionSource(source) {
			continue
		}
		seen[stepID] = true
		completed = append(completed, CompletionEntry{StepID: stepID, At: at, Source: source})
	}
	return completed
}

func migrateSeenSteps(raw any, known map[string]bool) []string {
	seenSteps := []string{}
	list, ok := raw.([]any)
	if !ok {
		return seenSteps
	}
	seen := map[string]bool{}
	for _, item := range list {
		stepID, ok := item.(string)
		if !ok || !known[stepID] || seen[stepID] {
			continue
		}
		seen[stepID] = true
		seenSteps = append(seenSteps, stepID)
	}
	return seenSteps
}

// mergeDerivedSignals folds server-computed predicate results into the
// record. A predicate that flips back to false never removes an existing
// completion: merges are monotonic, and a manual tick survives a resolver
// that disagrees with it (standard §3). Returns changed=false when nothing
// was added, so the caller can skip the write.
func mergeDerivedSignals(record Record, signals map[string]bool, journey Journey, now string) (Record, bool) {
	var additions []CompletionEntry
	for _, step := range journey.Steps {
		if step.Completion.Kind != completionSourceDerived || record.hasCompleted(step.ID) {
			continue
		}
		if !signals[step.Completion.PredicateID] {
			continue
		}
		additions = append(additions, CompletionEntry{StepID: step.ID, At: now, Source: completionSourceDerived})
	}
	if len(additions) == 0 {
		return record, false
	}
	out := record.clone()
	out.Completed = append(out.Completed, additions...)
	out.UpdatedAt = now
	return out, true
}

// reopenForVersion moves a stale record onto the current descriptor version.
// seen_steps is deliberately kept: a coach-mark that fired stays fired, so a
// version bump can never re-interrupt a user with a tip they dismissed
// (standard §4, §6 rule 8). An explicit dismissal is respected.
func reopenForVersion(record Record, journey Journey, now string) (Record, bool) {
	if record.JourneyVersion >= journey.JourneyVersion {
		return record, false
	}
	out := record.clone()
	out.JourneyVersion = journey.JourneyVersion
	out.UpdatedAt = now
	if journey.ReopenOnVersionBump && record.Status != recordStatusDismissed {
		out.Status = recordStatusActive
		reopened := now
		out.ReopenedAt = &reopened
	}
	return out, true
}

// applySignal applies a user-driven action. Returns changed=false when the
// action is a no-op for this record. A client may only assert completion of a
// step the contract lets it assert: cost-bearing steps are derived-only (or a
// manual tick via skip), so this can never become an entitlement bypass.
func applySignal(record Record, journey Journey, action Action, stepID, source, now string) (Record, bool) {
	bump := func(mutate func(r *Record)) (Record, bool) {
		out := record.clone()
		mutate(&out)
		out.Revision = record.Revision + 1
		out.UpdatedAt = now
		return out, true
	}
	switch action {
	case ActionComplete, ActionSkip:
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			return record, false
		}
		step, ok := journey.Step(stepID)
		if !ok || record.hasCompleted(stepID) {
			return record, false
		}
		if source == "" {
			if action == ActionSkip {
				source = completionSourceManual
			} else {
				source = completionSourceReported
			}
		}
		if step.CostBearing && source != completionSourceDerived && source != completionSourceManual {
			return record, false
		}
		return bump(func(r *Record) {
			r.Completed = append(r.Completed, CompletionEntry{StepID: stepID, At: now, Source: source})
		})
	case ActionDismiss:
		if record.Status == recordStatusDismissed {
			return record, false
		}
		return bump(func(r *Record) {
			r.Status = recordStatusDismissed
			at := now
			r.DismissedAt = &at
		})
	case ActionResume:
		if record.Status == recordStatusActive {
			return record, false
		}
		return bump(func(r *Record) {
			r.Status = recordStatusActive
			r.DismissedAt = nil
			at := now
			r.ReopenedAt = &at
		})
	case ActionReset:
		return bump(func(r *Record) {
			r.Completed = []CompletionEntry{}
			r.SeenSteps = []string{}
			r.Status = recordStatusActive
			r.DismissedAt = nil
			at := now
			r.ReopenedAt = &at
			r.JourneyVersion = journey.JourneyVersion
		})
	default:
		return record, false
	}
}

// markCoachSeen records that a step's coach-mark has been shown. Idempotent.
func markCoachSeen(record Record, stepID, now string) (Record, bool) {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" || record.hasSeen(stepID) {
		return record, false
	}
	out := record.clone()
	out.SeenSteps = append(out.SeenSteps, stepID)
	out.Revision = record.Revision + 1
	out.UpdatedAt = now
	return out, true
}

// costBearingRefusesReported is the one rule the handler must decide BEFORE
// any write: a reported completion of a cost-bearing step is a 403, not a
// silent no-op, so the client can explain why.
func costBearingRefusesReported(journey Journey, action Action, stepID, source string) bool {
	if action != ActionComplete {
		return false
	}
	step, ok := journey.Step(strings.TrimSpace(stepID))
	if !ok || !step.CostBearing {
		return false
	}
	if source == "" {
		source = completionSourceReported
	}
	return source != completionSourceDerived && source != completionSourceManual
}
