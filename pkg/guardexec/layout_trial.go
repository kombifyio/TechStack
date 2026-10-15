package guardexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/pkg/guardlayout"
)

// Layout states, reported next to the update states.
const (
	StateLayoutMigrated    = "guard_layout_migrated"
	StateLayoutRolledBack  = "guard_layout_rolled_back"
	layoutTrialPollDefault = 2 * time.Second
)

// LayoutTrialMarker is written to the handoff directory before an existing root
// Guard is switched to the privilege-split layout.
type LayoutTrialMarker struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Deadline  time.Time `json:"deadline"`
}

// WriteLayoutTrialMarker announces a layout trial to the new daemon, which
// confirms it after its first authenticated round trip.
func WriteLayoutTrialMarker(handoffDir string, marker LayoutTrialMarker) error {
	_ = os.Remove(filepath.Join(handoffDir, HandoffTrialReadyFile))
	return writeJSONAtomic(filepath.Join(handoffDir, HandoffTrialFile), marker, 0o644)
}

// RunLayoutTrial supervises the first run of the daemon on the split layout. It
// restarts the daemon and waits for its authenticated round trip. Without it the
// legacy unit is restored, so a failed migration leaves the host exactly as it
// was: enrollment, identity and journal were only re-owned, never replaced.
func RunLayoutTrial(ctx context.Context, opts Options, layout guardlayout.Options, id string, budget time.Duration) error {
	opts.defaults()
	if os.Geteuid() != 0 {
		return errors.New("the layout trial supervisor must run as root")
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(opts.HandoffDir, HandoffTrialFile))
	if err != nil {
		return fmt.Errorf("read the layout trial marker: %w", err)
	}
	var marker LayoutTrialMarker
	if err := json.Unmarshal(raw, &marker); err != nil || marker.ID != id {
		return errors.New("the layout trial marker does not name this trial")
	}
	if budget <= 0 {
		budget = DefaultTrialBudget
	}
	phase, err := guardlayout.LayoutTrialPhase(layout, id)
	if err != nil {
		return err
	}
	finish := func(state, reason string) error {
		if err := updater.record(UpdateStatus{State: state, Version: marker.Version, Reason: reason, TrialID: id}); err != nil {
			return err
		}
		if err := guardlayout.FinishLayoutTrial(layout, id); err != nil {
			return err
		}
		return os.Remove(filepath.Join(opts.HandoffDir, HandoffTrialFile))
	}
	if phase == "prepared" {
		if err := guardlayout.StartLayoutTrial(ctx, layout, id); err != nil {
			if outcome, readErr := guardlayout.LayoutTrialPhase(layout, id); readErr == nil && outcome == "rolled_back" {
				return errors.Join(err, finish(StateLayoutRolledBack, "layout_start_failed"))
			}
			return fmt.Errorf("start the prepared layout trial: %w", err)
		}
	} else if phase == "transferring" {
		if err := guardlayout.RestoreLegacy(ctx, layout); err != nil {
			return err
		}
		return finish(StateLayoutRolledBack, "interrupted_custody_transfer")
	} else if phase == "rolled_back" {
		return finish(StateLayoutRolledBack, "resumed_layout_rollback")
	}
	deadline := time.Now().Add(budget)
	if !marker.Deadline.IsZero() && marker.Deadline.Before(deadline) {
		deadline = marker.Deadline
	}
	ticker := time.NewTicker(layoutTrialPollDefault)
	defer ticker.Stop()
	for {
		if readyFile, err := os.ReadFile(filepath.Join(opts.HandoffDir, HandoffTrialReadyFile)); err == nil || phase == "committed" {
			var ready TrialReady
			if phase == "committed" || (json.Unmarshal(readyFile, &ready) == nil && ready.TrialID == id && !ready.At.Before(marker.StartedAt)) {
				if err := guardlayout.CommitLayoutTrial(layout, id); err != nil {
					return err
				}
				return finish(StateLayoutMigrated, "")
			}
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			if err := guardlayout.RestoreLegacy(ctx, layout); err != nil {
				return errors.Join(ctx.Err(), err)
			}
			return errors.Join(ctx.Err(), finish(StateLayoutRolledBack, "layout_trial_cancelled"))
		case <-ticker.C:
		}
	}
	opts.Log.Error("guard_layout_trial_failed", "reason", "not_confirmed_within_budget")
	if err := guardlayout.RestoreLegacy(ctx, layout); err != nil {
		return fmt.Errorf("restore the legacy layout: %w", err)
	}
	return finish(StateLayoutRolledBack, "trial_not_confirmed_within_budget")
}
