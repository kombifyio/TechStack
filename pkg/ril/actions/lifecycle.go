// Package actions manages the RIL action-card lifecycle and persistence.
package actions

// CardStatus is the action-card state the lifecycle machine transitions
// between.
type CardStatus string

const (
	StatusPending   CardStatus = "pending"
	StatusApproved  CardStatus = "approved"
	StatusExecuting CardStatus = "executing"
	StatusCompleted CardStatus = "completed"
	StatusDismissed CardStatus = "dismissed"
	StatusFailed    CardStatus = "failed"
)
