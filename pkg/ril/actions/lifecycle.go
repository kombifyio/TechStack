// Package actions manages the RIL action-card lifecycle and persistence.
package actions

import "fmt"

// Transition represents a status change from one state to another.
type Transition struct {
	From CardStatus
	To   CardStatus
}

// validTransitions defines the allowed state machine for action cards.
//
//	pending  -> approved | dismissed
//	approved -> executing
//	executing -> completed | failed
//	failed   -> approved  (retry)
var validTransitions = map[Transition]bool{
	{StatusPending, StatusApproved}:    true,
	{StatusPending, StatusDismissed}:   true,
	{StatusApproved, StatusExecuting}:  true,
	{StatusExecuting, StatusCompleted}: true,
	{StatusExecuting, StatusFailed}:    true,
	{StatusFailed, StatusApproved}:     true, // retry
}

// ValidateTransition returns an error if the transition is not allowed.
func ValidateTransition(from, to CardStatus) error {
	if from == to {
		return fmt.Errorf("no-op transition: status is already %s", from)
	}
	if !validTransitions[Transition{From: from, To: to}] {
		return fmt.Errorf("invalid transition: %s -> %s", from, to)
	}
	return nil
}
