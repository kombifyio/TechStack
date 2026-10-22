package jobs

import (
	"errors"
	"strings"
)

// stackKitHostPreflightRefusal is the StackKits apply outcome for a host whose
// own preflight refused every mutation before anything ran (for example no
// Docker binary, or ports it cannot verify). The text is the stable StackKits
// CLI message; StackKits applied nothing.
const stackKitHostPreflightRefusal = "host preflight refused to mutate this host"

// ownerHostRefusalError marks a rollout failure that the owner's own server
// reported about itself. It is an expected outcome of that server's state, not
// a Techstack fault: the job still fails with the same message and guidance,
// but it is not reported to Sentry as a server error.
type ownerHostRefusalError struct {
	Err error
}

func (e *ownerHostRefusalError) Error() string { return e.Err.Error() }

func (e *ownerHostRefusalError) Unwrap() error { return e.Err }

// classifyOwnerHostRefusal wraps err when it is a StackKits host preflight
// refusal on a server the owner runs. Managed servers are kombify's own
// infrastructure, so a refusal there stays a reportable fault.
func classifyOwnerHostRefusal(err error, managedRuntime bool) error {
	if err == nil || managedRuntime {
		return err
	}
	var operationErr *typedStackKitOperationError
	if !errors.As(err, &operationErr) || operationErr.Operation != StackKitLifecycleApply || operationErr.Err == nil {
		return err
	}
	if !strings.Contains(strings.ToLower(operationErr.Err.Error()), stackKitHostPreflightRefusal) {
		return err
	}
	return &ownerHostRefusalError{Err: err}
}

// isOwnerHostRefusal reports whether err carries an owner host refusal.
func isOwnerHostRefusal(err error) bool {
	var refusal *ownerHostRefusalError
	return errors.As(err, &refusal)
}
