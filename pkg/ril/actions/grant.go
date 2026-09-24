package actions

import (
	"time"

	rilaction "github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

// RefuseUnentitledStart denies a new RIL execution before any durable workflow
// run is created. Missing, unparseable, or expired grants and failed connector
// bindings are fail-closed entitlements, not later workflow failures.
func RefuseUnentitledStart(card *GovernedCard, input BeginExecution) error {
	if card == nil {
		return ErrCardNotFound
	}
	if _, err := liveGrantUntil(card, input.Now); err != nil {
		return err
	}
	return validateConnectorBinding(card, input)
}

func liveGrantUntil(card *GovernedCard, now time.Time) (time.Time, error) {
	if card == nil || card.Template.Grant == nil {
		return time.Time{}, ErrGrantRequired
	}
	grantUntil, err := time.Parse(time.RFC3339Nano, card.Template.Grant.ValidUntil)
	if err != nil || !now.Before(grantUntil) {
		return time.Time{}, ErrGrantRequired
	}
	return grantUntil, nil
}

func grantValidUntil(card *GovernedCard, now time.Time) time.Time {
	validUntil := now.Add(rilaction.MaxRequestValidity)
	grantUntil, err := liveGrantUntil(card, now)
	if err != nil {
		return validUntil
	}
	if grantUntil.Before(validUntil) {
		validUntil = grantUntil
	}
	if card.Approval != nil && card.Approval.ValidUntil.Before(validUntil) {
		validUntil = card.Approval.ValidUntil
	}
	return validUntil
}
