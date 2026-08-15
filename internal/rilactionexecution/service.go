// Package rilactionexecution owns TechStack's durable execution coordination
// around the provider-free StackKits RIL action handoff.
package rilactionexecution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

var (
	ErrExecutionInProgress   = errors.New("RIL action execution is already in progress")
	ErrExecutionConflict     = errors.New("RIL action idempotency identity conflicts with another request")
	ErrRemoteExecutionFailed = errors.New("StackKits completed the RIL action with failure evidence")
)

// Dispatcher delivers one already-admitted provider-free request to the
// StackKits-owned action endpoint. It cannot select a provider or primitive.
type Dispatcher interface {
	Execute(context.Context, rilaction.Request) (rilaction.Evidence, error)
}

type Config struct {
	Ledger     rilaction.ExecutionLedger
	Dispatcher Dispatcher
	Now        func() time.Time
}

type Service struct {
	ledger     rilaction.ExecutionLedger
	dispatcher Dispatcher
	now        func() time.Time
}

func New(config Config) (*Service, error) {
	if config.Ledger == nil || config.Dispatcher == nil {
		return nil, fmt.Errorf("rilactionexecution: ledger and dispatcher are required")
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{ledger: config.Ledger, dispatcher: config.Dispatcher, now: config.Now}, nil
}

// Execute atomically reserves the request, dispatches at most once, validates
// StackKits evidence, and token-fences its durable completion.
func (s *Service) Execute(ctx context.Context, request rilaction.Request, executionAdmissionDigest string) (rilaction.Evidence, error) {
	if s == nil {
		return rilaction.Evidence{}, fmt.Errorf("rilactionexecution: service is nil")
	}
	now := s.now()
	if now.Location() != time.UTC {
		return rilaction.Evidence{}, fmt.Errorf("rilactionexecution: trusted clock must return UTC")
	}
	requestDigest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	reservationRequest, err := rilaction.NewLedgerReservationRequest(request, requestDigest, executionAdmissionDigest, now)
	if err != nil {
		return rilaction.Evidence{}, err
	}
	reservation, err := s.ledger.Reserve(ctx, reservationRequest)
	if err != nil {
		return rilaction.Evidence{}, fmt.Errorf("reserve RIL action execution: %w", err)
	}
	if err := rilaction.ValidateLedgerReservation(request, reservationRequest, executionAdmissionDigest, reservation); err != nil {
		return rilaction.Evidence{}, fmt.Errorf("validate RIL action reservation: %w", err)
	}
	switch reservation.Disposition {
	case rilaction.LedgerReplay:
		evidence := *reservation.Evidence
		if evidence.Status == "failed" {
			return evidence, ErrRemoteExecutionFailed
		}
		return evidence, nil
	case rilaction.LedgerInProgress:
		return rilaction.Evidence{}, ErrExecutionInProgress
	case rilaction.LedgerConflict:
		return rilaction.Evidence{}, ErrExecutionConflict
	}

	evidence, dispatchErr := s.dispatcher.Execute(ctx, request)
	if err := rilaction.ValidateEvidenceForRequest(request, evidence); err != nil {
		if dispatchErr != nil {
			return rilaction.Evidence{}, fmt.Errorf("StackKits dispatch failed without valid evidence: %w", dispatchErr)
		}
		return rilaction.Evidence{}, fmt.Errorf("StackKits returned invalid RIL action evidence: %w", err)
	}
	if dispatchErr != nil && evidence.Status != "failed" {
		return rilaction.Evidence{}, fmt.Errorf("StackKits dispatch error contradicted successful evidence: %w", dispatchErr)
	}
	completion, err := rilaction.NewLedgerCompletion(request, reservationRequest, executionAdmissionDigest, reservation.ReservationToken, evidence)
	if err != nil {
		return rilaction.Evidence{}, fmt.Errorf("construct RIL action completion: %w", err)
	}
	if err := s.ledger.Complete(ctx, completion); err != nil {
		return rilaction.Evidence{}, fmt.Errorf("commit RIL action completion: %w", err)
	}
	if dispatchErr != nil {
		return evidence, fmt.Errorf("StackKits dispatch failed with durable failure evidence: %w", dispatchErr)
	}
	if evidence.Status == "failed" {
		return evidence, ErrRemoteExecutionFailed
	}
	return evidence, nil
}
