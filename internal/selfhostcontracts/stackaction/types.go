// Package stackaction defines the self-hosted StackKits action subset. It is
// intentionally limited to the typed rollout-verification action exposed by
// the Open-Core runtime.
package stackaction

import (
	"errors"
	"strings"
)

// Action is a typed, allowlisted StackKits action.
type Action string

// ActionVerifyRollout asks the pinned StackKits CLI to verify a rollout.
const ActionVerifyRollout Action = "verify_rollout"

// ErrInvalidVerifyRolloutRequest reports an invalid self-host verification
// request.
var ErrInvalidVerifyRolloutRequest = errors.New("stackaction: invalid verify rollout request")

// RuntimeTarget is the operator-owned runtime endpoint described by a verify
// request. It carries no credentials.
type RuntimeTarget struct {
	Host string `json:"host"`
	User string `json:"user"`
}

// VerifyRolloutRequest is the bounded wire shape accepted by the self-host
// rollout-verification path.
type VerifyRolloutRequest struct {
	Action        Action        `json:"action"`
	StackID       string        `json:"stack_id"`
	RuntimeTarget RuntimeTarget `json:"runtime_target"`
}

// Validate checks that a request cannot select an arbitrary StackKits action
// or an empty target.
func (r VerifyRolloutRequest) Validate() error {
	if r.Action != ActionVerifyRollout || strings.TrimSpace(r.StackID) == "" ||
		strings.TrimSpace(r.RuntimeTarget.Host) == "" ||
		strings.TrimSpace(r.RuntimeTarget.User) == "" {
		return ErrInvalidVerifyRolloutRequest
	}
	return nil
}
